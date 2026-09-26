package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func normalizeBasispointsUsage(usage map[string]any, asInput bool) *UsageInfo {
	if usage == nil {
		return nil
	}
	raw, _ := json.Marshal(usage)
	observed := extractUsageFromResult(gjson.ParseBytes(raw))
	if asInput {
		for _, path := range responsesCacheWriteFields {
			parts := strings.Split(path, ".")
			parent := usage
			for _, part := range parts[:len(parts)-1] {
				parent, _ = parent[part].(map[string]any)
				if parent == nil {
					break
				}
			}
			if parent != nil {
				if _, exists := parent[parts[len(parts)-1]]; exists {
					parent[parts[len(parts)-1]] = 0
				}
			}
		}
	}
	return observed
}

var responsesCacheWriteFields = []string{
	"cache_write_tokens", "cache_creation_input_tokens", "cache_write_input_tokens", "cache_creation_tokens",
	"input_tokens_details.cache_write_tokens", "prompt_tokens_details.cache_write_tokens",
	"input_tokens_details.cache_creation_tokens", "prompt_tokens_details.cache_creation_tokens",
	"cache_write_5m_tokens", "cache_write_1h_tokens",
	"cache_creation.ephemeral_5m_input_tokens", "cache_creation.ephemeral_1h_input_tokens",
}

func extractResponsesCacheWrites(usage gjson.Result, result *UsageInfo) {
	writeTotal := 0
	for _, field := range responsesCacheWriteFields[:8] {
		writeTotal = max(writeTotal, int(usage.Get(field).Int()))
	}
	write5 := max(0, int(usage.Get("cache_creation.ephemeral_5m_input_tokens").Int()), int(usage.Get("cache_write_5m_tokens").Int()))
	write1 := max(0, int(usage.Get("cache_creation.ephemeral_1h_input_tokens").Int()), int(usage.Get("cache_write_1h_tokens").Int()))
	available := max(0, result.InputTokens-result.CachedTokens)
	write1 = min(write1, available)
	write5 = min(write5, available-write1)
	writeTotal = min(max(writeTotal, write5+write1), available)
	// An unspecified remainder uses the native default five-minute price.
	write5 += writeTotal - write5 - write1
	result.CacheWriteTokens, result.CacheWrite5mTokens, result.CacheWrite1hTokens = writeTotal, write5, write1
}

var basispointsRelayedHeaders = []string{"X-Codex2API-Upstream", "X-Codex2API-Reasoning-Effort", "X-Codex2API-Basispoints-Warnings", basispointsBypassHeader}

func relayBasispointsResponseHeaders(c *gin.Context, response *http.Response) {
	for _, name := range basispointsRelayedHeaders {
		c.Writer.Header().Del(name)
	}
	if response.Header.Get("X-Codex2API-Upstream") != "basispoints" && response.Header.Get(basispointsBypassHeader) == "" {
		return
	}
	for _, name := range basispointsRelayedHeaders {
		if value := response.Header.Get(name); value != "" {
			c.Header(name, value)
		}
	}
}

// Local validation failures must be visible without being billed as upstream
// attempts or leaking caller-controlled tool names, prompts or arguments.
func (h *Handler) logBasispointsPreparationFailure(c *gin.Context, account *auth.Account, err error, model, effort string, durationMs, attempt int, stream bool) {
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != ErrorCodeBasispointsInvalidRequest {
		return
	}
	endpoint := c.Request.URL.Path
	h.logUsageForRequest(c, &database.UsageLogInput{
		AccountID: account.ID(), Endpoint: endpoint, InboundEndpoint: endpoint,
		Model: model, StatusCode: http.StatusBadRequest, DurationMs: durationMs,
		ReasoningEffort: effectiveReasoningEffortForAccount(account, effort, c.Request.Context()),
		Stream:          stream, AttemptIndex: attempt + 1,
		UpstreamErrorKind: ErrorCodeBasispointsInvalidRequest,
		ErrorMessage:      fmt.Sprintf("%s · stage=prepare · category=%s; request rejected locally before contacting Basispoints", ErrorCodeBasispointsInvalidRequest, basispointsPreparationCategory(failure)),
	})
}
