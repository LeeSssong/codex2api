package admin

import (
	"fmt"
	"slices"

	"github.com/codex2api/auth"
	"github.com/codex2api/ipv6state"
	"github.com/codex2api/statepool"
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
	return statePredicate(h.stateSnapshot(), status, model)
}

func (h *Handler) stateSnapshot() ipv6state.Snapshot {
	if h.ipv6State == nil {
		return ipv6state.Snapshot{Summary: ipv6state.Summary{Models: []ipv6state.ModelCoverage{}}}
	}
	return h.ipv6State.Snapshot()
}

func statePredicate(snapshot ipv6state.Snapshot, status, model string) (func(int64) bool, error) {
	if status != "" && status != "all" && status != "valid" && status != "available" && status != "missing" {
		return nil, fmt.Errorf("unsupported state filter")
	}
	if model != "" && !slices.Contains(statepool.Models, model) {
		return nil, fmt.Errorf("unsupported state model")
	}
	if !slices.ContainsFunc(snapshot.Summary.Models, func(item ipv6state.ModelCoverage) bool { return item.Model == model }) {
		model = ""
	}
	if status == "" || status == "all" {
		return func(int64) bool { return true }, nil
	}
	matched := map[int64]bool{}
	for id, models := range snapshot.Accounts {
		for _, item := range models {
			if item.Valid && (status != "available" || item.Available) && (model == "" || item.Model == model) {
				matched[id] = true
			}
		}
	}
	return func(id int64) bool { return matched[id] == (status == "valid" || status == "available") }, nil
}
