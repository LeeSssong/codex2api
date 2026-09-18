package admin

import (
	"fmt"
	"github.com/codex2api/auth"
	"github.com/codex2api/ipv6state"
	"github.com/codex2api/statepool"
	"slices"
)

type accountStateModel = ipv6state.ModelStatus

func (h *Handler) accountStateModels(account *auth.Account) []accountStateModel {
	if h.ipv6State == nil {
		return nil
	}
	return h.ipv6State.AccountModels(account)
}

// Evaluate current validity before pagination and bulk selection, not from the
// cached account-list snapshot. A state can expire while that snapshot is valid.
func (h *Handler) accountStatePredicate(status, model string) (func(int64) bool, error) {
	if status != "" && status != "all" && status != "valid" && status != "missing" {
		return nil, fmt.Errorf("unsupported state filter")
	}
	if model != "" && !slices.Contains(statepool.Models, model) {
		return nil, fmt.Errorf("unsupported state model")
	}
	if status == "" || status == "all" {
		return func(int64) bool { return true }, nil
	}
	matched := map[int64]bool{}
	if h.ipv6State != nil {
		for _, account := range h.store.Accounts() {
			for _, item := range h.accountStateModels(account) {
				if item.Valid && (model == "" || item.Model == model) {
					matched[account.ID()] = true
				}
			}
		}
	}
	return func(id int64) bool { return matched[id] == (status == "valid") }, nil
}
