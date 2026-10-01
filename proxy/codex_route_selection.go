package proxy

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/codex2api/api"
	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
)

func (d *CodexRouteDecision) rememberSelectionReasons(id int64, reasons []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(reasons) == 0 {
		delete(d.selectionReasons, id)
		return
	}
	if d.selectionReasons == nil {
		d.selectionReasons = make(map[int64][]string)
	}
	d.selectionReasons[id] = reasons
}

func (h *Handler) codexRouteNoCandidatesError(ctx context.Context, keyID int64, filter auth.AccountFilter) *Error {
	d := codexRouteFromContext(ctx)
	counts := map[string]int{}
	now := time.Now()
	var retryAt time.Time
	for _, account := range h.store.Accounts() {
		if !account.AllowsAPIKey(keyID) || !h.store.APIKeyAllowsAccount(keyID, account) {
			counts["key_scope"]++
			continue
		}
		if filter == nil || filter(account) {
			continue
		}
		d.mu.Lock()
		reasons := append([]string(nil), d.selectionReasons[account.ID()]...)
		d.mu.Unlock()
		if len(reasons) == 0 {
			reasons = []string{"request_scope_or_model"}
		}
		for _, reason := range reasons {
			counts[reason]++
			path, kind, _ := strings.Cut(reason, ":")
			if kind == "path_cooldown" || kind == "path_recovering" {
				until := account.CodexPathSnapshot(path, d.EffectiveModel, now).CooldownUntil
				if !until.After(now) {
					until = now.Add(time.Second)
				}
				if retryAt.IsZero() || until.Before(retryAt) {
					retryAt = until
				}
			}
		}
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, counts[key]))
	}
	summary := strings.Join(parts, ", ")
	err := routeLocalError("codex_route_no_candidates", "No eligible account for this request. Rejection reasons: "+summary)
	if !retryAt.IsZero() {
		err.Code = "codex_route_cooldown"
		err.Retryable = true
		err.RetryAfterSeconds = max(1, int((retryAt.Sub(now)+time.Second-1)/time.Second))
		err.Message = fmt.Sprintf("The eligible upstream path is temporarily cooling down. Retry after %d seconds. Rejection reasons: %s", err.RetryAfterSeconds, summary)
	}
	d.mu.Lock()
	pinned, history := d.pinnedPath, d.HistoryLockReason
	d.mu.Unlock()
	log.Printf("[CodexRoute] selection_failed key_id=%d model=%q code=%s pinned=%s history=%s reasons=%s", keyID, d.EffectiveModel, err.Code, pinned, history, summary)
	return err
}

func routeAPIErrorType(err *Error) api.ErrorType {
	if err.HTTPStatus >= http.StatusInternalServerError {
		return api.ErrorTypeServer
	}
	return api.ErrorType(err.Type)
}

// Local selection failures have no upstream account or billable usage, but must
// remain visible beside upstream errors instead of disappearing from the UI.
func (h *Handler) logCodexRouteSelectionError(c *gin.Context, d *CodexRouteDecision, err *Error) {
	if c.Request.Context().Err() != nil {
		return
	}
	d.mu.Lock()
	if d.selectionLogged {
		d.mu.Unlock()
		return
	}
	d.selectionLogged = true
	model, effective := d.RequestedModel, d.EffectiveModel
	d.mu.Unlock()
	h.logUsageForRequest(c, &database.UsageLogInput{
		Channel: database.UpstreamChannelCodex, Endpoint: c.Request.URL.Path,
		Model: model, EffectiveModel: effective, StatusCode: err.HTTPStatus,
		AttemptIndex: 1, UpstreamErrorKind: "local_route", ErrorMessage: err.Error(),
		ViaWebsocket: strings.EqualFold(c.Request.Header.Get("Upgrade"), "websocket"),
	})
}
