package auth

import (
	"context"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/codex2api/database"
)

type codexPathHealth struct {
	Until      time.Time
	Reason     string
	ObservedAt int64
	Probing    bool
	Rejections int
}

type codexAccountRoutes struct {
	mu           sync.Mutex
	db           *database.DB
	loadedAt     time.Time
	loadFailed   bool
	configs      map[string]bool
	facts        map[string]database.CodexCapability
	health       map[string]codexPathHealth
	probeRunning bool
	probeResults map[string]codexProbeRecord
}

// CodexPathSnapshot keeps administration, evidence and temporary health separate.
type CodexPathSnapshot struct {
	ExactModelSupported bool      `json:"-"`
	Upstream            string    `json:"upstream"`
	Model               string    `json:"model"`
	Allowed             bool      `json:"allowed"`
	Capability          string    `json:"capability"`
	Source              string    `json:"source,omitempty"`
	Reason              string    `json:"reason,omitempty"`
	ObservedAt          int64     `json:"observed_at,omitempty"`
	Health              string    `json:"health"`
	CooldownUntil       time.Time `json:"cooldown_until,omitempty"`
	HealthReason        string    `json:"health_reason,omitempty"`
}

func codexFactKey(path, model string) string {
	return path + "|" + strings.ToLower(strings.TrimSpace(model))
}

func (a *Account) attachCodexRouteDB(db *database.DB) {
	a.codexRoutes.mu.Lock()
	a.codexRoutes.db = db
	a.codexRoutes.mu.Unlock()
}

func (a *Account) ReloadCodexRoutes(ctx context.Context) error {
	a.codexRoutes.mu.Lock()
	defer a.codexRoutes.mu.Unlock()
	return a.reloadCodexRoutesLocked(ctx, time.Now())
}

func (a *Account) reloadCodexRoutesLocked(ctx context.Context, now time.Time) error {
	r := &a.codexRoutes
	if r.db == nil {
		return nil
	}
	configs, facts, err := r.db.GetCodexRoutes(ctx, a.ID())
	r.loadedAt, r.loadFailed = now, err != nil
	if err != nil {
		return err
	}
	r.configs = make(map[string]bool, len(configs))
	r.facts = make(map[string]database.CodexCapability, len(facts))
	for _, c := range configs {
		r.configs[c.Upstream] = c.Allowed
	}
	for _, f := range facts {
		r.facts[codexFactKey(f.Upstream, f.Model)] = f
	}
	return nil
}

func (a *Account) codexPathSnapshotLocked(path, model string, now time.Time, generation int64) CodexPathSnapshot {
	r := &a.codexRoutes
	s := CodexPathSnapshot{Upstream: path, Model: model, Allowed: true, Capability: database.CapabilityUnknown, Health: "ready"}
	if allowed, ok := r.configs[path]; ok {
		s.Allowed = allowed
	}
	if r.loadFailed {
		s.Allowed = false
		s.Health = "unavailable"
		s.HealthReason = "configuration_unavailable"
	}
	f, exists := r.facts[codexFactKey(path, model)]
	if f.CredentialGeneration != 0 && f.CredentialGeneration != generation {
		f, exists = database.CodexCapability{}, false
	}
	global := r.facts[codexFactKey(path, "")]
	if global.CredentialGeneration != 0 && global.CredentialGeneration != generation {
		global = database.CodexCapability{}
	}
	if global.Capability == database.CapabilityUnsupported || !exists {
		f = global
	}
	if f.Capability != "" {
		s.Capability, s.Source, s.Reason, s.ObservedAt = f.Capability, f.Source, f.Reason, f.ObservedAt
	}
	s.ExactModelSupported = strings.TrimSpace(model) != "" && codexFactKey(path, f.Model) == codexFactKey(path, model) && f.Capability == database.CapabilitySupported
	h := r.health[codexFactKey(path, model)]
	s.CooldownUntil = h.Until
	if h.Reason != "" {
		s.HealthReason = h.Reason
	}
	if h.Probing {
		s.Health = "recovering"
	} else if now.Before(h.Until) {
		s.Health = "cooldown"
	} else if !h.Until.IsZero() {
		s.Health = "probe_ready"
	}
	return s
}

func (a *Account) CodexPathSnapshot(path, model string, now time.Time) CodexPathSnapshot {
	generation := a.GetCredentialGeneration()
	a.codexRoutes.mu.Lock()
	defer a.codexRoutes.mu.Unlock()
	return a.codexPathSnapshotLocked(path, model, now, generation)
}

// Refresh is bounded by a local cache. Control-plane updates explicitly reload.
// A failed refresh fails closed; it never converts evidence to unsupported.
func (a *Account) RefreshCodexRoutes(ctx context.Context, now time.Time) error {
	a.codexRoutes.mu.Lock()
	defer a.codexRoutes.mu.Unlock()
	if now.Sub(a.codexRoutes.loadedAt) < 5*time.Second {
		return nil
	}
	return a.reloadCodexRoutesLocked(ctx, now)
}

func (a *Account) CodexPathViews(model string, now time.Time) []CodexPathSnapshot {
	generation := a.GetCredentialGeneration()
	a.codexRoutes.mu.Lock()
	defer a.codexRoutes.mu.Unlock()
	out := []CodexPathSnapshot{}
	for _, path := range []string{database.CodexPathNative, database.CodexPathBasispoints} {
		out = append(out, a.codexPathSnapshotLocked(path, model, now, generation))
		if model != "" {
			continue
		}
		models := []string{}
		seen := map[string]bool{}
		for _, f := range a.codexRoutes.facts {
			if f.Upstream == path && f.Model != "" {
				models = append(models, f.Model)
				seen[f.Model] = true
			}
		}
		for key := range a.codexRoutes.health {
			if m, ok := strings.CutPrefix(key, path+"|"); ok && m != "" && !seen[m] {
				models = append(models, m)
			}
		}
		sort.Strings(models)
		for _, m := range models {
			out = append(out, a.codexPathSnapshotLocked(path, m, now, generation))
		}
	}
	return out
}

// BeginCodexPath grants one recovery probe after expiry without a timer goroutine.
func (a *Account) BeginCodexPath(path, model string, now time.Time) (func(), bool) {
	generation := a.GetCredentialGeneration()
	r := &a.codexRoutes
	r.mu.Lock()
	s := a.codexPathSnapshotLocked(path, model, now, generation)
	if !s.Allowed || s.Capability == database.CapabilityUnsupported || s.Health == "cooldown" || s.Health == "recovering" {
		r.mu.Unlock()
		return nil, false
	}
	key := codexFactKey(path, model)
	h := r.health[key]
	probing := !h.Until.IsZero()
	if probing {
		h.Probing = true
		if r.health == nil {
			r.health = make(map[string]codexPathHealth)
		}
		r.health[key] = h
	}
	r.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			if !probing {
				return
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			current := r.health[key]
			if !current.Probing {
				return
			}
			current.Probing = false
			// An inconclusive probe must not immediately admit another wave.
			if !current.Until.After(now) {
				current.Until = now.Add(30 * time.Second)
			}
			r.health[key] = current
		})
	}, true
}

func (a *Account) SetCodexPathCooldown(path, model, reason string, started, until time.Time) {
	r := &a.codexRoutes
	r.mu.Lock()
	defer r.mu.Unlock()
	key := codexFactKey(path, model)
	if r.health[key].ObservedAt >= started.UnixNano() || r.facts[key].ObservedAt >= started.UnixNano() || r.facts[codexFactKey(path, "")].ObservedAt >= started.UnixNano() {
		return
	}
	if r.health == nil {
		r.health = make(map[string]codexPathHealth)
	}
	r.health[key] = codexPathHealth{Until: until, Reason: reason, ObservedAt: started.UnixNano(), Probing: r.health[key].Probing}
}

// A usage-policy rejection is temporary path health evidence, not proof of an
// exhausted account or missing entitlement. Concurrent failures share a window;
// only a failed recovery attempt increases the backoff, up to five minutes.
func (a *Account) NoteCodexPathUsageRejection(path, model string, started, now time.Time) {
	r := &a.codexRoutes
	r.mu.Lock()
	defer r.mu.Unlock()
	key := codexFactKey(path, model)
	previous := r.health[key]
	if previous.ObservedAt >= started.UnixNano() || r.facts[key].ObservedAt >= started.UnixNano() || r.facts[codexFactKey(path, "")].ObservedAt >= started.UnixNano() {
		return
	}
	if now.Before(previous.Until) || started.Before(previous.Until) {
		return
	}
	failures := 1
	if previous.Reason == "ambiguous_usage_rejection" {
		failures = min(previous.Rejections+1, 5)
	}
	delay := min(30*time.Second*time.Duration(1<<(failures-1)), 5*time.Minute)
	if r.health == nil {
		r.health = make(map[string]codexPathHealth)
	}
	r.health[key] = codexPathHealth{Until: now.Add(delay), Reason: "ambiguous_usage_rejection", ObservedAt: started.UnixNano(), Probing: previous.Probing, Rejections: failures}
}

func (a *Account) ObserveCodexPath(ctx context.Context, f database.CodexCapability) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if f.CredentialGeneration != 0 && f.CredentialGeneration != a.CredentialGeneration {
		return
	}
	f.Model = strings.ToLower(strings.TrimSpace(f.Model))
	r := &a.codexRoutes
	r.mu.Lock()
	defer r.mu.Unlock()
	key := codexFactKey(f.Upstream, f.Model)
	if r.facts[key].ObservedAt >= f.ObservedAt {
		return
	}
	reset := r.facts[codexFactKey(f.Upstream, "")]
	if reset.Source == "admin_reset" && reset.ObservedAt >= f.ObservedAt {
		return
	}
	if r.db != nil {
		applied, err := r.db.ObserveCodexCapability(ctx, a.ID(), f)
		if err != nil {
			log.Printf("[CodexRoute] evidence persistence failed account=%d", a.ID())
			return
		}
		if !applied {
			return
		}
	}
	if r.facts == nil {
		r.facts = make(map[string]database.CodexCapability)
	}
	r.facts[key] = f
	if f.Capability == database.CapabilitySupported && r.health[key].ObservedAt <= f.ObservedAt {
		delete(r.health, key)
	}
}

func (a *Account) ClearCodexPathHealth(path string) {
	a.codexRoutes.mu.Lock()
	defer a.codexRoutes.mu.Unlock()
	for key := range a.codexRoutes.health {
		if strings.HasPrefix(key, path+"|") {
			delete(a.codexRoutes.health, key)
		}
	}
}
