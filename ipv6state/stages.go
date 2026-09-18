package ipv6state

import (
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/statepool"
)

func (m *Manager) stage(entry Entry, now time.Time) (string, int) {
	if !m.config.StagedConcurrency {
		return "standard", m.config.Concurrency
	}
	if !entry.Valid {
		return "expired", m.config.ExpiredConcurrency
	}
	if entry.ExpiresAt <= now.Add(time.Duration(m.config.UrgentBeforeMinutes)*time.Minute).Unix() {
		return "urgent", m.config.UrgentConcurrency
	}
	return "early", m.config.EarlyConcurrency
}

// Budgets apply per account across selected models, sharing the existing global
// and account scheduler limits. No persisted account concurrency is modified.
func (m *Manager) updateBusinessBudgets() {
	for _, account := range m.store.Accounts() {
		limit := 0
		if m.config.Enabled && m.config.StagedConcurrency && NativeAccount(account) {
			identity, err := statepool.Snapshot(account, "")
			for _, model := range m.config.Models {
				if !m.selected(account, model) {
					continue
				}
				entry := m.observe(account, model, identity, err, m.now())
				if entry.CapturePhase == "account_unavailable" {
					continue
				}
				stage, _ := m.stage(entry, m.now())
				if stage == "urgent" || stage == "expired" {
					limit = m.config.UrgentBusinessConcurrency
					break
				}
			}
		}
		account.SetStateBusinessLimit(limit)
	}
}

func (m *Manager) captureBudget(account *auth.Account) (int, int) {
	limit, active := 0, 0
	identity, err := statepool.Snapshot(account, "")
	for _, model := range m.config.Models {
		entry := m.observe(account, model, identity, err, m.now())
		if entry.CapturePhase == "account_unavailable" || entry.CapturePhase == "out_of_scope" {
			continue
		}
		if entry.Valid && entry.ExpiresAt > m.now().Add(m.config.refreshBefore()).Unix() {
			continue
		}
		_, budget := m.stage(entry, m.now())
		limit = max(limit, budget)
	}
	for _, task := range m.active {
		if task.account == account {
			active++
		}
	}
	return limit, active
}
