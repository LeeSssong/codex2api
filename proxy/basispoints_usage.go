package proxy

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
)

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
		ReasoningEffort: effectiveReasoningEffortForAccount(account, effort),
		Stream:          stream, AttemptIndex: attempt + 1,
		UpstreamErrorKind: ErrorCodeBasispointsInvalidRequest,
		ErrorMessage:      fmt.Sprintf("%s · stage=prepare · category=%s; request rejected locally before contacting Basispoints", ErrorCodeBasispointsInvalidRequest, basispointsPreparationCategory(failure)),
	})
}
