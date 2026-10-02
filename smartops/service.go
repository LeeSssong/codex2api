package smartops

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

type PluginGate func(context.Context, string) bool

const (
	PluginAutoConfig         = "auto-config"
	PluginPriorityScheduling = "priority-scheduling"
	PluginPelicanTests       = "pelican-tests"
)

type JobRecord struct {
	PelicanJob
	Status    string
	Results   []PelicanResult
	CreatedAt time.Time
	Cancel    context.CancelFunc
}

// Runtime is the process boundary mounted by admin and scheduler adapters.
// Durable workers should replace Jobs with database leases; this registry
// keeps API state coherent while a lease is being executed.
type Runtime struct {
	mu       sync.RWMutex
	oauth    OAuthAutoConfig
	priority PriorityConfig
	jobs     map[int64]*JobRecord
	next     atomic.Int64
	gate     PluginGate
	Probe    PelicanProbe
	History  PelicanHistory
}

func NewRuntime(gate PluginGate) *Runtime {
	r := &Runtime{oauth: DefaultOAuthAutoConfig(), priority: DefaultPriorityConfig(), jobs: map[int64]*JobRecord{}, gate: gate}
	r.next.Store(0)
	return r
}
func (r *Runtime) enabled(ctx context.Context, id string) bool {
	return r.gate == nil || r.gate(ctx, id)
}
func (r *Runtime) Config(ctx context.Context) (OAuthAutoConfig, PriorityConfig, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.enabled(ctx, PluginAutoConfig) && !r.enabled(ctx, PluginPriorityScheduling) {
		return OAuthAutoConfig{}, PriorityConfig{}, errors.New("smart operations disabled")
	}
	return r.oauth, r.priority, nil
}
func (r *Runtime) SetOAuth(ctx context.Context, c OAuthAutoConfig) error {
	if !r.enabled(ctx, PluginAutoConfig) {
		return errors.New("auto configuration disabled")
	}
	if err := ValidateOAuthAutoConfig(c); err != nil {
		return err
	}
	r.mu.Lock()
	r.oauth = c
	r.mu.Unlock()
	return nil
}
func (r *Runtime) SetPriority(ctx context.Context, c PriorityConfig) error {
	if !r.enabled(ctx, PluginPriorityScheduling) {
		return errors.New("priority scheduling disabled")
	}
	if err := ValidatePriorityConfig(c); err != nil {
		return err
	}
	r.mu.Lock()
	r.priority = c
	r.mu.Unlock()
	return nil
}
func (r *Runtime) CreateJob(ctx context.Context, j PelicanJob) (*JobRecord, error) {
	if !r.enabled(ctx, PluginPelicanTests) {
		return nil, errors.New("pelican tests disabled")
	}
	if r.Probe == nil || r.History == nil {
		return nil, errors.New("pelican runtime is not connected")
	}
	j.ID = r.next.Add(1)
	runCtx, cancel := context.WithCancel(ctx)
	rec := &JobRecord{PelicanJob: j, Status: "queued", CreatedAt: time.Now().UTC(), Cancel: cancel}
	r.mu.Lock()
	r.jobs[j.ID] = rec
	r.mu.Unlock()
	go func() {
		r.mu.Lock()
		rec.Status = "running"
		r.mu.Unlock()
		results, err := RunGroup(runCtx, j, r.Probe, r.History)
		r.mu.Lock()
		rec.Results = results
		if err != nil {
			rec.Status = "cancelled"
		} else {
			rec.Status = "completed"
		}
		r.mu.Unlock()
	}()
	return rec, nil
}
func (r *Runtime) CancelJob(ctx context.Context, id int64) error {
	if !r.enabled(ctx, PluginPelicanTests) {
		return errors.New("pelican tests disabled")
	}
	r.mu.RLock()
	j := r.jobs[id]
	r.mu.RUnlock()
	if j == nil {
		return errors.New("job not found")
	}
	j.Cancel()
	return nil
}
func (r *Runtime) Jobs(ctx context.Context) []JobRecord {
	if !r.enabled(ctx, PluginPelicanTests) {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]JobRecord, 0, len(r.jobs))
	for _, j := range r.jobs {
		copy := *j
		copy.Cancel = nil
		out = append(out, copy)
	}
	return out
}
