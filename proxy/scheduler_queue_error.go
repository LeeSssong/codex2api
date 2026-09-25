package proxy

import (
	"errors"
	"net/http"
	"strings"

	"github.com/codex2api/api"
	"github.com/codex2api/auth"
	"github.com/gin-gonic/gin"
)

const schedulerQueueFullMessage = "Scheduler wait queue is full. Retry after 1 second."

func schedulerQueueFullAPIError() *api.APIError {
	return api.NewAPIError(api.ErrCodeServiceUnavailable, schedulerQueueFullMessage, api.ErrorTypeServer)
}

// Queue overload is a local retryable capacity error. It must not enter the
// quota scan or continuous-retry exclusion reset, or overwrite committed SSE.
func writeSchedulerQueueError(c *gin.Context, err error, protocol continuousRetryHTTPProtocol) bool {
	var routeErr *Error
	if errors.As(err, &routeErr) && strings.HasPrefix(routeErr.Code, "codex_route_") {
		if !claimContinuousRetryTerminal(c, protocol) || c.Request.Context().Err() != nil {
			return true
		}
		switch protocol {
		case continuousRetryProtocolAnthropic:
			if !writeCommittedAnthropicRetryError(c, "overloaded_error", routeErr.Message) {
				sendAnthropicError(c, routeErr.HTTPStatus, "overloaded_error", routeErr.Message)
			}
		case continuousRetryProtocolChat:
			if !writeCommittedChatRetryError(c, routeErr.Message) {
				c.JSON(routeErr.HTTPStatus, gin.H{"error": api.NewAPIError(api.ErrorCode(routeErr.Code), routeErr.Message, api.ErrorTypeServer)})
			}
		default:
			if !writeCommittedResponsesRetryError(c, routeErr.Message) {
				c.JSON(routeErr.HTTPStatus, gin.H{"error": api.NewAPIError(api.ErrorCode(routeErr.Code), routeErr.Message, api.ErrorTypeServer)})
			}
		}
		return true
	}
	if !errors.Is(err, auth.ErrSchedulerQueueFull) {
		return false
	}
	if !claimContinuousRetryTerminal(c, protocol) || c.Request.Context().Err() != nil {
		return true
	}
	if !c.Writer.Written() {
		c.Header("Retry-After", "1")
	}
	switch protocol {
	case continuousRetryProtocolAnthropic:
		if !writeCommittedAnthropicRetryError(c, "overloaded_error", schedulerQueueFullMessage) {
			sendAnthropicError(c, http.StatusServiceUnavailable, "overloaded_error", schedulerQueueFullMessage)
		}
	case continuousRetryProtocolChat:
		if !writeCommittedChatRetryError(c, schedulerQueueFullMessage) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": schedulerQueueFullAPIError()})
		}
	default:
		if !writeCommittedResponsesRetryError(c, schedulerQueueFullMessage) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": schedulerQueueFullAPIError()})
		}
	}
	return true
}
