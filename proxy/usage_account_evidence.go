package proxy

import (
	"encoding/json"
	"strings"

	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// accountUsageObservation is captured before legacy error reporting substitutes
// character-count estimates. Account statistics consume only upstream evidence.
type accountUsageObservation struct {
	usage    database.AccountUsageEvidence
	admitted bool
}

func observeAccountUsage(usage *UsageInfo, result bool) accountUsageObservation {
	o := accountUsageObservation{admitted: result || usage != nil}
	known := usage != nil
	o.usage.UsageKnown = &known
	if usage == nil {
		return o
	}
	o.usage = database.AccountUsageEvidence{
		UsageKnown:  &known,
		InputTokens: max(0, usage.InputTokens), OutputTokens: max(0, usage.OutputTokens),
		CachedTokens: max(0, usage.CachedTokens), CacheWrite5mTokens: max(0, usage.CacheWrite5mTokens), CacheWrite1hTokens: max(0, usage.CacheWrite1hTokens),
		ImageInputTokens: max(0, usage.ImageInputTokens), ImageOutputTokens: max(0, usage.ImageOutputTokens), CachedImageInputTokens: max(0, usage.CachedImageInputTokens),
	}
	if o.usage.InputTokens == 0 {
		o.usage.InputTokens = max(0, usage.PromptTokens)
	}
	if o.usage.OutputTokens == 0 {
		o.usage.OutputTokens = max(0, usage.CompletionTokens)
	}
	if o.usage.CacheWrite5mTokens+o.usage.CacheWrite1hTokens == 0 {
		o.usage.CacheWrite5mTokens = max(0, usage.CacheWriteTokens, usage.accountCacheWriteTokens)
	}
	return o
}

func (o accountUsageObservation) apply(input *database.UsageLogInput) {
	if input == nil {
		return
	}
	evidence := o.usage
	if evidence.EffectiveModel == "" {
		evidence.EffectiveModel = upstreamSentModelForAudit(input.EffectiveModel, input.Model)
	}
	admitted := o.admitted
	cache := int64(evidence.CachedTokens) + int64(evidence.CacheWrite5mTokens) + int64(evidence.CacheWrite1hTokens)
	// Source records the exclusive input bucket clamped at zero plus the
	// original cache categories, including inconsistent upstream totals.
	tokens := max(int64(evidence.InputTokens)-cache, 0) + cache + int64(evidence.OutputTokens)
	input.AccountUsage = &evidence
	input.QuotaUsageAdmitted = &admitted
	input.QuotaTokens = &tokens
}

// finalizeAccountUsageEvidence maps the native diagnostic ledger to source
// gateway events. A retry/499 flag alone cannot reject real upstream usage.
func finalizeAccountUsageEvidence(input *database.UsageLogInput) {
	if input == nil {
		return
	}
	if input.QuotaUsageAdmitted == nil {
		observed := input.InputTokens > 0 || input.OutputTokens > 0 || input.PromptTokens > 0 || input.CompletionTokens > 0 || input.CachedTokens > 0 || input.CacheWrite5mTokens > 0 || input.CacheWrite1hTokens > 0 || input.ImageInputTokens > 0 || input.CachedImageInputTokens > 0 || input.ImageCount > 0 || input.ImageOutputTokens > 0
		result := input.StatusCode >= 200 && input.StatusCode < 300 && !input.IsRetryAttempt
		usage := &UsageInfo{InputTokens: input.InputTokens, OutputTokens: input.OutputTokens, PromptTokens: input.PromptTokens, CompletionTokens: input.CompletionTokens, CachedTokens: input.CachedTokens, CacheWrite5mTokens: input.CacheWrite5mTokens, CacheWrite1hTokens: input.CacheWrite1hTokens, ImageInputTokens: input.ImageInputTokens, ImageOutputTokens: input.ImageOutputTokens, CachedImageInputTokens: input.CachedImageInputTokens}
		if !observed && !result {
			usage = nil
		}
		observeAccountUsage(usage, result).apply(input)
	}
	reason := strings.TrimSpace(input.InternalReason)
	admittedChild := input.APIKeyID > 0 && (reason == "overflow_compact_summary" || reason == "prompt_intelligence_cy_analysis")
	if reason != "" && !admittedChild {
		admitted := false
		tokens := int64(0)
		input.QuotaUsageAdmitted = &admitted
		input.QuotaTokens = &tokens
	}
}

// logAccountUsageRetry records an observed partial upstream attempt once before
// retrying. Diagnostic-only cyber rows retain their existing incident behavior.
func (h *Handler) logAccountUsageRetry(c *gin.Context, input database.UsageLogInput, usage *UsageInfo, terminal []byte, actualTier, requestedTier, incidentID string) {
	usage = observedAttemptUsage(usage, terminal)
	if usage == nil && strings.TrimSpace(incidentID) == "" {
		return
	}
	input.IsRetryAttempt = true
	input.AccountStatsOnly = true
	input.PromptPolicyIncidentID = incidentID
	tiers := resolveUsageServiceTiers(actualTier, requestedTier)
	input.ServiceTier, input.RequestedServiceTier, input.ActualServiceTier, input.BillingServiceTier = tiers.ServiceTier, tiers.RequestedServiceTier, tiers.ActualServiceTier, tiers.BillingServiceTier
	// These rows extend only the account ledger. The previous retry path had
	// no native token/fee event (or a zero-token incident diagnostic), so keep
	// native counters unchanged while AccountUsage holds the full observation.
	input.InputTokens, input.OutputTokens, input.PromptTokens, input.CompletionTokens, input.TotalTokens = 0, 0, 0, 0, 0
	input.CachedTokens, input.ReasoningTokens = 0, 0
	input.CacheWrite5mTokens, input.CacheWrite1hTokens = 0, 0
	input.ImageInputTokens, input.ImageOutputTokens, input.CachedImageInputTokens = 0, 0, 0
	observeAccountUsageFromAttempt(usage, terminal, false).apply(&input)
	h.logUsageForRequest(c, &input)
}

// Nested presence takes precedence over top-level aliases, including zero.
func observedOpenAICacheWriteTokens(usage gjson.Result) int {
	for _, path := range []string{"input_tokens_details.cache_write_tokens", "prompt_tokens_details.cache_write_tokens", "input_tokens_details.cache_creation_tokens", "prompt_tokens_details.cache_creation_tokens"} {
		if value := usage.Get(path); value.Exists() {
			return max(0, int(value.Int()))
		}
	}
	for _, path := range []string{"cache_write_tokens", "cache_creation_input_tokens", "cache_write_input_tokens", "cache_creation_tokens"} {
		if value := int(usage.Get(path).Int()); value > 0 {
			return value
		}
	}
	return 0
}

// Failed Responses terminals carry metered usage too. Legacy client translation
// may skip that snapshot; account evidence must still retain the last report.
func observedAttemptUsage(earlier *UsageInfo, terminal []byte) *UsageInfo {
	if len(terminal) > 0 {
		if usage := extractUsage(terminal); usage != nil {
			return usage
		}
		if usage := extractUsageFromResult(gjson.GetBytes(terminal, "usage")); usage != nil {
			return usage
		}
	}
	return earlier
}

// Dedup is request-local: no user-controlled identifier can suppress another
// request. Separate upstream attempts and independent child calls remain distinct.
func deduplicateAccountUsageEvidence(c *gin.Context, input *database.UsageLogInput) {
	if c == nil || input == nil || input.QuotaUsageAdmitted == nil || !*input.QuotaUsageAdmitted {
		return
	}
	if input.UpstreamRequestID == "" && input.AttemptIndex == 0 {
		return
	}
	const contextKey = "accountQuotaAdmittedAttempts"
	seen, _ := c.Get(contextKey)
	keys, _ := seen.(map[string]struct{})
	if keys == nil {
		keys = make(map[string]struct{})
		c.Set(contextKey, keys)
	}
	// Include the observed snapshot: an unknown audit cannot consume a later
	// complete observed-usage delivery's billing identity.
	raw, _ := json.Marshal([]any{input.RequestID, input.UpstreamRequestID, input.AccountID, input.AttemptIndex, input.Endpoint, input.AccountUsage})
	key := string(raw)
	if _, duplicate := keys[key]; duplicate {
		admitted := false
		tokens := int64(0)
		input.QuotaUsageAdmitted = &admitted
		input.QuotaTokens = &tokens
		return
	}
	keys[key] = struct{}{}
}

func observeAccountUsageFromAttempt(earlier *UsageInfo, terminal []byte, result bool) accountUsageObservation {
	observation := observeAccountUsage(observedAttemptUsage(earlier, terminal), result)
	for _, path := range []string{"response.service_tier", "service_tier"} {
		if tier := gjson.GetBytes(terminal, path); tier.Type == gjson.String {
			value := tier.String()
			observation.usage.ServiceTier = &value
			break
		}
	}
	return observation
}

const accountUsageDownstreamWebsocketKey = "accountUsageDownstreamWebsocket"

// Source HTTP partial-result recording and WebSocket AfterTurn have different
// admission boundaries. ViaWebsocket describes the upstream transport and must
// not be used to infer which client protocol invoked this request.
func applyAccountUsageIngressPolicy(input *database.UsageLogInput, downstreamWebsocket bool) {
	if input == nil || input.QuotaUsageAdmitted == nil {
		return
	}
	reason := strings.TrimSpace(input.InternalReason)
	if reason != "" && !(input.APIKeyID > 0 && (reason == "overflow_compact_summary" || reason == "prompt_intelligence_cy_analysis")) {
		return
	}
	if input.ImageCount > 0 {
		admitted := true
		input.QuotaUsageAdmitted = &admitted
	}
	failed := input.StatusCode < 200 || input.StatusCode >= 300
	if !failed {
		return
	}
	cyber := input.UpstreamErrorKind == "cyber_policy"
	if cyber && input.AccountUsage != nil {
		// Error-path RecordCyberPolicyUsageLog constructs a plain input/output
		// result; it does not copy the normal cache/image detail categories.
		evidence := *input.AccountUsage
		evidence.CachedTokens, evidence.CacheWrite5mTokens, evidence.CacheWrite1hTokens = 0, 0, 0
		evidence.ImageInputTokens, evidence.ImageOutputTokens, evidence.CachedImageInputTokens = 0, 0, 0
		(accountUsageObservation{usage: evidence, admitted: true}).apply(input)
		return
	}
	if downstreamWebsocket && input.ImageCount <= 0 {
		admitted := false
		input.QuotaUsageAdmitted = &admitted
		tokens := int64(0)
		input.QuotaTokens = &tokens
	}
}
