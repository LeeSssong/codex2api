package smartops

import (
	"context"
	"errors"
	"fmt"
	"github.com/robfig/cron/v3"
	"log"
	"sync"
	"time"
)

type PluginGate func(context.Context, string) bool

const (
	PluginAutoConfig         = "auto-config"
	PluginPriorityScheduling = "priority-scheduling"
	PluginPelicanTests       = "pelican-tests"
)

var ErrDisabled = errors.New("plugin disabled")

type JobRecord struct {
	PelicanJob
	PlanID    int64           `json:"plan_id"`
	Status    string          `json:"status"`
	Results   []PelicanResult `json:"results"`
	CreatedAt time.Time       `json:"created_at"`
	Error     string          `json:"error,omitempty"`
}
type PelicanPlan struct {
	ID              int64      `json:"id"`
	Name            string     `json:"name"`
	Enabled         bool       `json:"enabled"`
	IntervalMinutes int        `json:"interval_minutes"`
	CronExpression  string     `json:"cron_expression"`
	NextRunAt       time.Time  `json:"next_run_at"`
	Job             PelicanJob `json:"job"`
}
type Store interface {
	LoadOAuthAutoConfig(context.Context) (OAuthAutoConfig, error)
	SaveOAuthAutoConfig(context.Context, OAuthAutoConfig) error
	LoadPriorityScheduling(context.Context) (PriorityConfig, error)
	SavePriorityScheduling(context.Context, PriorityConfig) error
	CreatePelicanJob(context.Context, PelicanJob, int64) (JobRecord, error)
	ListPelicanJobs(context.Context) ([]JobRecord, error)
	GetPelicanJob(context.Context, int64) (JobRecord, error)
	ClaimPelicanJob(context.Context, string, time.Time) (*JobRecord, error)
	PelicanJobActive(context.Context, int64, string, time.Time) (bool, error)
	FinishPelicanJob(context.Context, JobRecord, string) error
	CancelPelicanJob(context.Context, int64) error
	SavePelicanResult(context.Context, PelicanResult) error
	ListPelicanPlans(context.Context) ([]PelicanPlan, error)
	SavePelicanPlan(context.Context, PelicanPlan) (PelicanPlan, error)
	DeletePelicanPlan(context.Context, int64) error
	EnqueueDuePelicanPlans(context.Context, time.Time) error
}
type PelicanExecutor func(context.Context, PelicanJob, int) (PelicanResult, error)
type Runtime struct {
	mu       sync.RWMutex
	configMu sync.Mutex
	oauth    OAuthAutoConfig
	priority PriorityConfig
	gate     PluginGate
	store    Store
	execute  PelicanExecutor
	parent   context.Context
	wg       sync.WaitGroup
	owner    string
	wake     chan struct{}
}

func NewRuntime(gate PluginGate) *Runtime {
	return &Runtime{gate: gate, oauth: DefaultOAuthAutoConfig(), priority: DefaultPriorityConfig(), wake: make(chan struct{}, 1)}
}
func (r *Runtime) Enabled(ctx context.Context, id string) bool {
	return r.gate != nil && r.gate(ctx, id)
}
func (r *Runtime) Start(ctx context.Context, store Store, execute PelicanExecutor) error {
	if store == nil || execute == nil {
		return errors.New("smart operations adapters required")
	}
	o, e := store.LoadOAuthAutoConfig(ctx)
	if e != nil {
		return e
	}
	p, e := store.LoadPriorityScheduling(ctx)
	if e != nil {
		return e
	}
	r.mu.Lock()
	if r.store != nil {
		r.mu.Unlock()
		return errors.New("runtime already started")
	}
	r.store = store
	r.execute = execute
	r.parent = ctx
	r.oauth = o
	r.priority = p
	r.owner = fmt.Sprintf("%d", time.Now().UnixNano())
	r.mu.Unlock()
	r.wg.Add(1)
	go r.worker()
	r.wg.Add(1)
	go r.refreshConfigs()
	return nil
}
func (r *Runtime) refreshConfigs() {
	defer r.wg.Done()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.parent.Done():
			return
		case <-ticker.C:
			r.configMu.Lock()
			o, e := r.store.LoadOAuthAutoConfig(r.parent)
			if e == nil {
				p, err := r.store.LoadPriorityScheduling(r.parent)
				if err == nil {
					r.mu.Lock()
					r.oauth = cloneOAuth(o)
					r.priority = clonePriority(p)
					r.mu.Unlock()
				}
			}
			r.configMu.Unlock()
		}
	}
}
func (r *Runtime) Wait() { r.wg.Wait() }
func (r *Runtime) Config(ctx context.Context) (OAuthAutoConfig, PriorityConfig, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return cloneOAuth(r.oauth), clonePriority(r.priority), nil
}
func clonePriority(c PriorityConfig) PriorityConfig {
	c.GroupIDs = append([]int64{}, c.GroupIDs...)
	c.Models = append([]string{}, c.Models...)
	return c
}
func cloneOAuth(c OAuthAutoConfig) OAuthAutoConfig {
	c.GroupIDs = append([]int64{}, c.GroupIDs...)
	c.UpgradeGroupIDs = append([]int64{}, c.UpgradeGroupIDs...)
	c.ModelMappings = append([]ModelMapping{}, c.ModelMappings...)
	c.BPS.Models = append([]string{}, c.BPS.Models...)
	c.ModelBilling.Rules = append([]ModelBillingRule{}, c.ModelBilling.Rules...)
	return c
}
func (r *Runtime) SetOAuth(ctx context.Context, c OAuthAutoConfig) error {
	r.configMu.Lock()
	defer r.configMu.Unlock()
	if !r.Enabled(ctx, PluginAutoConfig) {
		return ErrDisabled
	}
	if e := ValidateOAuthAutoConfig(c); e != nil {
		return e
	}
	c.UpdatedAt = time.Now().UTC()
	c.Revision = fmt.Sprint(c.UpdatedAt.UnixNano())
	if e := r.store.SaveOAuthAutoConfig(ctx, c); e != nil {
		return e
	}
	r.mu.Lock()
	r.oauth = cloneOAuth(c)
	r.mu.Unlock()
	return nil
}
func (r *Runtime) SetPriority(ctx context.Context, c PriorityConfig) error {
	r.configMu.Lock()
	defer r.configMu.Unlock()
	if !r.Enabled(ctx, PluginPriorityScheduling) {
		return ErrDisabled
	}
	if e := ValidatePriorityConfig(c); e != nil {
		return e
	}
	if e := r.store.SavePriorityScheduling(ctx, c); e != nil {
		return e
	}
	r.mu.Lock()
	r.priority = clonePriority(c)
	r.mu.Unlock()
	return nil
}
func (r *Runtime) signal() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}
func (r *Runtime) CreateJob(ctx context.Context, j PelicanJob) (*JobRecord, error) {
	if !r.Enabled(ctx, PluginPelicanTests) {
		return nil, ErrDisabled
	}
	if e := ValidatePelicanJob(j); e != nil {
		return nil, e
	}
	rec, e := r.store.CreatePelicanJob(ctx, j, 0)
	if e != nil {
		return nil, e
	}
	r.signal()
	return &rec, nil
}
func (r *Runtime) CancelJob(ctx context.Context, id int64) error {
	if !r.Enabled(ctx, PluginPelicanTests) {
		return ErrDisabled
	}
	return r.store.CancelPelicanJob(ctx, id)
}
func (r *Runtime) Jobs(ctx context.Context) ([]JobRecord, error) { return r.store.ListPelicanJobs(ctx) }
func (r *Runtime) GetJob(ctx context.Context, id int64) (JobRecord, error) {
	return r.store.GetPelicanJob(ctx, id)
}
func (r *Runtime) Plans(ctx context.Context) ([]PelicanPlan, error) {
	return r.store.ListPelicanPlans(ctx)
}
func (r *Runtime) SavePlan(ctx context.Context, p PelicanPlan) (PelicanPlan, error) {
	if !r.Enabled(ctx, PluginPelicanTests) {
		return p, ErrDisabled
	}
	if p.Name == "" || len(p.Name) > 200 || p.CronExpression == "" && (p.IntervalMinutes < 1 || p.IntervalMinutes > 43200) {
		return p, errors.New("invalid scheduled plan")
	}
	if p.CronExpression != "" {
		schedule, e := cron.ParseStandard(p.CronExpression)
		if e != nil {
			return p, e
		}
		p.NextRunAt = schedule.Next(time.Now().UTC())
	}
	if e := ValidatePelicanJob(p.Job); e != nil {
		return p, e
	}
	if p.NextRunAt.IsZero() {
		p.NextRunAt = time.Now().UTC()
	}
	p, e := r.store.SavePelicanPlan(ctx, p)
	r.signal()
	return p, e
}
func (r *Runtime) DeletePlan(ctx context.Context, id int64) error {
	if !r.Enabled(ctx, PluginPelicanTests) {
		return ErrDisabled
	}
	return r.store.DeletePelicanPlan(ctx, id)
}
func (r *Runtime) RunPlan(ctx context.Context, id int64) (JobRecord, error) {
	if !r.Enabled(ctx, PluginPelicanTests) {
		return JobRecord{}, ErrDisabled
	}
	ps, e := r.Plans(ctx)
	if e != nil {
		return JobRecord{}, e
	}
	for _, p := range ps {
		if p.ID == id {
			j, e := r.store.CreatePelicanJob(ctx, p.Job, id)
			r.signal()
			return j, e
		}
	}
	return JobRecord{}, errors.New("plan not found")
}
func (r *Runtime) worker() {
	defer r.wg.Done()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		if r.parent.Err() != nil {
			return
		}
		if r.Enabled(r.parent, PluginPelicanTests) {
			if e := r.store.EnqueueDuePelicanPlans(r.parent, time.Now().UTC()); e == nil {
				for r.parent.Err() == nil && r.Enabled(r.parent, PluginPelicanTests) {
					j, e := r.store.ClaimPelicanJob(r.parent, r.owner, time.Now().UTC())
					if e != nil || j == nil {
						break
					}
					r.run(*j)
				}
			}
		}
		select {
		case <-r.parent.Done():
			return
		case <-tick.C:
		case <-r.wake:
		}
	}
}

// The database lease is renewed while executing and gates cross-process cancellation.
func (r *Runtime) run(j JobRecord) {
	ctx, cancel := context.WithTimeout(r.parent, 10*time.Minute)
	defer cancel()
	done := make(chan struct{})
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				active, e := r.store.PelicanJobActive(ctx, j.ID, r.owner, time.Now().UTC())
				if e != nil || !active || !r.Enabled(ctx, PluginPelicanTests) {
					cancel()
					return
				}
			}
		}
	}()
	results := make([]PelicanResult, j.Samples)
	for _, result := range j.Results {
		if result.Sample >= 0 && result.Sample < len(results) {
			results[result.Sample] = result
		}
	}
	sem := make(chan struct{}, j.Parallel)
	var wg sync.WaitGroup
	var saveMu sync.Mutex
	var saveErr error
	for i := 0; i < j.Samples; i++ {
		if results[i].Status != "" {
			continue
		}
		if ctx.Err() != nil {
			break
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(sample int) {
			defer wg.Done()
			defer func() { <-sem }()
			started := time.Now()
			var result PelicanResult
			var e error
			var cost float64
			incomplete := false
			attempts := []PelicanAttempt{}
			for attempt := 0; attempt <= j.Retries; attempt++ {
				if ctx.Err() != nil {
					e = ctx.Err()
					break
				}
				result, e = r.executeSafe(ctx, j.PelicanJob, sample)
				if result.CostUSD != nil {
					cost += *result.CostUSD
				} else {
					incomplete = true
				}
				message := ""
				if e != nil {
					message = e.Error()
				}
				attempts = append(attempts, PelicanAttempt{AccountID: result.AccountID, Error: message, CostUSD: result.CostUSD})
				if e == nil {
					e = ValidatePelicanOutput(result.Output)
				}
				if e == nil || result.Output != "" {
					break
				}
			}
			result.JobID = j.ID
			result.CostUSD = &cost
			result.CostIncomplete = incomplete
			result.Attempts = attempts
			result.LeaseOwner = r.owner
			result.Sample = sample
			result.StartedAt = started
			result.FinishedAt = time.Now()
			result.Latency = result.FinishedAt.Sub(started)
			result.Status = "success"
			if e != nil {
				result.Status = "failed"
				result.Error = e.Error()
			}
			results[sample] = result
			persist, pCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer pCancel()
			if e := r.store.SavePelicanResult(persist, result); e != nil {
				saveMu.Lock()
				saveErr = e
				saveMu.Unlock()
				cancel()
			}
		}(i)
	}
	wg.Wait()
	close(done)
	<-watched
	j.Status = "completed"
	for _, v := range results {
		if v.Status != "success" {
			j.Status = "failed"
		}
	}
	if ctx.Err() != nil {
		j.Status = "cancelled"
		j.Error = ctx.Err().Error()
	}
	if r.parent.Err() != nil {
		j.Status = "interrupted"
	}
	if saveErr != nil {
		j.Status = "failed"
		j.Error = saveErr.Error()
	}
	final, finalCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finalCancel()
	if e := r.store.FinishPelicanJob(final, j, r.owner); e != nil {
		log.Printf("[smart-ops] pelican job=%d finalization failed: %v", j.ID, e)
	}
}
func (r *Runtime) executeSafe(ctx context.Context, j PelicanJob, sample int) (result PelicanResult, err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("native test executor interrupted")
		}
	}()
	return r.execute(ctx, j, sample)
}
