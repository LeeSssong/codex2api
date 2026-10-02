package auth

import (
	"context"
	"encoding/json"
	"time"

	"github.com/codex2api/smartops"
)

// ApplySmartOpsOAuthDefaults applies only to a new OAuth account before it is
// persisted. It never changes existing accounts and leaves native eligibility
// and group validation to the normal import path.
func ApplySmartOpsOAuthDefaults(ctx context.Context, enabled func(context.Context, string) bool, cfg smartops.OAuthAutoConfig, acc *Account) bool {
	if acc == nil || !enabled(ctx, smartops.PluginAutoConfig) || !cfg.Enabled || acc.UpstreamType != "openai" {
		return false
	}
	acc.mu.Lock()
	defer acc.mu.Unlock()
	acc.SchedulerPriority = int64(cfg.Priority)
	n := int64(cfg.Concurrency)
	acc.BaseConcurrencyOverride = &n
	if acc.ModelMapping == "" {
		raw, _ := json.Marshal(cfg.ModelMappings)
		acc.ModelMapping = string(raw)
	}
	return true
}

// AdvanceSmartOpsConcurrency updates an already-successful native request.
// Persistence is injected by the caller so the normal account update/outbox
// transaction remains authoritative.
func AdvanceSmartOpsConcurrency(ctx context.Context, enabled func(context.Context, string) bool, cfg smartops.OAuthAutoConfig, state smartops.ConcurrencyState, acc *Account, persist func(context.Context, int64, int64, smartops.ConcurrencyState) error) error {
	if acc == nil || !enabled(ctx, smartops.PluginAutoConfig) || !cfg.Enabled {
		return nil
	}
	current, ok := acc.GetBaseConcurrencyOverride()
	if !ok {
		return nil
	}
	nextState, next := smartops.AdvanceConcurrency(state, int(current), cfg, smartops.ConcurrencyResult{Success: true, At: time.Now()}, time.Now())
	if next == int(current) {
		return nil
	}
	if err := persist(ctx, acc.DBID, int64(next), nextState); err != nil {
		return err
	}
	acc.mu.Lock()
	v := int64(next)
	acc.BaseConcurrencyOverride = &v
	acc.mu.Unlock()
	return nil
}
