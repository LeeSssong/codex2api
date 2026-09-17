package statepool

import (
	"errors"
)

type ImportPreviewItem struct {
	Index       int    `json:"index"`
	Model       string `json:"model"`
	AccountID   int64  `json:"account_id,omitempty"`
	AccountName string `json:"account_name,omitempty"`
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
	ExpiresAt   int64  `json:"expires_at"`
	ExistingID  string `json:"-"`
}

func (m *Manager) PreviewImport(pack Package) ([]ImportPreviewItem, error) {
	if pack.Format != "codex2api-state" || pack.Version != 1 || len(pack.States) == 0 || len(pack.States) > 128 {
		return nil, errors.New("unsupported or empty state package")
	}
	items := make([]ImportPreviewItem, 0, len(pack.States))
	seen := map[string]bool{}
	for index, state := range pack.States {
		item := ImportPreviewItem{Index: index, Model: state.Model, Status: "invalid", ExpiresAt: state.ExpiresAt}
		if err := ValidatePortable(state, m.now()); err != nil {
			item.Reason = err.Error()
			items = append(items, item)
			continue
		}
		var matches int
		var identity Identity
		for _, account := range m.store.Accounts() {
			current, err := Snapshot(account, m.store.ResolveProxyForAccount(account))
			if err != nil || current.MemberHash != state.MemberHash || current.WorkspaceHash != state.WorkspaceHash {
				continue
			}
			matches++
			identity = current
			item.AccountID = account.ID()
			account.Mu().RLock()
			item.AccountName = account.Email
			account.Mu().RUnlock()
		}
		if matches != 1 {
			item.Reason = "matching_account_missing_or_ambiguous"
		} else {
			binding := key(item.AccountID, state.Model, state.Effort)
			if seen[binding] {
				item.Reason = "duplicate_account_model_in_package"
			} else {
				seen[binding] = true
				item.Status = "ready_to_verify"
				m.mu.RLock()
				existing, found := m.entries[binding]
				m.mu.RUnlock()
				if found && existing.Identity == identity && existing.Value == state.Value && existing.ExpiresAt > m.now().Unix() {
					item.Status, item.ExistingID, item.ExpiresAt = "already_verified", existing.ID, existing.ExpiresAt
				}
			}
		}
		items = append(items, item)
	}
	return items, nil
}
