package proxy

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/codex2api/smartops"
	"github.com/gin-gonic/gin"
)

type concurrencyRequestKey struct{}
type concurrencyRequestResult struct {
	observation                           smartops.ConcurrencyObservation
	failed, completed, excluded, canceled bool
}
type concurrencyRequestAudit struct {
	mu             sync.Mutex
	entries        map[int64]*concurrencyRequestResult
	store          *auth.Store
	report         func(smartops.ConcurrencyObservation)
	finished       bool
	deliveryFailed bool
}

func concurrencyRequestEligible(c *gin.Context, body []byte) bool {
	if c == nil || c.Request == nil || c.Request.Method != http.MethodPost || rawResponsesBodyShouldForceHTTPForImageGeneration(body) {
		return false
	}
	path := strings.TrimPrefix(c.Request.URL.Path, "/v1")
	switch path {
	case "/responses", "/chat/completions", "/messages", "/backend-api/codex/responses":
		return !requestBodyCompactionMeta(body).UsageTriggered
	}
	return strings.HasPrefix(c.Request.URL.Path, "/v1beta/models/") && (strings.HasSuffix(path, ":generateContent") || strings.HasSuffix(path, ":streamGenerateContent"))
}

func (h *Handler) beginConcurrencyRequest(c *gin.Context, body []byte) func() {
	if !concurrencyRequestEligible(c, body) || h.store == nil {
		return func() {}
	}
	// Protocol adapters may delegate to another handler in this same request.
	if concurrencyAuditFromContext(c.Request.Context()) != nil {
		return func() {}
	}
	a := &concurrencyRequestAudit{entries: make(map[int64]*concurrencyRequestResult), store: h.store, report: h.store.ReportSmartOpsConcurrencyObservation}
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), concurrencyRequestKey{}, a))
	c.Writer = &concurrencyDeliveryWriter{ResponseWriter: c.Writer, audit: a}
	return func() { a.finish(c.Request.Context()) }
}
func concurrencyAuditFromContext(ctx context.Context) *concurrencyRequestAudit {
	if ctx == nil {
		return nil
	}
	a, _ := ctx.Value(concurrencyRequestKey{}).(*concurrencyRequestAudit)
	return a
}
func (a *concurrencyRequestAudit) begin(o smartops.ConcurrencyObservation) {
	if o.AccountID <= 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.finished && a.entries[o.AccountID] == nil {
		a.entries[o.AccountID] = &concurrencyRequestResult{observation: o}
	}
}
func beginConcurrencyAttempt(ctx context.Context, account *auth.Account, ws bool) {
	a := concurrencyAuditFromContext(ctx)
	if a == nil || a.store == nil || account == nil {
		return
	}
	if ws {
		a.exclude(account.ID())
		return
	}
	a.begin(a.store.BeginSmartOpsConcurrencyObservation(account))
}
func (a *concurrencyRequestAudit) exclude(id int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.entries[id] == nil {
		a.entries[id] = &concurrencyRequestResult{observation: smartops.ConcurrencyObservation{AccountID: id}}
	}
	a.entries[id].excluded = true
}
func (a *concurrencyRequestAudit) transportResult(id int64, resp *http.Response, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if entry := a.entries[id]; entry != nil && (err != nil || resp == nil || resp.StatusCode >= 400) {
		entry.failed = true
	}
}
func (a *concurrencyRequestAudit) result(input *database.UsageLogInput) {
	if input == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	entry := a.entries[input.AccountID]
	if entry == nil {
		return
	}
	if input.ViaWebsocket || input.Compact || input.InternalReason != "" || input.ImageCount > 0 || input.VideoCount > 0 {
		entry.excluded = true
		return
	}
	if input.StatusCode == 499 {
		entry.canceled = true
		return
	}
	if input.StatusCode >= 200 && input.StatusCode < 300 && input.ErrorMessage == "" && input.UpstreamErrorKind == "" {
		entry.completed = true
	} else {
		entry.failed = true
	}
}
func (a *concurrencyRequestAudit) finish(ctx context.Context) {
	a.mu.Lock()
	if a.finished {
		a.mu.Unlock()
		return
	}
	a.finished = true
	var outcomes []smartops.ConcurrencyObservation
	for _, entry := range a.entries {
		if entry.excluded || (!entry.failed && (ctx.Err() != nil || a.deliveryFailed || entry.canceled)) {
			continue
		}
		o := entry.observation
		o.Success = entry.completed && !entry.failed
		outcomes = append(outcomes, o)
	}
	a.mu.Unlock()
	for _, o := range outcomes {
		if a.report != nil {
			a.report(o)
		}
	}
}

// Track delivery failures as well as context cancellation: some writers return
// an error before the request context has been cancelled.
type concurrencyDeliveryWriter struct {
	gin.ResponseWriter
	audit *concurrencyRequestAudit
}

func (w *concurrencyDeliveryWriter) record(n, wanted int, err error) (int, error) {
	if n != wanted && err == nil {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.audit.mu.Lock()
		w.audit.deliveryFailed = true
		w.audit.mu.Unlock()
	}
	return n, err
}
func (w *concurrencyDeliveryWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	return w.record(n, len(b), err)
}
func (w *concurrencyDeliveryWriter) WriteString(s string) (int, error) {
	n, err := w.ResponseWriter.WriteString(s)
	return w.record(n, len(s), err)
}

// Keep Gin's writer chain available to HTTP informational keepalives and
// net/http response controllers.
func (w *concurrencyDeliveryWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
