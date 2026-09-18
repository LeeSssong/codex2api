package ipv6state

import (
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/statepool"
)

// observe separates saved validity, current scheduling gates and capture work.
// Historical HTTP failures never replace the account's current restriction.
func (m *Manager) observe(account *auth.Account, model string, identity statepool.Identity, identityErr error, now time.Time) Entry {
	entry := m.entries[key(account.ID(), model)]
	if identityErr != nil || entry.Identity != identity || entry.AccountID != account.ID() || entry.Model != model {
		entry = Entry{AccountID: account.ID(), Model: model, Identity: identity}
	}
	account.Mu().RLock()
	entry.AccountName = account.Email
	account.Mu().RUnlock()
	inScope := m.selected(account, model)
	entry.Valid = inScope && identityErr == nil && valid(entry, identity, now, m.config)
	available, reason, until := account.StateAvailability(model, now)
	entry.Available = entry.Valid && available
	entry.CooldownReason, entry.CooldownUntil = reason, 0
	if !until.IsZero() {
		entry.CooldownUntil = until.Unix()
	}
	switch {
	case !inScope:
		entry.CapturePhase = "out_of_scope"
	case !available:
		entry.CapturePhase = "account_unavailable"
	case identityErr != nil:
		entry.CapturePhase, entry.CooldownReason = "account_unavailable", "identity_unavailable"
	case !m.config.Enabled:
		entry.CapturePhase = "paused"
	case m.collecting(account.ID(), model):
		entry.CapturePhase = "collecting"
	case entry.RetryAt > now.Unix():
		entry.CapturePhase = "retrying"
	case entry.Valid && entry.ExpiresAt > now.Add(m.config.refreshBefore()).Unix():
		entry.CapturePhase = "idle"
	default:
		entry.CapturePhase = "waiting"
	}
	entry.Refreshing = entry.Valid && entry.CapturePhase == "collecting"
	entry.CaptureStage, _ = m.stage(entry, now)
	// Keep the legacy status for older clients. New clients read both dimensions.
	if entry.Valid {
		entry.Status = "ready"
	} else if entry.CapturePhase == "retrying" && entry.Status == "account_unavailable" {
		// An upstream header rejection also carries an account-wide retry gate.
	} else {
		entry.Status = entry.CapturePhase
	}
	return entry
}
