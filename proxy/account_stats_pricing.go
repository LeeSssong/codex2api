package proxy

import "github.com/codex2api/database"

// snapshotAccountStatsPricing is called after channel attribution and before the
// common DB sink. Config is account local and cannot affect customer counters.
func (h *Handler) snapshotAccountStatsPricing(input *database.UsageLogInput) *database.UsageLogInput {
	if input == nil || input.AccountCost != nil {
		return input
	}
	cfg := database.AccountStatsPricingConfig{}
	if h != nil && h.store != nil && input.AccountID > 0 {
		if acc := h.store.FindByID(input.AccountID); acc != nil {
			// Source equivalence is scoped to OpenAI/Codex upstreams.
			if !acc.IsGrokAPI() && !acc.IsAntigravityAPI() && !acc.IsClaudeOAuth() {
				cfg = acc.AccountStatsPricing()
			}
		}
	}
	if input.Channel != "" && input.Channel != database.UpstreamChannelCodex {
		// Other native channels retain their existing fee semantics.
		amount := database.UsageLogBilledCost(input)
		copy := *input
		copy.AccountCost = &amount
		copy.StandardCost = &amount
		copy.AccountCostSource = "native_non_codex"
		return &copy
	}
	return database.SnapshotAccountStatsCost(input, cfg)
}
