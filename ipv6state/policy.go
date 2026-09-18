package ipv6state

import (
	"context"
	"github.com/codex2api/auth"
	"github.com/codex2api/statepool"
)

// ModelStatus never contains a token, credential, or account identity hash.
type ModelStatus struct {
	Model      string `json:"model"`
	Valid      bool   `json:"valid"`
	ExpiresAt  int64  `json:"expires_at"`
	Length     int    `json:"length"`
	Refreshing bool   `json:"refreshing"`
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
	if !NativeAccount(account) {
		return nil
	}
	identity, err := statepool.Snapshot(account, "")
	result := make([]ModelStatus, 0, len(statepool.Models))
	for _, model := range statepool.Models {
		entry := m.entries[key(account.ID(), model)]
		ok := err == nil && valid(entry, identity, m.now(), m.config)
		item := ModelStatus{Model: model, Valid: ok, Refreshing: m.collecting(account.ID(), model)}
		if ok {
			item.ExpiresAt, item.Length = entry.ExpiresAt, len(entry.Value)
		}
		result = append(result, item)
	}
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
