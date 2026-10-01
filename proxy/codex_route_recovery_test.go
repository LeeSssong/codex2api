package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/cache"
	"github.com/codex2api/config"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
)

type historyWriteFaultCache struct {
	cache.TokenCache
	failures int
}

func (c *historyWriteFaultCache) SetRuntime(ctx context.Context, namespace, key string, value json.RawMessage, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.failures > 0 {
		c.failures--
		return errors.New("synthetic cache failure")
	}
	return c.TokenCache.SetRuntime(ctx, namespace, key, value, ttl)
}

func TestCodexHistoryWriteSurvivesCancellationAndRetriesFailedIdentity(t *testing.T) {
	for _, cancelClient := range []bool{false, true} {
		t.Run(map[bool]string{false: "partial failure", true: "client canceled"}[cancelClient], func(t *testing.T) {
			enableBasispointsForTest(t)
			memory := cache.NewMemory(1)
			t.Cleanup(func() { _ = memory.Close() })
			backend := &historyWriteFaultCache{TokenCache: memory, failures: 1}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			origin := newCodexRouteDecision(ctx, "gpt-6-astra", "gpt-6-astra", database.APIKeyLimits{CodexRoutePolicy: "basispoints_prefer"}, 3)
			origin.historyCache, origin.historyOwner = backend, "key:9"
			attempt := &codexRouteAttemptState{decision: origin, account: &auth.Account{DBID: 99}, path: "codex"}
			seen := map[string]bool{}
			if cancelClient {
				cancel()
			}
			attempt.recordHistoryRoute([]byte(`{"type":"response.output_item.done","item":{"type":"reasoning","encrypted_content":"opaque"}}`), seen)
			if len(seen) != 0 {
				t.Fatal("failed cache write marked an identity saved")
			}
			attempt.recordHistoryRoute([]byte(`{"type":"response.completed","response":{"id":"response-1","output":[{"type":"reasoning","encrypted_content":"opaque"}]}}`), seen)
			for _, body := range []string{`{"input":[{"type":"reasoning","encrypted_content":"opaque"}]}`, `{"previous_response_id":"response-1"}`} {
				next := newCodexRouteDecision(context.Background(), "gpt-6-astra", "gpt-6-astra", database.APIKeyLimits{CodexRoutePolicy: "basispoints_prefer"}, 3)
				next.historyCache, next.historyOwner = backend, origin.historyOwner
				next.bindHistory([]byte(body))
				if next.pinnedPath != "codex" || next.HistoryLockReason != "history_provenance" {
					t.Fatalf("produced history lost its native origin: %+v", next)
				}
			}
		})
	}
}

func TestCodexHistoryNativeFeatureConflictReturnsActionableClientError(t *testing.T) {
	enableBasispointsForTest(t)
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, CodexBasispointsEnabled: true})
	t.Cleanup(store.Stop)
	account := &auth.Account{DBID: 99, AccessToken: "token", AccountID: "workspace", PlanType: "pro"}
	store.AddAccount(account)
	unexpected := func(*http.Request) (*http.Response, error) {
		t.Error("incompatible request reached an upstream")
		return routeTestResponse(400, `{"error":{"code":"unexpected_request"}}`), nil
	}
	installBasispointsTransport(t, account, unexpected)
	installClaudeBoundaryTransport(t, account, unexpected)
	db := newTestModelRegistryDB(t)
	h := NewHandler(store, db, &config.Config{}, nil)
	r := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(r)
	c.Request = httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"gpt-6-astra","input":[{"type":"reasoning","encrypted_content":"gAAAAA_opaque"}],"tools":[{"type":"web_search"}]}`))
	c.Request.Header.Set("Content-Type", "application/json")
	attachUpstreamTrace(c, store)
	c.Set(contextAPIKeyID, int64(3))
	c.Set(contextAPIKeyRow, &database.APIKeyRow{ID: 3, Limits: database.APIKeyLimits{CodexRoutePolicy: "basispoints_prefer"}})
	h.Responses(c)
	if r.Code != http.StatusBadRequest || !strings.Contains(r.Body.String(), "codex_route_history_protocol_conflict") || !strings.Contains(r.Body.String(), "web_search") || !strings.Contains(r.Body.String(), "invalid_request_error") {
		t.Fatalf("incompatible history was sent or hidden behind 503: %d %s", r.Code, r.Body.String())
	}
	// Rendering the terminal error again must not duplicate local usage records.
	d := codexRouteFromContext(c.Request.Context())
	d.recordSelectionError(d.historyError)
	db.FlushUsageLogs()
	page, err := db.ListUsageLogsByTimeRangePaged(context.Background(), database.UsageLogFilter{Start: time.Now().Add(-time.Minute), End: time.Now().Add(time.Minute), Page: 1, PageSize: 10, ErrorOnly: true})
	if err != nil || len(page.Logs) != 1 {
		t.Fatalf("local error missing or duplicated: %+v %v", page, err)
	}
	row := page.Logs[0]
	if row.AccountID != 0 || row.APIKeyID != 3 || row.UpstreamErrorKind != "local_route" || row.TotalTokens != 0 || row.StatusCode != 400 || row.RequestID == "" {
		t.Fatalf("local error attributed to an upstream or missing metadata: %+v", row)
	}
}

func TestCodexWebSocketHistoryFeatureConflictNeverDispatches(t *testing.T) {
	enableBasispointsForTest(t)
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, CodexBasispointsEnabled: true})
	t.Cleanup(store.Stop)
	account := &auth.Account{DBID: 98, AccessToken: "token", AccountID: "workspace", PlanType: "pro"}
	store.AddAccount(account)
	unexpected := func(*http.Request) (*http.Response, error) {
		t.Error("WebSocket history conflict reached upstream")
		return routeTestResponse(400, `{"error":{"code":"unexpected_request"}}`), nil
	}
	installBasispointsTransport(t, account, unexpected)
	installClaudeBoundaryTransport(t, account, unexpected)
	h := NewHandler(store, nil, &config.Config{}, nil)
	router := gin.New()
	router.GET("/v1/responses", func(c *gin.Context) {
		c.Set(contextAPIKeyID, int64(3))
		c.Set(contextAPIKeyRow, &database.APIKeyRow{ID: 3, Limits: database.APIKeyLimits{CodexRoutePolicy: "basispoints_prefer"}})
		h.ResponsesWebSocket(c)
	})
	server := httptest.NewServer(router)
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"gpt-6-astra","input":[{"type":"reasoning","encrypted_content":"gAAAAA_opaque"}],"tools":[{"type":"web_search"}]}`)); err != nil {
		t.Fatal(err)
	}
	_, payload, err := conn.ReadMessage()
	if err != nil || gjson.GetBytes(payload, "error.code").String() != "codex_route_history_protocol_conflict" || gjson.GetBytes(payload, "error.type").String() != "invalid_request_error" {
		t.Fatalf("WS lost local conflict: %s %v", payload, err)
	}
}

func TestCodexRejectedPathIsSkippedWithoutChangingAccountCapability(t *testing.T) {
	enableBasispointsForTest(t)
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, CodexBasispointsEnabled: true})
	t.Cleanup(store.Stop)
	a := &auth.Account{DBID: 99, AccessToken: "token", AccountID: "workspace", PlanType: "pro"}
	store.AddAccount(a)
	h := NewHandler(store, nil, &config.Config{}, nil)
	d := newCodexRouteDecision(context.Background(), "gpt-6-astra", "gpt-6-astra", database.APIKeyLimits{CodexRoutePolicy: "basispoints_prefer"}, 3)
	attempt := &codexRouteAttemptState{decision: d, account: a, path: "basispoints", model: "gpt-6-astra", started: time.Now()}
	attempt.recordFailure(classifyCodexRouteFailure(403, "upstream_http", []byte(`{"error":{"type":"server_error","code":null,"message":"403: This request was blocked by our usage policy."}}`)))
	if d.pathEligible(a, "basispoints", "gpt-6-astra", nil) || !d.pathEligible(a, "codex", "gpt-6-astra", nil) || a.FreshDispatchUsageLimited() {
		t.Fatal("rejected path remained eligible or unrelated account capability changed")
	}
	r := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(r)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	c.Set(contextAPIKeyRow, &database.APIKeyRow{ID: 3, Limits: database.APIKeyLimits{CodexRoutePolicy: "basispoints_prefer"}})
	filter := h.withCodexRouteFilter(c, "gpt-6-astra", "gpt-6-astra", []byte(`{"input":[{"type":"reasoning","encrypted_content":"opaque"}]}`), nil)
	_, _, _, err := h.nextRetryAccountWithGuard(c.Request.Context(), "", 3, newRetryAccountExclusions(), filter, false, auth.DispatchPolicyStandard)
	if !writeSchedulerQueueError(c, err, continuousRetryProtocolResponses) || r.Code != 503 || r.Header().Get("Retry-After") == "" || !strings.Contains(r.Body.String(), "codex_route_cooldown") || !strings.Contains(r.Body.String(), "basispoints:path_cooldown") {
		t.Fatalf("temporary path failure lacks recovery guidance: %v %d %s", err, r.Code, r.Body.String())
	}
}
