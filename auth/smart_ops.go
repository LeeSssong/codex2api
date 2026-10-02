package auth

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/codex2api/smartops"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type smartOpsObservation struct {
	id      int64
	success bool
	epoch   int64
}

func (s *Store) ResolveSmartOpsBPSSessionProxy(account *Account, session string) (string, error) {
	if account == nil {
		return "", errors.New("account missing")
	}
	s.mu.RLock()
	pool := append([]string{}, s.proxyPool...)
	s.mu.RUnlock()
	if len(pool) == 0 {
		return "", errors.New("native proxy pool has no active proxy")
	}
	offset := int(affinityKeyHash(strconv.FormatInt(account.DBID, 10)+"|"+session) % uint64(len(pool)))
	for i := range pool {
		url := strings.TrimSpace(pool[(offset+i)%len(pool)])
		if url != "" && !s.ManagedProxyUnavailable(url) {
			return url, nil
		}
	}
	return "", errors.New("native proxy pool has no usable proxy")
}

type smartOpsSnapshot struct {
	config  smartops.PriorityConfig
	signals map[int64]smartops.Signal
	enabled bool
}
type smartOpsAdapter struct {
	snapshot  atomic.Pointer[smartOpsSnapshot]
	observe   chan smartOpsObservation
	wg        sync.WaitGroup
	gate      smartops.PluginGate
	blocked   atomic.Bool
	autoEpoch atomic.Int64
}

func (s *Store) StartSmartOps(ctx context.Context, gate smartops.PluginGate, config func(context.Context) (smartops.OAuthAutoConfig, smartops.PriorityConfig, error)) {
	a := &smartOpsAdapter{observe: make(chan smartOpsObservation, 4096), gate: gate}
	s.smartOps.Store(a)
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		var revision string
		var lastSignals time.Time
		refresh := func() {
			oauth, p, e := config(ctx)
			if e != nil {
				return
			}
			if oauth.Revision != revision {
				revision = oauth.Revision
				a.blocked.Store(false)
			}
			if s.db != nil {
				if epoch, e := s.db.SmartOpsControlEpoch(ctx, smartops.PluginAutoConfig); e == nil {
					a.autoEpoch.Store(epoch)
				} else {
					a.blocked.Store(true)
				}
				s.db.SetSmartOpsBilling(oauth.ModelBilling, func() bool { return gate != nil && gate(context.Background(), smartops.PluginAutoConfig) })
			}
			enabled := gate != nil && gate(ctx, smartops.PluginPriorityScheduling) && p.Enabled
			signals := map[int64]smartops.Signal{}
			old := a.snapshot.Load()
			if old != nil {
				signals = old.signals
			}
			if enabled && s.db != nil && (old == nil || !old.enabled || time.Since(lastSignals) >= 30*time.Second || old.config.WindowMinutes != p.WindowMinutes) {
				signals, e = s.db.ReadSmartOpsSignals(ctx, p)
				if e != nil {
					enabled = false
					log.Printf("[smart-ops] priority refresh: %v", e)
				}
				lastSignals = time.Now()
			}
			a.snapshot.Store(&smartOpsSnapshot{config: p, signals: signals, enabled: enabled})
		}
		refresh()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				refresh()
			case observation := <-a.observe:
				if a.blocked.Load() || gate == nil || !gate(ctx, smartops.PluginAutoConfig) || s.db == nil {
					continue
				}
				c, _, e := config(ctx)
				if e != nil {
					continue
				}
				next, changed, e := s.db.RecordSmartOpsConcurrency(ctx, observation.id, c, observation.success, observation.epoch)
				if e != nil {
					log.Printf("[smart-ops] concurrency observation account=%d: %v", observation.id, e)
					continue
				}
				if changed {
					s.ApplyAccountSchedulerOverridePatch(observation.id, false, nil, true, &next, nil)
				}
			}
		}
	}()
}
func (s *Store) WaitSmartOps() {
	if a := s.smartOps.Load(); a != nil {
		a.wg.Wait()
	}
}
func (s *Store) observeSmartOps(acc *Account, success bool) {
	if a := s.smartOps.Load(); a != nil {
		select {
		case a.observe <- smartOpsObservation{id: acc.DBID, success: success, epoch: a.autoEpoch.Load()}:
		default:
			if !a.blocked.Swap(true) {
				log.Printf("[smart-ops] concurrency observation queue full; progression paused account=%d", acc.DBID)
			}
		}
	}
}

func (s *Store) smartOpsAcquire(apiKeyID int64, exclude map[int64]bool, filter AccountFilter, policy DispatchPolicy, modelScope ...string) (*Account, bool) {
	a := s.smartOps.Load()
	if a == nil {
		return nil, false
	}
	snapshot := a.snapshot.Load()
	if snapshot == nil || !snapshot.enabled || a.gate == nil || !a.gate(context.Background(), smartops.PluginPriorityScheduling) {
		return nil, false
	}
	if len(snapshot.config.Models) > 0 {
		matched := false
		for _, model := range modelScope {
			for _, pattern := range snapshot.config.Models {
				prefix, wild := strings.CutSuffix(pattern, "*")
				if strings.EqualFold(model, pattern) || wild && strings.HasPrefix(strings.ToLower(model), strings.ToLower(prefix)) {
					matched = true
				}
			}
		}
		if !matched {
			return nil, false
		}
	}
	candidates := []smartops.Candidate{}
	accounts := map[int64]*Account{}
	limits := map[int64]int64{}
	maxConcurrency := atomic.LoadInt64(&s.maxConcurrency)
	for _, acc := range s.accountSnapshotAccounts() {
		if len(snapshot.config.GroupIDs) > 0 {
			matches := false
			for _, id := range acc.GroupIDSnapshot() {
				for _, scope := range snapshot.config.GroupIDs {
					if id == scope {
						matches = true
					}
				}
			}
			if !matches {
				continue
			}
		}
		if exclude[acc.DBID] || !acc.dispatchableForPolicy(policy) || !s.accountAllowedForAPIKey(acc, apiKeyID) || filter != nil && !filter(acc) {
			continue
		}
		_, _, _, limit := acc.schedulerSnapshotForPolicy(maxConcurrency, policy)
		load := accountOccupiedRequests(acc)
		if limit <= 0 || load >= limit || s.accountHasBlockingCachedCooldown(acc, policy) {
			continue
		}
		signal := snapshot.signals[acc.DBID]
		signal.LoadPercent = float64(load) * 100 / float64(limit)
		signal.LastUsedUnix = atomic.LoadInt64(&acc.LastUsedAt) / int64(time.Second)
		signal.Protocol = acc.UpstreamType
		acc.mu.RLock()
		factor := acc.smartOpsLoadFactor
		if signal.Samples == 0 && acc.RecentResultsCnt > 0 {
			signal.QualityPercent = acc.recentSuccessRateLocked() * 100
			signal.Samples = acc.RecentResultsCnt
		}
		acc.mu.RUnlock()
		if factor > 1 {
			signal.LoadPercent *= float64(factor)
		}
		candidates = append(candidates, smartops.Candidate{ID: acc.DBID, Signal: signal, Eligible: true})
		accounts[acc.DBID] = acc
		limits[acc.DBID] = limit
	}
	for _, candidate := range smartops.RankCandidates(snapshot.config, candidates) {
		acc := accounts[candidate.ID]
		if !acc.dispatchableForPolicy(policy) || s.accountHasBlockingCachedCooldown(acc, policy) {
			continue
		}
		if s.tryAcquireAccount(acc, limits[candidate.ID], true) {
			s.fastSchedulerUpdate(acc)
			return acc, true
		}
	}
	if len(candidates) == 0 && len(snapshot.config.GroupIDs) > 0 {
		return nil, false
	}
	return nil, true
}

type prioritySchedulingModelKey struct{}

func WithPrioritySchedulingModel(ctx context.Context, model string) context.Context {
	return context.WithValue(ctx, prioritySchedulingModelKey{}, model)
}
func PrioritySchedulingModel(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	model, _ := ctx.Value(prioritySchedulingModelKey{}).(string)
	return model
}

// Accounts created from a deferred import seed receive the persisted native projection.
func (s *Store) ApplySmartOpsAccountRow(acc *Account) {
	if s.db == nil || acc == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	row, e := s.db.GetAccountByID(ctx, acc.DBID)
	if e != nil {
		return
	}
	acc.mu.Lock()
	if row.BaseConcurrencyOverride.Valid {
		n := row.BaseConcurrencyOverride.Int64
		acc.BaseConcurrencyOverride = &n
	}
	acc.ModelMapping = row.GetCredential("model_mapping")
	groups, e := s.db.GetAccountGroupIDs(ctx, acc.DBID)
	if e == nil {
		acc.GroupIDs = append([]int64{}, groups...)
	}
	factor, _ := row.GetCredentialInt64("smart_ops_load_factor")
	acc.smartOpsLoadFactor = int(factor)
	_ = json.Unmarshal([]byte(row.GetCredential("smart_ops_bps_defaults")), &acc.smartOpsBPS)
	acc.smartOpsBPSSet = row.GetCredential("smart_ops_bps_defaults") != ""
	acc.mu.Unlock()
	if p, ok := row.GetCredentialInt64("scheduler_priority"); ok {
		acc.SetSchedulerPriority(p)
	}
}
func (a *Account) SmartOpsBPSDefaults() (smartops.BPSDefaults, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	c := a.smartOpsBPS
	c.Models = append([]string{}, c.Models...)
	return c, a.smartOpsBPSSet
}
func (a *Account) SmartOpsBPSModelAllowed(model string) bool {
	c, set := a.SmartOpsBPSDefaults()
	if !set || c.AllModels {
		return true
	}
	for _, m := range c.Models {
		if m == model {
			return true
		}
	}
	return false
}

func (s *Store) rankSmartOpsFastCandidates(candidates []fastSchedulerCandidate) {
	a := s.smartOps.Load()
	if a == nil {
		return
	}
	snapshot := a.snapshot.Load()
	if snapshot == nil || !snapshot.enabled || a.gate == nil || !a.gate(context.Background(), smartops.PluginPriorityScheduling) {
		return
	}
	if len(snapshot.config.Models) > 0 || len(snapshot.config.GroupIDs) > 0 {
		return
	}
	scored := make([]smartops.Candidate, 0, len(candidates))
	for _, c := range candidates {
		signal := snapshot.signals[c.acc.DBID]
		_, _, _, limit := c.acc.schedulerSnapshot(atomic.LoadInt64(&s.maxConcurrency))
		if limit > 0 {
			signal.LoadPercent = float64(accountOccupiedRequests(c.acc)) * 100 / float64(limit)
		}
		signal.LastUsedUnix = atomic.LoadInt64(&c.acc.LastUsedAt) / int64(time.Second)
		scored = append(scored, smartops.Candidate{ID: c.acc.DBID, Eligible: true, Signal: signal})
	}
	ranked := smartops.RankCandidates(snapshot.config, scored)
	positions := map[int64]int{}
	for i, c := range ranked {
		positions[c.ID] = i
	}
	sort.SliceStable(candidates, func(i, j int) bool { return positions[candidates[i].acc.DBID] < positions[candidates[j].acc.DBID] })
}
