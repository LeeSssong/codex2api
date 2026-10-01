package statepool

import (
	"github.com/codex2api/auth"
)

// ClaimsInjectedState recognizes a previously resolved value without resolving
// its business proxy again. Resolve already checked the actual dispatch route.
func (m *Manager) ClaimsInjectedState(account *auth.Account, model, effort, value string) bool {
	if account == nil || value == "" {
		return false
	}
	m.mu.RLock()
	entry := m.entries[key(account.ID(), model, effort)]
	m.mu.RUnlock()
	identity, err := Snapshot(account, "")
	identity.ProxyHash = entry.Identity.ProxyHash
	return err == nil && identity == entry.Identity && entry.Enabled && entry.ExpiresAt > m.now().Unix() && entry.Value == value
}

func (m *Manager) reusableEntry(job Job) (Entry, bool) {
	m.mu.RLock()
	entry := m.entries[key(job.AccountID, job.Model, job.Effort)]
	m.mu.RUnlock()
	if entry.Identity == job.Identity || !entry.Identity.SameAccount(job.Identity) {
		return Entry{}, false
	}
	err := ValidatePortable(PortableState{MemberHash: entry.Identity.MemberHash, WorkspaceHash: entry.Identity.WorkspaceHash,
		Model: entry.Model, Effort: entry.Effort, Value: entry.Value, Fingerprint: entry.Fingerprint,
		CapturedAt: entry.CapturedAt, ExpiresAt: entry.ExpiresAt}, m.now())
	return entry, err == nil
}
