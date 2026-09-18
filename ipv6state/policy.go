package ipv6state

import (
	"context"
	"slices"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/statepool"
)

// ModelStatus never contains a token, credential, or account identity hash.
type ModelStatus struct {
	InScope          bool   `json:"in_scope"`
	Available        bool   `json:"available"`
	CapturePhase     string `json:"capture_phase"`
	RetryAt          int64  `json:"retry_at"`
	UpdatedAt        int64  `json:"updated_at,omitempty"`
	Restriction      string `json:"restriction,omitempty"`
	RestrictionUntil int64  `json:"restriction_until,omitempty"`
	Model            string `json:"model"`
	Valid            bool   `json:"valid"`
	ExpiresAt        int64  `json:"expires_at"`
	Length           int    `json:"length"`
	Refreshing       bool   `json:"refreshing"`
}

func NativeAccount(account *auth.Account) bool {
	return account != nil && !account.IsRelayStyle() && !account.IsCodexAgentIdentity() && !account.IsAntigravityAPI() && !account.IsClaudeOAuth() && !account.IsGrokAPI()
}

func (m *Manager) SetRequireValidState(ctx context.Context, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	config := m.config
	config.RequireValidState = enabled
	if err := m.db.SaveIPv6StateConfig(ctx, encode(config)); err != nil {
		return err
	}
	m.config = config
	return nil
}

func (m *Manager) AccountModels(account *auth.Account) []ModelStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.accountModels(account)
}

// Caller holds m.mu. Saved validity is independent of the automation switch.
func (m *Manager) accountModels(account *auth.Account) []ModelStatus {
	return m.accountModelsAt(account, m.now())
}

func (m *Manager) accountModelsAt(account *auth.Account, now time.Time) []ModelStatus {
	if !NativeAccount(account) {
		return nil
	}
	identity, err := statepool.Snapshot(account, "")
	result := make([]ModelStatus, 0, len(m.config.Models))
	for _, model := range m.config.Models {
		entry := m.observe(account, model, identity, err, now)
		inScope := m.selected(account, model)
		ok := inScope && entry.Valid
		item := ModelStatus{Model: model, InScope: inScope, Valid: ok, Available: ok && entry.Available,
			Refreshing: ok && entry.Refreshing, CapturePhase: entry.CapturePhase, RetryAt: entry.RetryAt,
			UpdatedAt: entry.UpdatedAt, Restriction: entry.CooldownReason, RestrictionUntil: entry.CooldownUntil}
		if ok {
			item.ExpiresAt, item.Length = entry.ExpiresAt, len(entry.Value)
		}
		result = append(result, item)
	}
	return result
}

type ModelCoverage struct {
	Model             string `json:"model"`
	ReuseAccounts     int    `json:"reuse_accounts"`
	AvailableAccounts int    `json:"available_accounts"`
}

// Summary counts identities and exact models, never in-flight requests.
type Summary struct {
	Enabled           bool            `json:"enabled"`
	RequireValidState bool            `json:"require_valid_state"`
	TotalAccounts     int             `json:"total_accounts"`
	ReuseAccounts     int             `json:"reuse_accounts"`
	AvailableAccounts int             `json:"available_accounts"`
	ValidCombinations int             `json:"valid_combinations"`
	CoveredModels     int             `json:"covered_models"`
	Models            []ModelCoverage `json:"models"`
	Revision          string          `json:"revision"`
	NextExpiry        int64           `json:"next_expiry"`
}

type Snapshot struct {
	Summary  Summary                 `json:"summary"`
	Accounts map[int64][]ModelStatus `json:"accounts"`
}

func (m *Manager) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.snapshot(m.now())
}

// Caller holds m.mu. All admin surfaces and filters consume this same projection.
func (m *Manager) snapshot(now time.Time) Snapshot {
	result := Snapshot{Summary: Summary{Enabled: m.config.Enabled, RequireValidState: m.config.RequireValidState,
		Models: make([]ModelCoverage, len(m.config.Models))}, Accounts: map[int64][]ModelStatus{}}
	for i, model := range m.config.Models {
		result.Summary.Models[i].Model = model
	}
	// Exclude transient capture phases from the filter revision. Polling must not
	// reload a whole page simply because a worker started or a slot was released.
	filterRows := map[int64][]ModelStatus{}
	for _, account := range m.store.Accounts() {
		if !NativeAccount(account) {
			continue
		}
		result.Summary.TotalAccounts++
		models := m.accountModelsAt(account, now)
		result.Accounts[account.ID()] = models
		filterModels := slices.Clone(models)
		reuse, available := false, false
		for i, item := range models {
			filterModels[i] = ModelStatus{Model: item.Model, InScope: item.InScope, Valid: item.Valid, Available: item.Available, ExpiresAt: item.ExpiresAt, Length: item.Length, Restriction: item.Restriction, RestrictionUntil: item.RestrictionUntil}
			if !item.Valid {
				continue
			}
			reuse = true
			result.Summary.ValidCombinations++
			result.Summary.Models[i].ReuseAccounts++
			if result.Summary.NextExpiry == 0 || item.ExpiresAt < result.Summary.NextExpiry {
				result.Summary.NextExpiry = item.ExpiresAt
			}
			if item.Available {
				available = true
				result.Summary.Models[i].AvailableAccounts++
			}
		}
		filterRows[account.ID()] = filterModels
		if reuse {
			result.Summary.ReuseAccounts++
		}
		if available {
			result.Summary.AvailableAccounts++
		}
	}
	for _, item := range result.Summary.Models {
		if item.AvailableAccounts > 0 {
			result.Summary.CoveredModels++
		}
	}
	result.Summary.Revision = statepool.Hash(encode(struct {
		Config   Config
		Accounts map[int64][]ModelStatus
	}{m.config, filterRows}))
	return result
}

// Take the snapshot before entering the account scheduler to avoid lock inversion.
// Resolve checks again immediately before dispatch, including expiry and identity.
func (m *Manager) EligibleAccounts(model string) (bool, map[int64]bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.config.RequireValidState {
		return false, nil
	}
	allowed := map[int64]bool{}
	if !m.config.Enabled {
		return true, allowed
	}
	for _, account := range m.store.Accounts() {
		if !m.selected(account, model) {
			continue
		}
		identity, err := statepool.Snapshot(account, "")
		if err == nil && valid(m.entries[key(account.ID(), model)], identity, m.now(), m.config) {
			allowed[account.ID()] = true
		}
	}
	return true, allowed
}
