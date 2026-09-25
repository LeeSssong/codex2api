package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/codex2api/proxy"
	"github.com/gin-gonic/gin"
)

const (
	codexProbeConcurrency = 3
	codexProbeMaxAccounts = 100
	codexProbeMaxBody     = 16 << 10
)

type codexProbeRequest struct {
	IDs   []int64 `json:"ids"`
	Model string  `json:"model"`
	Level string  `json:"level"`
}

type codexProbeEvent struct {
	Type      string                   `json:"type"`
	Total     int                      `json:"total"`
	Completed int                      `json:"completed"`
	AccountID int64                    `json:"account_id,omitempty"`
	Result    *proxy.CodexProbeResult  `json:"result,omitempty"`
	Results   []proxy.CodexProbeResult `json:"results,omitempty"`
}

func validCodexProbeModel(model string) bool {
	if model == "" || len(model) > 128 {
		return false
	}
	for _, r := range model {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// ProbeCodexAccounts owns a bounded administrative job, separate from business
// selection and the ordinary connection test's account-wide health mutations.
func (h *Handler) ProbeCodexAccounts(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, codexProbeMaxBody)
	var req codexProbeRequest
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "Invalid or oversized probe request")
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeError(c, http.StatusBadRequest, "A probe request must contain one JSON object")
		return
	}
	req.Model = strings.ToLower(strings.TrimSpace(req.Model))
	if len(req.IDs) == 0 || len(req.IDs) > codexProbeMaxAccounts || !validCodexProbeModel(req.Model) || (req.Level != "basic" && req.Level != "tools") {
		writeError(c, http.StatusBadRequest, "Select 1–100 accounts, an exact model and basic or tools level")
		return
	}
	for _, id := range req.IDs {
		if id <= 0 {
			writeError(c, http.StatusBadRequest, "Account IDs must be positive integers")
			return
		}
	}
	req.IDs = positiveUniqueAdminIDs(req.IDs)
	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()
	stream := c.Query("stream") == "true"
	if stream {
		setupSSE(c)
		if !sendSSEJSON(c, codexProbeEvent{Type: "start", Total: len(req.IDs)}) {
			return
		}
	}
	results := make([]proxy.CodexProbeResult, 0, len(req.IDs))
	streamOpen := true
	for event := range h.runCodexProbeBatch(ctx, req) {
		if event.Result != nil {
			results = append(results, *event.Result)
		}
		event.Total, event.Completed = len(req.IDs), len(results)
		if stream && streamOpen && !sendSSEJSON(c, event) {
			streamOpen = false
			cancel()
		}
	}
	if ctx.Err() != nil {
		return
	}
	h.invalidateAccountSnapshotCaches()
	if stream {
		if streamOpen {
			sendSSEJSON(c, codexProbeEvent{Type: "done", Total: len(req.IDs), Completed: len(results), Results: results})
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"results": results, "total": len(req.IDs), "completed": len(results)})
}

func (h *Handler) runCodexProbeBatch(ctx context.Context, req codexProbeRequest) <-chan codexProbeEvent {
	events := make(chan codexProbeEvent, len(req.IDs)*2)
	jobs := make(chan int64, len(req.IDs))
	for _, id := range req.IDs {
		jobs <- id
	}
	close(jobs)
	var workers sync.WaitGroup
	for range min(codexProbeConcurrency, len(req.IDs)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for id := range jobs {
				result := h.runCodexAccountProbe(ctx, id, req, func() {
					events <- codexProbeEvent{Type: "testing", AccountID: id}
				})
				events <- codexProbeEvent{Type: "result", AccountID: id, Result: &result}
			}
		}()
	}
	go func() { workers.Wait(); close(events) }()
	return events
}

// Slots are shared by all jobs on this handler; submitting more batches cannot
// multiply upstream concurrency. Duplicate accounts are skipped, never queued.
func (h *Handler) claimCodexProbe(ctx context.Context, id int64) (func(), string) {
	h.codexProbeMu.Lock()
	if h.codexProbeSlots == nil {
		h.codexProbeSlots = make(chan struct{}, codexProbeConcurrency)
	}
	if h.codexProbeRunning == nil {
		h.codexProbeRunning = make(map[int64]bool)
	}
	if h.codexProbeRunning[id] {
		h.codexProbeMu.Unlock()
		return nil, "probe_in_progress"
	}
	h.codexProbeRunning[id] = true
	slots := h.codexProbeSlots
	h.codexProbeMu.Unlock()
	clear := func() { h.codexProbeMu.Lock(); delete(h.codexProbeRunning, id); h.codexProbeMu.Unlock() }
	select {
	case slots <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-slots; clear() }) }, ""
	case <-ctx.Done():
		clear()
		return nil, "canceled"
	}
}

func newCodexProbeLocalResult(id int64, req codexProbeRequest, outcome, code, message string) proxy.CodexProbeResult {
	now := time.Now().UTC()
	return proxy.CodexProbeResult{
		AccountID: id, Model: req.Model, Upstream: database.CodexPathBasispoints,
		Level: req.Level, Outcome: outcome, Capability: database.CapabilityUnknown,
		BasicOutcome: "not_run", ToolsOutcome: "not_run", ErrorCode: code,
		Message: message, StartedAt: now, FinishedAt: now,
	}
}

func (h *Handler) runCodexAccountProbe(ctx context.Context, id int64, req codexProbeRequest, testing func()) proxy.CodexProbeResult {
	local := func(outcome, code, message string) proxy.CodexProbeResult {
		return newCodexProbeLocalResult(id, req, outcome, code, message)
	}
	if ctx.Err() != nil {
		return local("canceled", "canceled", "Probe canceled")
	}
	release, reason := h.claimCodexProbe(ctx, id)
	if release == nil {
		if reason == "canceled" {
			return local("canceled", reason, "Probe canceled")
		}
		return local("skipped", reason, "Another probe is already testing this account")
	}
	defer release()
	if ctx.Err() != nil {
		return local("canceled", "canceled", "Probe canceled")
	}
	testing()
	row, err := h.db.GetAccountByID(ctx, id)
	if err != nil {
		if ctx.Err() != nil {
			return local("canceled", "canceled", "Probe canceled")
		}
		return local("error", "account_lookup_failed", "Could not load the account")
	}
	if row == nil {
		return local("skipped", "account_missing", "Account does not exist")
	}
	if row.GetCredential("upstream_type") != "" || row.GetCredential("auth_mode") == auth.CodexAuthModeAgentIdentity {
		return local("skipped", "ineligible_identity", "BPS probes require a Codex OAuth account")
	}
	if !row.Enabled {
		return local("blocked", "account_disabled", "The administrator disabled this account")
	}
	account := h.store.FindByID(id)
	if account == nil {
		return local("skipped", "account_unavailable", "Account is not loaded in the runtime pool")
	}
	if _, blocked := h.store.LinkedDeactivatedWorkspaceResult(account); blocked {
		return local("workspace_deactivated", "deactivated_workspace", "The account workspace is deactivated")
	}
	if available, reason, _ := account.StateAvailability(req.Model, time.Now()); !available {
		outcome := "blocked"
		switch reason {
		case "unauthorized", "credential_unavailable":
			outcome = "unauthorized"
		case "rate_limited", "usage_exhausted", "quota_paused", "model_cooldown":
			outcome = "rate_limited"
		}
		result := local(outcome, reason, "Current account state prevents a BPS probe")
		result.Capability = account.CodexPathSnapshot(database.CodexPathBasispoints, req.Model, time.Now()).Capability
		return result
	}
	proxyURL, usable := h.store.ResolveUsableProxyForAccount(account)
	if !usable {
		return local("blocked", "proxy_unavailable", "The configured account proxy is unavailable")
	}
	probe := h.codexCapabilityProbe
	if probe == nil {
		probe = proxy.ProbeCodexCapability
	}
	return probe(ctx, account, proxy.CodexCapabilityProbeOptions{Model: req.Model, Level: req.Level, ProxyURL: proxyURL})
}

func (h *Handler) codexProbeResults(ctx context.Context, id int64, model string) ([]database.CodexProbeResult, error) {
	results, err := h.db.GetCodexCapabilityProbeResults(ctx, id)
	if err != nil {
		return nil, err
	}
	if model == "" {
		return results, nil
	}
	filtered := make([]database.CodexProbeResult, 0, 2)
	for _, result := range results {
		if result.Model == model {
			filtered = append(filtered, result)
		}
	}
	return filtered, nil
}

func (h *Handler) GetCodexProbes(c *gin.Context) {
	id, err := parseCodexRouteID(c.Param("id"))
	if err != nil {
		writeError(c, http.StatusBadRequest, err.Error())
		return
	}
	model := strings.ToLower(strings.TrimSpace(c.Query("model")))
	if model != "" && !validCodexProbeModel(model) {
		writeError(c, http.StatusBadRequest, "Invalid model")
		return
	}
	row, err := h.db.GetAccountByID(c.Request.Context(), id)
	if err != nil {
		writeInternalError(c, err)
		return
	}
	if row == nil {
		writeError(c, http.StatusNotFound, "Account not found")
		return
	}
	results, err := h.codexProbeResults(c.Request.Context(), id, model)
	if err != nil {
		writeInternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"results": results})
}
