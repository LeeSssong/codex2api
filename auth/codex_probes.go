package auth

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/codex2api/database"
)

type codexProbeRecord struct {
	generation int64
	result     database.CodexProbeResult
}

// BeginCodexCapabilityProbe ignores learned capability only. Administrative
// permissions, account health, model cooldowns and one-probe-per-account remain.
func (a *Account) BeginCodexCapabilityProbe(ctx context.Context, model string) (func(), string) {
	if !a.IsAvailable() {
		return nil, "account_unavailable"
	}
	if a.IsModelRateLimited(model) {
		return nil, "model_cooldown"
	}
	if err := a.RefreshCodexRoutes(ctx, time.Now()); err != nil {
		return nil, "configuration_unavailable"
	}
	generation := a.GetCredentialGeneration()
	r := &a.codexRoutes
	r.mu.Lock()
	defer r.mu.Unlock()
	s := a.codexPathSnapshotLocked(database.CodexPathBasispoints, model, time.Now(), generation)
	if !s.Allowed {
		return nil, "path_disabled"
	}
	if s.Health == "cooldown" || s.Health == "recovering" {
		return nil, "path_cooldown"
	}
	if r.probeRunning {
		return nil, "probe_in_progress"
	}
	r.probeRunning = true
	var once sync.Once
	return func() { once.Do(func() { r.mu.Lock(); r.probeRunning = false; r.mu.Unlock() }) }, ""
}

// SaveCodexCapabilityProbeResult rejects results from replaced credentials.
// The database serializes this check with updates from other server instances.
func (a *Account) SaveCodexCapabilityProbeResult(ctx context.Context, generation int64, result database.CodexProbeResult, evidence *database.CodexCapability) (bool, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.CredentialGeneration != generation {
		return false, nil
	}
	r := &a.codexRoutes
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, model := range []string{"", result.Model} {
		reset := r.facts[codexFactKey(result.Upstream, model)]
		if reset.Source == "admin_reset" && reset.ObservedAt >= result.StartedAt.UnixNano() {
			return false, nil
		}
	}
	key := codexFactKey(result.Upstream, result.Model) + "|" + result.Level
	if old, exists := r.probeResults[key]; exists && old.generation == generation && !old.result.StartedAt.Before(result.StartedAt) {
		return false, nil
	}
	if r.db != nil {
		applied, err := r.db.SaveCodexProbeResult(ctx, generation, result, evidence)
		if err != nil || !applied {
			return applied, err
		}
		if err := a.reloadCodexRoutesLocked(ctx, time.Now()); err != nil {
			return false, err
		}
	} else if evidence != nil {
		factKey := codexFactKey(evidence.Upstream, evidence.Model)
		reset := r.facts[codexFactKey(evidence.Upstream, "")]
		if r.facts[factKey].ObservedAt < evidence.ObservedAt && (reset.Source != "admin_reset" || reset.ObservedAt < evidence.ObservedAt) {
			if r.facts == nil {
				r.facts = map[string]database.CodexCapability{}
			}
			f := *evidence
			f.CredentialGeneration = generation
			f.Model = strings.ToLower(strings.TrimSpace(f.Model))
			r.facts[factKey] = f
		}
	}
	if r.probeResults == nil {
		r.probeResults = map[string]codexProbeRecord{}
	}
	if evidence != nil && evidence.Capability == database.CapabilitySupported {
		factKey := codexFactKey(evidence.Upstream, evidence.Model)
		if r.facts[factKey].ObservedAt == evidence.ObservedAt && r.health[factKey].ObservedAt <= evidence.ObservedAt {
			delete(r.health, factKey)
		}
	}
	r.probeResults[key] = codexProbeRecord{generation: generation, result: result}
	return true, nil
}

func (a *Account) GetCodexCapabilityProbeResults(ctx context.Context) ([]database.CodexProbeResult, error) {
	generation := a.GetCredentialGeneration()
	r := &a.codexRoutes
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.db != nil {
		return r.db.GetCodexCapabilityProbeResults(ctx, a.ID())
	}
	results := []database.CodexProbeResult{}
	for _, record := range r.probeResults {
		if record.generation == generation {
			results = append(results, record.result)
		}
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Model != results[j].Model {
			return results[i].Model < results[j].Model
		}
		return results[i].Level < results[j].Level
	})
	return results, nil
}
