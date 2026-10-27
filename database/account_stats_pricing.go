package database

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// AccountCostSnapshot is independent of native customer billing. Nil identifies
// pre-migration rows; a pointer to zero is a real, immutable cost.
type AccountCostSnapshot struct {
	QuotaUsageAdmitted     *bool    `json:"quota_usage_admitted"`
	QuotaTokens            *int64   `json:"quota_tokens"`
	AccountCost            *float64 `json:"account_cost"`
	AccountStatsCost       *float64 `json:"account_stats_cost"`
	StandardCost           *float64 `json:"standard_cost"`
	AccountRateMultiplier  *float64 `json:"account_rate_multiplier"`
	AccountCostSource      string   `json:"account_cost_source"`
	AccountPricingSnapshot string   `json:"account_pricing_snapshot,omitempty"`
}

// AccountUsageEvidence contains the observed original upstream categories.
// InputTokens includes cache reads/writes (native protocol convention); OutputTokens
// follows the original upstream total; source custom rules also price image output separately.
// Diagnostic estimates stay on UsageLogInput and never enter this evidence.
type AccountUsageEvidence struct {
	UsageKnown             *bool
	ServiceTier            *string
	EffectiveModel         string
	InputTokens            int
	OutputTokens           int
	CachedTokens           int
	CacheWrite5mTokens     int
	CacheWrite1hTokens     int
	ImageInputTokens       int
	ImageOutputTokens      int
	CachedImageInputTokens int
}

type AccountStatsPricingConfig struct {
	SourceGroupID    *int64                    `json:"source_group_id,omitempty"`
	RateMultiplier   *float64                  `json:"account_rate_multiplier,omitempty"`
	ApplySalePricing bool                      `json:"apply_sale_pricing"`
	Rules            []AccountStatsPricingRule `json:"rules,omitempty"`
	// UpstreamPrices represents the operator's source model catalog. Absent entries
	// use the pinned Sub2API fallback table, including model-specific ladders.
	UpstreamPrices     []AccountStatsPricingRule `json:"upstream_prices,omitempty"`
	LongContextEnabled *bool                     `json:"long_context_enabled,omitempty"`
}

type AccountStatsPrices struct {
	Input          *float64 `json:"input,omitempty"` // USD / million tokens
	Output         *float64 `json:"output,omitempty"`
	CacheRead      *float64 `json:"cache_read,omitempty"`
	CacheWrite     *float64 `json:"cache_write,omitempty"`
	CacheWrite1h   *float64 `json:"cache_write_1h,omitempty"`
	ImageOutput    *float64 `json:"image_output,omitempty"`
	ImageInput     *float64 `json:"image_input,omitempty"`
	ImageCacheRead *float64 `json:"image_cache_read,omitempty"`
}
type AccountStatsPricingInterval struct {
	MinTokens int  `json:"min_tokens"`
	MaxTokens *int `json:"max_tokens,omitempty"`
	AccountStatsPrices
}

// Rules are evaluated in order. Pricing entries within the selected rule use
// exact model matches before wildcard matches. Empty selectors are a native
// per-account shorthand for AccountIDs=[the account carrying this config].
type AccountStatsPricingRule struct {
	AccountIDs []int64                   `json:"account_ids,omitempty"`
	GroupIDs   []int64                   `json:"group_ids,omitempty"`
	Pricing    []AccountStatsPricingRule `json:"pricing,omitempty"`

	Models []string `json:"models"`
	AccountStatsPrices
	PerRequest           *float64                      `json:"per_request,omitempty"`
	ImageTiers           map[string]float64            `json:"image_tiers,omitempty"`
	Intervals            []AccountStatsPricingInterval `json:"intervals,omitempty"`
	ReasoningMultipliers map[string]float64            `json:"reasoning_multipliers,omitempty"`
	LongContextThreshold int                           `json:"long_context_threshold,omitempty"`
	LongInputMultiplier  float64                       `json:"long_input_multiplier,omitempty"`
	LongOutputMultiplier float64                       `json:"long_output_multiplier,omitempty"`
}

func validStatsNumber(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 }
func ValidateAccountStatsPricing(c AccountStatsPricingConfig) error {
	if c.SourceGroupID != nil && *c.SourceGroupID <= 0 {
		return fmt.Errorf("source_group_id must be positive")
	}
	if c.RateMultiplier != nil && !validStatsNumber(*c.RateMultiplier) {
		return fmt.Errorf("account_rate_multiplier must be finite and non-negative")
	}
	all := append([]AccountStatsPricingRule(nil), c.UpstreamPrices...)
	for _, outer := range c.Rules {
		for _, id := range append(append([]int64(nil), outer.AccountIDs...), outer.GroupIDs...) {
			if id <= 0 {
				return fmt.Errorf("account and group selectors must be positive")
			}
		}
		if len(outer.Pricing) > 0 {
			if len(outer.Models) > 0 {
				return fmt.Errorf("outer rule must use pricing or flat model fields, not both")
			}
			for _, price := range outer.Pricing {
				if len(price.Pricing) > 0 {
					return fmt.Errorf("nested pricing rules are not supported")
				}
			}
			all = append(all, outer.Pricing...)
		} else {
			all = append(all, outer)
		}
	}
	for _, rules := range [][]AccountStatsPricingRule{all} {
		for _, r := range rules {
			if len(r.Models) == 0 {
				return fmt.Errorf("pricing rule requires models")
			}
			for _, m := range r.Models {
				if strings.TrimSpace(m) == "" || strings.Contains(strings.TrimSuffix(m, "*"), "*") {
					return fmt.Errorf("models allow exact names or a trailing wildcard")
				}
			}
			prices := []AccountStatsPrices{r.AccountStatsPrices}
			for _, iv := range r.Intervals {
				if iv.MinTokens < 0 || (iv.MaxTokens != nil && *iv.MaxTokens <= iv.MinTokens) {
					return fmt.Errorf("invalid pricing interval")
				}
				prices = append(prices, iv.AccountStatsPrices)
			}
			for _, p := range prices {
				for _, v := range []*float64{p.Input, p.Output, p.CacheRead, p.CacheWrite, p.CacheWrite1h, p.ImageOutput, p.ImageInput, p.ImageCacheRead} {
					if v != nil && !validStatsNumber(*v) {
						return fmt.Errorf("prices must be finite and non-negative")
					}
				}
			}
			if r.PerRequest != nil && !validStatsNumber(*r.PerRequest) {
				return fmt.Errorf("per_request must be finite and non-negative")
			}
			for tier, v := range r.ImageTiers {
				if _, ok := statsImageTier(tier); !ok || !validStatsNumber(v) {
					return fmt.Errorf("invalid image tier price")
				}
			}
			for _, v := range r.ReasoningMultipliers {
				if !validStatsNumber(v) {
					return fmt.Errorf("invalid reasoning multiplier")
				}
			}
			if r.LongContextThreshold < 0 || !validStatsNumber(r.LongInputMultiplier) || !validStatsNumber(r.LongOutputMultiplier) {
				return fmt.Errorf("invalid long context policy")
			}
		}
	}
	return nil
}

// AccountCostSQL uses only persisted values. account_billed is the explicitly
// approximate legacy fallback; it never recalculates history at today's prices.
func AccountCostSQL(alias string) string {
	p := ""
	if alias != "" {
		p = alias + "."
	}
	return "COALESCE(" + p + "account_cost, COALESCE(" + p + "account_stats_cost, " + p + "standard_cost, " + p + "account_billed) * COALESCE(" + p + "account_rate_multiplier, 1))"
}
func StandardCostSQL(alias string) string {
	p := ""
	if alias != "" {
		p = alias + "."
	}
	return "COALESCE(" + p + "standard_cost, " + p + "account_billed)"
}
func statsValue(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}
func statsPointer(v float64) *float64    { return &v }
func roundAccountCost(v float64) float64 { return math.Round(v*1e10) / 1e10 }

// Source token buckets are exclusive; the native InputTokens includes cache.
func statsSourceTotalInput(input *UsageLogInput) int {
	return max(0, input.InputTokens-input.CachedTokens-input.CacheWrite5mTokens-input.CacheWrite1hTokens) + max(0, input.CachedTokens) + max(0, input.CacheWrite5mTokens) + max(0, input.CacheWrite1hTokens)
}

func observedAccountStandardCost(input *UsageLogInput) float64 {
	if s := input.billingSnapshot; s != nil && s.UserBillingMode == UserBillingModePerImage {
		return s.ImageUnitPrice * float64(max(0, input.ImageCount))
	}
	normalized := *input
	normalized.InputTokens = statsSourceTotalInput(input)
	return UsageLogCostBreakdown(&normalized).TotalCost
}

func statsTokenCost(input *UsageLogInput, r AccountStatsPricingRule) float64 {
	p := r.AccountStatsPrices
	total := statsSourceTotalInput(input) + input.OutputTokens
	for _, iv := range r.Intervals {
		if total > iv.MinTokens && (iv.MaxTokens == nil || total <= *iv.MaxTokens) {
			p = iv.AccountStatsPrices
			break
		}
	}
	ordinary := max(0, input.InputTokens-input.CachedTokens-input.CacheWrite5mTokens-input.CacheWrite1hTokens)
	write1h := p.CacheWrite1h
	if write1h == nil {
		write1h = p.CacheWrite
	}
	return (float64(ordinary)*statsValue(p.Input) + float64(input.OutputTokens)*statsValue(p.Output) + float64(input.CachedTokens)*statsValue(p.CacheRead) + float64(input.CacheWrite5mTokens)*statsValue(p.CacheWrite) + float64(input.CacheWrite1hTokens)*statsValue(write1h) + float64(input.ImageOutputTokens)*statsValue(p.ImageOutput)) / 1e6
}
func statsMatchingRule(rules []AccountStatsPricingRule, model string) *AccountStatsPricingRule {
	model = strings.ToLower(model)
	for _, wild := range []bool{false, true} {
		for i := range rules {
			for _, m := range rules[i].Models {
				m = strings.ToLower(m)
				isWild := strings.HasSuffix(m, "*")
				if isWild != wild {
					continue
				}
				if (!wild && m == model) || (wild && strings.HasPrefix(model, strings.TrimSuffix(m, "*"))) {
					return &rules[i]
				}
			}
		}
	}
	return nil
}
func statsCustomPricing(rules []AccountStatsPricingRule, input *UsageLogInput, model string, sourceGroupID *int64) *AccountStatsPricingRule {
	for i := range rules {
		r := &rules[i]
		match := len(r.AccountIDs) == 0 && len(r.GroupIDs) == 0
		for _, id := range r.AccountIDs {
			match = match || id == input.AccountID
		}
		for _, id := range r.GroupIDs {
			if sourceGroupID != nil {
				match = match || id == *sourceGroupID
			}
		}
		if !match {
			continue
		}
		prices := r.Pricing
		if len(prices) == 0 {
			prices = []AccountStatsPricingRule{*r}
		}
		if selected := statsMatchingRule(prices, model); selected != nil {
			return selected
		}
	}
	return nil
}

func statsImageTier(size string) (string, bool) {
	s := strings.ToLower(strings.TrimSpace(size))
	if s == "1k" || s == "2k" || s == "4k" {
		return strings.ToUpper(s), true
	}
	parts := strings.Split(s, "x")
	if len(parts) != 2 {
		return "", false
	}
	w, e := strconv.Atoi(strings.TrimSpace(parts[0]))
	h, f := strconv.Atoi(strings.TrimSpace(parts[1]))
	if e != nil || f != nil || w <= 0 || h <= 0 {
		return "", false
	}
	if max(w, h) <= 1024 {
		return "1K", true
	}
	if max(w, h) <= 2048 {
		return "2K", true
	}
	return "4K", true
}
func statsReasoningMultiplier(r *AccountStatsPricingRule, effort string) float64 {
	if r != nil {
		if v, ok := r.ReasoningMultipliers[strings.ToLower(strings.TrimSpace(effort))]; ok && v > 0 {
			return v
		}
	}
	return 1
}

// SnapshotAccountStatsCost freezes the source pricing chain before buffering.
// Caller billing and its token categories are left intact.
func SnapshotAccountStatsCost(input *UsageLogInput, cfg AccountStatsPricingConfig) *UsageLogInput {
	if input == nil || input.AccountCost != nil {
		return input
	}
	copy := *input
	standard := observedAccountStandardCost(input)
	saleBreakdown := UsageLogCostBreakdown(input)
	if input.AccountStatsOnly {
		saleBreakdown = CostBreakdown{}
	}
	// Only account statistics consume observed upstream usage. Native customer
	// billing continues to use the original log and its own admission policy.
	if evidence := input.AccountUsage; evidence != nil {
		original := *input
		if evidence.ServiceTier != nil {
			original.BillingServiceTier = *evidence.ServiceTier
			original.ActualServiceTier = *evidence.ServiceTier
			original.ServiceTier = *evidence.ServiceTier
		}
		if evidence.EffectiveModel != "" {
			original.EffectiveModel = evidence.EffectiveModel
		}
		original.InputTokens = evidence.InputTokens
		original.OutputTokens = evidence.OutputTokens
		original.CachedTokens = evidence.CachedTokens
		original.CacheWrite5mTokens = evidence.CacheWrite5mTokens
		original.CacheWrite1hTokens = evidence.CacheWrite1hTokens
		original.ImageInputTokens = evidence.ImageInputTokens
		original.ImageOutputTokens = evidence.ImageOutputTokens
		original.CachedImageInputTokens = evidence.CachedImageInputTokens
		input = &original
	}
	// Source standard/pre-user-multiplier cost uses observed original buckets.
	// Native account_billed/user_billed and display breakdown remain separate.
	if input.AccountUsage != nil {
		standard = observedAccountStandardCost(input)
	}

	multiplier := 1.0
	if cfg.RateMultiplier != nil && validStatsNumber(*cfg.RateMultiplier) {
		multiplier = *cfg.RateMultiplier
	}
	model := input.EffectiveModel
	if model == "" {
		model = input.Model
	}
	tier := usageLogBillingServiceTier(input)
	source := "fallback"
	cost := standard
	applyRate := true
	var stats *float64
	var selected *AccountStatsPricingRule
	if r := statsCustomPricing(cfg.Rules, input, model, cfg.SourceGroupID); r != nil {
		selected = r
		if len(r.ImageTiers) > 0 && SupportsImageBilling(model) && input.ImageCount > 0 {
			if imageTier, ok := statsImageTier(input.ImageSize); ok {
				for k, v := range r.ImageTiers {
					if strings.EqualFold(k, imageTier) {
						cost = v * float64(input.ImageCount) * statsReasoningMultiplier(r, input.ReasoningEffort)
						stats = statsPointer(cost)
						source = "custom_image_tier"
						applyRate = false
						break
					}
				}
			}
		} else if r.PerRequest != nil && *r.PerRequest > 0 {
			cost = *r.PerRequest * float64(max(1, input.ImageCount)) * statsReasoningMultiplier(r, input.ReasoningEffort)
			stats = statsPointer(cost)
			source = "custom_request"
		} else if len(r.ImageTiers) == 0 {
			v := statsTokenCost(input, *r) * statsReasoningMultiplier(r, input.ReasoningEffort)
			if v > 0 {
				cost = v
				stats = statsPointer(v)
				source = "custom_tokens"
			}
		}
	}
	if stats == nil && cfg.ApplySalePricing {
		source = "sale"
		if standard > 0 {
			stats = statsPointer(standard)
		}
	}
	if stats == nil && !cfg.ApplySalePricing {
		r := statsMatchingRule(cfg.UpstreamPrices, model)
		if r == nil {
			r = sourceFallbackStatsPricing(model)
		}
		if r != nil {
			selected = r
			adjusted := *r
			longEnabled := cfg.LongContextEnabled == nil || *cfg.LongContextEnabled
			// Source explicitly exempts GPT-6.1 Sol from the model long-context ladder.
			if longEnabled && sourceStatsModelKey(model) != "gpt-6.1-sol" && r.LongContextThreshold > 0 && statsSourceTotalInput(input) > r.LongContextThreshold {
				im, om := r.LongInputMultiplier, r.LongOutputMultiplier
				if im <= 0 {
					im = 1
				}
				if om <= 0 {
					om = 1
				}
				adjusted.Input = statsPointer(statsValue(r.Input) * im)
				adjusted.CacheRead = statsPointer(statsValue(r.CacheRead) * im)
				adjusted.CacheWrite = statsPointer(statsValue(r.CacheWrite) * im)
				if r.CacheWrite1h != nil {
					adjusted.CacheWrite1h = statsPointer(*r.CacheWrite1h * im)
				}
				adjusted.Output = statsPointer(statsValue(r.Output) * om)
			}
			v := sourceModelTokenCost(input, adjusted, model, tier) * statsReasoningMultiplier(r, input.ReasoningEffort)
			if v > 0 {
				cost = v
				stats = statsPointer(v)
				source = "upstream_default"
			}
		}
	}
	if applyRate {
		cost *= multiplier
	}
	if input.AccountUsage != nil && input.AccountUsage.UsageKnown != nil && !*input.AccountUsage.UsageKnown {
		// Native diagnostic output estimates are not observed billable usage.
		cost = 0
		standard = 0
		stats = nil
		source = "usage_unknown"
	}

	cost = roundAccountCost(cost)
	if stats != nil {
		stats = statsPointer(roundAccountCost(*stats))
	}
	copy.AccountCostSnapshot = AccountCostSnapshot{QuotaUsageAdmitted: input.QuotaUsageAdmitted, QuotaTokens: input.QuotaTokens, AccountCost: &cost, AccountStatsCost: stats, StandardCost: statsPointer(standard), AccountRateMultiplier: statsPointer(multiplier), AccountCostSource: source}
	frozen := map[string]any{
		"version": 1, "usage_known": func() any {
			if input.AccountUsage != nil {
				return input.AccountUsage.UsageKnown
			}
			return nil
		}(), "model": model, "input_tokens": input.InputTokens, "ordinary_input_tokens": max(0, input.InputTokens-input.CachedTokens-input.CacheWrite5mTokens-input.CacheWrite1hTokens), "cached_tokens": input.CachedTokens, "output_tokens": input.OutputTokens,
		"cache_write_5m_tokens": input.CacheWrite5mTokens, "cache_write_1h_tokens": input.CacheWrite1hTokens, "image_input_tokens": input.ImageInputTokens, "image_output_tokens": input.ImageOutputTokens, "cached_image_input_tokens": input.CachedImageInputTokens,
		"image_count": input.ImageCount, "image_size": input.ImageSize, "service_tier": tier, "reasoning_effort": input.ReasoningEffort, "price": selected, "config": AccountStatsPricingConfig{SourceGroupID: cfg.SourceGroupID, RateMultiplier: cfg.RateMultiplier, ApplySalePricing: cfg.ApplySalePricing, LongContextEnabled: cfg.LongContextEnabled}, "sale_breakdown": saleBreakdown,
	}

	raw, _ := json.Marshal(frozen)
	copy.AccountPricingSnapshot = string(raw)
	return &copy
}

// Model catalog pricing splits image output from total output. Custom account
// rules intentionally retain the source's separate additive image-output term.
func sourceModelTokenCost(input *UsageLogInput, r AccountStatsPricingRule, model, tier string) float64 {
	multiplier := sourceStatsTierMultiplier(model, tier)
	key := sourceStatsModelKey(model)
	priority := (tier == "priority" || tier == "fast") && key != "" && key != "gpt-5.4-mini" && key != "gpt-5.4-nano" && key != "gpt-5.5-pro"
	if priority {
		for _, field := range []**float64{&r.Input, &r.Output, &r.CacheRead, &r.CacheWrite, &r.CacheWrite1h} {
			if *field != nil {
				*field = statsPointer(**field * multiplier)
			}
		}
		multiplier = 1
	}
	cost := statsTokenCost(input, r)
	ordinary := max(0, input.InputTokens-input.CachedTokens-input.CacheWrite5mTokens-input.CacheWrite1hTokens)
	imageInput := min(max(0, input.ImageInputTokens), ordinary)
	if r.ImageInput != nil && *r.ImageInput != 0 {
		cost += float64(imageInput) * (*r.ImageInput - statsValue(r.Input)) / 1e6
	}
	imageCached := min(max(0, input.CachedImageInputTokens), max(0, input.CachedTokens))
	if r.ImageCacheRead != nil && *r.ImageCacheRead > 0 {
		cost += float64(imageCached) * (*r.ImageCacheRead - statsValue(r.CacheRead)) / 1e6
	}
	if r.ImageOutput != nil {
		cost -= float64(min(max(0, input.ImageOutputTokens), max(0, input.OutputTokens))) * statsValue(r.Output) / 1e6
	}
	return cost * multiplier
}

func sourceStatsTierMultiplier(model, tier string) float64 {
	switch normalizeServiceTier(tier) {
	case "flex":
		return .5
	case "priority", "fast":
		if sourceStatsModelKey(model) == "gpt-5.5" {
			return 2.5
		}
		return 2
	case "ultrafast":
		if sourceStatsModelKey(model) == "gpt-6-astra" {
			return 6
		}
		return 2
	}
	return 1
}

// Pinned from Sub2API a6d32ede billing_service.go fallbackPrices. Catalog long
// context rules are configured separately rather than invented from sale prices.
func sourceFallbackStatsPricing(model string) *AccountStatsPricingRule {
	rates := map[string][4]float64{
		"gpt-5.4": {2.5, 15, .25, 2.5}, "gpt-5.4-mini": {.75, 4.5, .075, 0}, "gpt-5.4-nano": {.2, 1.25, .02, 0}, "gpt-5.5-pro": {30, 180, 30, 30}, "gpt-5.2": {1.75, 14, .175, 1.75}, "gpt-5.5": {5, 30, .5, 5}, "gpt-5.6-sol": {5, 30, .5, 6.25}, "gpt-5.6-terra": {2, 12, .2, 2.5}, "gpt-5.6-luna": {.2, 1.2, .02, .25}, "gpt-6-astra": {10, 50, 1, 12.5}, "gpt-6.1-sol": {2, 10, .1, 2.5}, "gpt-6-sol": {2, 10, .2, 2.5}, "gpt-6-luna": {.1, .5, .01, .125}, "gpt-5.3-codex": {1.5, 12, .15, 1.5}, "gpt-5.3-codex-spark": {1.5, 12, .15, 1.5},
	}
	r, ok := rates[sourceStatsModelKey(model)]
	if !ok {
		return nil
	}
	pricing := &AccountStatsPricingRule{Models: []string{model}, AccountStatsPrices: AccountStatsPrices{Input: statsPointer(r[0]), Output: statsPointer(r[1]), CacheRead: statsPointer(r[2]), CacheWrite: statsPointer(r[3])}}
	switch sourceStatsModelKey(model) {
	case "gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-6.1-sol":
		pricing.LongContextThreshold = 272000
		pricing.LongInputMultiplier = 2
		pricing.LongOutputMultiplier = 1.5
	}
	return pricing
}

// Keep source model-family admission separate from the native sale normalizer:
// unknown GPT-6 families and unknown GPT-5.6 variants have no source price.
func sourceStatsModelKey(model string) string {
	normalized := normalizeBillingModelName(model)
	normalized = strings.NewReplacer("gpt6", "gpt-6", "gpt5", "gpt-5", " ", "-", "_", "-").Replace(normalized)
	if strings.HasPrefix(normalized, "gpt-6") {
		for _, key := range []string{"gpt-6.1-sol", "gpt-6-sol", "gpt-6-luna", "gpt-6-astra"} {
			if normalized == key || strings.HasPrefix(normalized, key+"-") {
				return key
			}
		}
		if normalized == "gpt-6" {
			return "gpt-6-astra"
		}
		return ""
	}
	if strings.HasPrefix(normalized, "gpt-5.6-") {
		for _, key := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"} {
			if strings.Contains(normalized, key) {
				return key
			}
		}
		switch strings.TrimPrefix(normalized, "gpt-5.6-") {
		case "max", "none", "minimal", "low", "medium", "high", "xhigh":
			return "gpt-5.6-sol"
		}
		return ""
	}
	return CanonicalBillingModelKey(normalized)
}
