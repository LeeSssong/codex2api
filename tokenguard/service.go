package tokenguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/codex2api/database"
	"github.com/google/uuid"
	"strings"
	"sync"
	"time"
)

type Publisher interface {
	PublishTokenGuardCredentials(context.Context, database.TokenGuardJob, database.TokenGuardAccount, map[string]any) (*database.TokenGuardAccount, error)
	SyncTokenGuardAccount(context.Context, int64) error
}
type ExternalClient interface {
	Probe(context.Context, Config, string) ProbeResult
	Relogin(context.Context, Config, ReloginAccount) (map[string]any, error)
	Notify(context.Context, Config, string, string, bool)
}
type Service struct {
	db        *database.DB
	publisher Publisher
	client    ExternalClient
	owner     string
	start     sync.Once
	stop      sync.Once
	mu        sync.Mutex
	cancel    context.CancelFunc
	running   map[string]context.CancelFunc
	wg        sync.WaitGroup
	wake      chan struct{}
}

func NewService(db *database.DB, publisher Publisher, client ExternalClient) *Service {
	if client == nil {
		client = NewClient(nil)
	}
	return &Service{db: db, publisher: publisher, client: client, owner: uuid.NewString(), running: map[string]context.CancelFunc{}, wake: make(chan struct{}, 1)}
}
func (s *Service) Config(ctx context.Context) (Config, int64, error) {
	raw, v, err := s.db.TokenGuardConfig(ctx)
	if err != nil {
		return Config{}, 0, err
	}
	cfg := DefaultConfig()
	if err = json.Unmarshal([]byte(raw), &cfg); err != nil {
		return Config{}, 0, errors.New("凭证守护配置无法读取")
	}
	return cfg, v, nil
}
func (s *Service) SaveConfig(ctx context.Context, next Config) (Config, error) {
	old, v, err := s.Config(ctx)
	if err != nil {
		return Config{}, err
	}
	next = Normalize(RestoreSecrets(next, old))
	if err = ValidateConfig(next); err != nil {
		return Config{}, err
	}
	for _, entry := range next.ReloginAccounts {
		a, e := s.db.TokenGuardAccount(ctx, entry.AccountID)
		if e != nil || !a.Eligible() {
			return Config{}, errors.New("重登映射必须指向符合条件的OAuth账号")
		}
		email, _ := a.Identity()
		if email == "" || !strings.EqualFold(email, entry.Email) {
			return Config{}, errors.New("重登映射邮箱与账号身份不一致")
		}
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return Config{}, errors.New("配置保存失败")
	}
	if _, err = s.db.SaveTokenGuardConfig(ctx, string(raw), v); err != nil {
		return Config{}, err
	}
	s.cancelLocal()
	s.signal()
	return PublicConfig(next), nil
}
func (s *Service) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *Service) cancelLocal() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cancel := range s.running {
		cancel()
	}
}
func (s *Service) Cancel(ctx context.Context, id string) (bool, error) {
	ok, err := s.db.CancelTokenGuardJob(ctx, id)
	if err == nil && ok {
		s.mu.Lock()
		cancel := s.running[id]
		s.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	}
	return ok, err
}
func (s *Service) Run(ctx context.Context, accountID int64) (*database.TokenGuardJob, error) {
	cfg, v, err := s.Config(ctx)
	if err != nil {
		return nil, err
	}
	if err = ValidateConfig(cfg); err != nil {
		return nil, err
	}
	kind := "cycle"
	if accountID > 0 {
		kind = "relogin"
		a, e := s.db.TokenGuardAccount(ctx, accountID)
		if e != nil || !a.Eligible() {
			return nil, errors.New("账号不支持凭证守护")
		}
		if !s.inScope(ctx, accountID, cfg.GroupIDs) {
			return nil, errors.New("账号不在守护范围内")
		}
		if _, e = s.mapping(cfg, *a); e != nil {
			return nil, e
		}
		if cfg.ReloginEndpoint == "" {
			return nil, errors.New("请配置可信重登端点")
		}
	} else if cfg.ProbeEndpoint == "" {
		return nil, errors.New("请配置可信探活端点")
	}
	j, err := s.db.CreateTokenGuardJob(ctx, kind, accountID, v)
	if err == nil {
		s.signal()
	}
	return j, err
}
func (s *Service) inScope(ctx context.Context, id int64, groups []int64) bool {
	if len(groups) == 0 {
		return true
	}
	ids, err := s.db.GetAccountGroupIDs(ctx, id)
	if err != nil {
		return false
	}
	for _, a := range ids {
		for _, b := range groups {
			if a == b {
				return true
			}
		}
	}
	return false
}
func (s *Service) mapping(cfg Config, a database.TokenGuardAccount) (ReloginAccount, error) {
	email, workspace := a.Identity()
	for _, entry := range cfg.ReloginAccounts {
		if entry.AccountID == a.ID && email != "" && workspace != "" && strings.EqualFold(email, entry.Email) && entry.Password != "" {
			return entry, nil
		}
	}
	return ReloginAccount{}, errors.New("缺少与账号身份一致的明确重登映射")
}
func (s *Service) Start(parent context.Context) {
	s.start.Do(func() {
		ctx, cancel := context.WithCancel(parent)
		s.mu.Lock()
		s.cancel = cancel
		s.mu.Unlock()
		s.wg.Add(1)
		go s.loop(ctx)
	})
}
func (s *Service) Stop() {
	s.stop.Do(func() {
		s.mu.Lock()
		cancel := s.cancel
		s.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		s.cancelLocal()
		s.wg.Wait()
	})
}
func (s *Service) loop(ctx context.Context) {
	defer s.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	s.signal()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.wake:
		}
		if ctx.Err() != nil {
			return
		}
		s.tick(ctx)
	}
}
func (s *Service) tick(ctx context.Context) {
	enabled, err := s.db.TokenGuardModuleEnabled(ctx)
	if err != nil || !enabled {
		s.cancelLocal()
		return
	}
	cfg, v, err := s.Config(ctx)
	if err != nil {
		return
	}
	if cfg.Enabled && ValidateConfig(cfg) == nil {
		last, e := s.db.LatestTokenGuardJob(ctx)
		if e == nil && (last == nil || last.State != "running" && last.State != "queued" && last.UpdatedAt+int64(cfg.IntervalSeconds) <= time.Now().Unix()) {
			_, _ = s.db.CreateTokenGuardJob(ctx, "cycle", 0, v)
		}
	}
	j, err := s.db.ClaimTokenGuardJob(ctx, s.owner, time.Now())
	if err != nil || j == nil {
		return
	}
	jobCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.running[j.ID] = cancel
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		defer func() { s.mu.Lock(); delete(s.running, j.ID); s.mu.Unlock() }()
		s.execute(jobCtx, *j)
	}()
}

type storedState struct {
	AccountState
	Generation    int64
	ConfigVersion int64
}

func (s *Service) loadStates(ctx context.Context) (map[int64]storedState, error) {
	raws, err := s.db.TokenGuardStates(ctx)
	if err != nil {
		return nil, err
	}
	states := map[int64]storedState{}
	for _, raw := range raws {
		var st storedState
		if json.Unmarshal([]byte(raw), &st) == nil {
			states[st.AccountID] = st
		}
	}
	return states, nil
}
func (s *Service) execute(parent context.Context, j database.TokenGuardJob) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				renewCtx, done := context.WithTimeout(ctx, 5*time.Second)
				err := s.db.RenewTokenGuardJob(renewCtx, j, time.Now())
				done()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-heartbeatDone }()
	cfg, v, err := s.Config(ctx)
	if err != nil || v != j.ConfigVersion {
		_, _ = s.db.CancelTokenGuardJob(context.WithoutCancel(ctx), j.ID)
		return
	}
	states, err := s.loadStates(ctx)
	if err != nil {
		s.finish(j, "failed", Stats{}, "读取守护状态失败")
		return
	}
	var accounts []database.TokenGuardAccount
	if j.Kind == "relogin" {
		a, e := s.db.TokenGuardAccount(ctx, j.AccountID)
		err = e
		if e == nil && a.Eligible() && s.inScope(ctx, a.ID, cfg.GroupIDs) {
			accounts = []database.TokenGuardAccount{*a}
		} else if err == nil {
			err = errors.New("账号不在守护范围")
		}
	} else {
		accounts, err = s.db.TokenGuardAccounts(ctx, cfg.GroupIDs, cfg.MaxProbePerCycle)
	}
	if err != nil {
		s.finish(j, "failed", Stats{}, "读取守护账号失败")
		return
	}
	started := time.Now()
	stats := Stats{StartedAt: started.Unix()}
	var mu sync.Mutex
	var wg sync.WaitGroup
	workers := cfg.ProbeConcurrency
	if workers < 1 {
		workers = 1
	}
	queue := make(chan database.TokenGuardAccount)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for a := range queue {
				if ctx.Err() != nil {
					return
				}
				one := s.process(ctx, j, cfg, a, states[a.ID])
				mu.Lock()
				stats.Probed += one.Probed
				stats.Healthy += one.Healthy
				stats.AuthFailed += one.AuthFailed
				stats.Transient += one.Transient
				stats.Repaired += one.Repaired
				stats.StateFixed += one.StateFixed
				stats.Failed += one.Failed
				mu.Unlock()
			}
		}()
	}
enqueue:
	for _, a := range accounts {
		select {
		case queue <- a:
		case <-ctx.Done():
			break enqueue
		}
	}
	close(queue)
	wg.Wait()
	stats.DurationMS = time.Since(started).Milliseconds()
	state, message := "completed", "巡检完成"
	if j.Kind == "relogin" {
		message = "重登完成"
		if stats.Failed > 0 {
			state, message = "failed", "重登失败或结果已失效"
		}
	}
	if ctx.Err() != nil {
		state, message = "failed", "任务已中断，旧结果已失效"
	}
	s.finish(j, state, stats, message)
	pruneCtx, pruneCancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = s.db.PruneTokenGuardEvents(pruneCtx, time.Now().AddDate(0, 0, -30))
	pruneCancel()
}
func (s *Service) finish(j database.TokenGuardJob, state string, stats Stats, message string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, _ := json.Marshal(stats)
	_ = s.db.FinishTokenGuardJob(ctx, j, state, string(raw), message)
}
func (s *Service) process(ctx context.Context, j database.TokenGuardJob, cfg Config, a database.TokenGuardAccount, previous storedState) Stats {
	stats := Stats{}
	state := previous
	if state.Generation != a.Generation || state.ConfigVersion != j.ConfigVersion {
		state = storedState{}
	}
	state.AccountID = a.ID
	state.AccountName = a.Name
	state.AccountStatus = a.Status
	state.Schedulable = a.Schedulable()
	state.Generation = a.Generation
	state.ConfigVersion = j.ConfigVersion
	now := time.Now().UTC()
	state.UpdatedAt = now
	kind, detail := "manual_run", "手动重登"
	if j.Kind != "relogin" {
		result := s.client.Probe(ctx, cfg, a.Credential("access_token"))
		stats.Probed = 1
		state.ProbeState = result.State
		state.ProbeDetail = result.Detail
		state.LatencyMS = result.LatencyMS
		state.LastProbeAt = &now
		detail = result.Detail
		switch result.State {
		case "ok":
			stats.Healthy = 1
			state.FailStreak = 0
			state.NeedsRelogin = false
			kind = "probe_ok"
			if cfg.Enabled && cfg.RestoreSchedulable {
				fresh, changed, err := s.db.RecoverTokenGuardAccount(ctx, j, a)
				if err != nil {
					stats.Failed++
					return stats
				}
				a = *fresh
				if changed {
					stats.StateFixed++
					state.LastFixAt = &now
					state.LastFixAction = "状态自愈"
					state.LastFixResult = "已解除凭证守护自有隔离"
					kind = "state_fixed"
					detail = state.LastFixResult
					s.syncAccount(ctx, a.ID)
				}
			}
		case "auth":
			stats.AuthFailed = 1
			state.FailStreak++
			state.NeedsRelogin = true
			kind = "probe_auth"
			if state.FailStreak >= cfg.FailStreakThreshold && cfg.Enabled && cfg.RestoreSchedulable {
				fresh, changed, err := s.db.IsolateTokenGuardAccount(ctx, j, a)
				if err != nil {
					stats.Failed++
					return stats
				}
				a = *fresh
				if changed {
					s.syncAccount(ctx, a.ID)
				}
			}
		default:
			stats.Transient = 1
			state.FailStreak = 0
			kind = "probe_transient"
		}
	}
	shouldRelogin := j.Kind == "relogin" || state.ProbeState == "auth" && state.FailStreak >= cfg.FailStreakThreshold && cfg.Enabled && cfg.AutoRelogin
	if shouldRelogin && ctx.Err() == nil {
		// Persist the probe before external password/MFA may take 25 minutes.
		// A cancelled or crashed login must not discard the verified probe.
		state.AccountStatus = a.Status
		state.Schedulable = a.Schedulable()
		raw, _ := json.Marshal(state)
		if err := s.db.RecordTokenGuardState(ctx, j, a, string(raw), kind, detail, state.LatencyMS); err != nil {
			stats.Failed++
			return stats
		}
		entry, err := s.mapping(cfg, a)
		var updates map[string]any
		if err == nil {
			updates, err = s.client.Relogin(ctx, cfg, entry)
		}
		if err == nil && s.publisher == nil {
			err = errors.New("原生凭据发布器不可用")
		}
		if err == nil {
			var fresh *database.TokenGuardAccount
			fresh, err = s.publisher.PublishTokenGuardCredentials(ctx, j, a, updates)
			if err == nil {
				a = *fresh
				stats.Repaired++
				state.Generation = a.Generation
				state.FailStreak = 0
				state.NeedsRelogin = false
				kind = "relogin_ok"
				detail = "新凭据已安全写回"
				state.LastFixResult = "凭据修复成功，其他限制保持不变"
				if cfg.Enabled && cfg.RestoreSchedulable {
					restored, changed, recoverErr := s.db.RecoverTokenGuardAccount(ctx, j, a)
					if recoverErr == nil {
						a = *restored
						if changed {
							stats.StateFixed++
							state.LastFixResult = "凭据修复成功，已解除守护自有隔离"
							s.syncAccount(ctx, a.ID)
						}
					} else {
						state.LastFixResult = "凭据修复成功，恢复因账号变更跳过"
					}
				}
			}
		}
		state.LastFixAt = &now
		state.LastFixAction = "重登"
		if err != nil {
			stats.Failed++
			kind = "relogin_failed"
			detail = "重登失败或结果因账号/任务变更被拒绝"
			state.LastFixResult = detail
		}
	}
	state.AccountStatus = a.Status
	state.Schedulable = a.Schedulable()
	state.UpdatedAt = time.Now().UTC()
	raw, _ := json.Marshal(state)
	if err := s.db.RecordTokenGuardState(ctx, j, a, string(raw), kind, detail, state.LatencyMS); err != nil {
		stats.Failed++
		return stats
	}
	if kind == "relogin_ok" || kind == "state_fixed" {
		s.client.Notify(ctx, cfg, "凭证守护修复结果", fmt.Sprintf("账号#%d: %s；当前可调度=%t", a.ID, state.LastFixResult, state.Schedulable), cfg.NotifyOnFix)
	} else if kind == "relogin_failed" {
		s.client.Notify(ctx, cfg, "凭证守护重登失败", fmt.Sprintf("账号#%d: %s", a.ID, detail), cfg.NotifyOnFail)
	}
	return stats
}
func (s *Service) syncAccount(ctx context.Context, id int64) {
	if s.publisher != nil {
		_ = s.publisher.SyncTokenGuardAccount(ctx, id)
	}
}
func (s *Service) Events(ctx context.Context, before int64, limit int) ([]Event, int64, error) {
	rows, cursor, err := s.db.TokenGuardEvents(ctx, before, limit)
	if err != nil {
		return nil, 0, err
	}
	out := []Event{}
	for _, r := range rows {
		out = append(out, Event{ID: r.ID, AccountID: r.AccountID, AccountName: fmt.Sprintf("账号#%d", r.AccountID), Kind: r.Kind, Detail: r.Detail, LatencyMS: r.LatencyMS, CreatedAt: r.CreatedAt})
	}
	return out, cursor, nil
}
func (s *Service) Status(ctx context.Context) (Status, error) {
	cfg, _, err := s.Config(ctx)
	if err != nil {
		return Status{}, err
	}
	enabled, err := s.db.TokenGuardModuleEnabled(ctx)
	if err != nil {
		return Status{}, err
	}
	states, err := s.loadStates(ctx)
	if err != nil {
		return Status{}, err
	}
	accounts, err := s.db.TokenGuardAccounts(ctx, cfg.GroupIDs, 0)
	if err != nil {
		return Status{}, err
	}
	out := Status{Config: PublicConfig(cfg), Accounts: []AccountState{}, ModuleEnabled: enabled}
	for _, a := range accounts {
		state := states[a.ID].AccountState
		state.AccountID = a.ID
		state.AccountName = a.Name
		state.AccountStatus = a.Status
		state.Schedulable = a.Schedulable()
		out.Accounts = append(out.Accounts, state)
	}
	out.Events, _, err = s.Events(ctx, 0, 100)
	if err != nil {
		return Status{}, err
	}
	j, err := s.db.LatestTokenGuardJob(ctx)
	if err != nil {
		return Status{}, err
	}
	if j != nil {
		last := time.Unix(j.UpdatedAt, 0).UTC()
		out.Runtime = Runtime{JobID: j.ID, JobState: j.State, Cancellation: j.Cancellation, Running: j.State == "queued" || j.State == "running" || j.State == "cancelling", LastRun: &last, LastMessage: j.Message}
		_ = json.Unmarshal([]byte(j.Stats), &out.Runtime.Stats)
	}
	return out, nil
}
