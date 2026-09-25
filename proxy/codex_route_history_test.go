package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

func TestCodexNonReplayableFailureKeepsOriginalUpstream(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, input := range []string{
			`[{"type":"reasoning","encrypted_content":"opaque"}]`,
			`[{"type":"compaction","encrypted_content":"opaque"}]`,
			`[{"type":"item_reference","id":"item-1"}]`,
		} {
			t.Run(input+map[bool]string{true: "SSE", false: "HTTP"}[stream], func(t *testing.T) {
				enableBasispointsForTest(t)
				a := &auth.Account{DBID: 98701, AccessToken: "token", AccountID: "workspace"}
				bpsCalls, nativeCalls := 0, 0
				installBasispointsTransport(t, a, func(*http.Request) (*http.Response, error) {
					bpsCalls++
					failure := `{"error":{"type":"server_error","code":null,"message":"403: This request was blocked by our usage policy."}}`
					if stream {
						return routeTestResponse(200, "data: "+`{"type":"response.failed","response":`+failure+"}\n\n"), nil
					}
					return routeTestResponse(403, failure), nil
				})
				d := newCodexRouteDecision(context.Background(), "gpt-6-astra", "gpt-6-astra", database.APIKeyLimits{CodexRoutePolicy: "basispoints_prefer"}, 4)
				ctx := context.WithValue(context.Background(), codexRouteKey{}, d)
				native := func(context.Context, *auth.Account, []byte, string, string, string, *DeviceProfileConfig, http.Header) (*http.Response, error) {
					nativeCalls++
					return nil, nil
				}
				resp, err := executeCodexRoute(ctx, a, []byte(`{"model":"gpt-6-astra","input":`+input+`}`), "", "", "", nil, nil, false, native)
				// An unsupported bridge input may fail preparation; it still must
				// never issue a native request or turn a received 403 into a 503.
				if err != nil && bpsCalls != 0 {
					t.Fatal(err)
				}
				if resp != nil {
					body, readErr := io.ReadAll(resp.Body)
					resp.Body.Close()
					if readErr != nil || !strings.Contains(string(body), codexUsageRejectedCode) {
						t.Fatalf("lost upstream failure: %s, %v", body, readErr)
					}
					if !stream && resp.StatusCode != 403 {
						t.Fatalf("status = %d", resp.StatusCode)
					}
				}
				if nativeCalls != 0 || d.Switched || !d.NoSwitch || d.pinnedPath != "basispoints" {
					t.Fatalf("unsafe route: native=%d decision=%+v", nativeCalls, d)
				}
			})
		}
	}
}

func TestCodexHistoryLocksBeforeAccountSelection(t *testing.T) {
	enableBasispointsForTest(t)
	a := &auth.Account{DBID: 98702, AccessToken: "token", AccountID: "workspace"}
	a.SetCodexPathCooldown("basispoints", "gpt-6-astra", "upstream_access", time.Now(), time.Now().Add(time.Minute))
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 1, TestConcurrency: 1})
	t.Cleanup(store.Stop)
	h := &Handler{store: store}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	c.Set(contextAPIKeyRow, &database.APIKeyRow{Limits: database.APIKeyLimits{CodexRoutePolicy: "basispoints_prefer"}})
	filter := h.withCodexRouteFilter(c, "gpt-6-astra", "gpt-6-astra", []byte(`{"input":[{"type":"reasoning","encrypted_content":"opaque"}]}`), nil)
	if filter(a) {
		t.Fatal("native availability bypassed the history pin")
	}
}

func TestCodexHistoryProvenancePinsActualPathAndRespectsKeyPolicy(t *testing.T) {
	enableBasispointsForTest(t)
	cached := cache.NewMemory(1)
	t.Cleanup(func() { cached.Close() })
	a := &auth.Account{DBID: 98703}
	origin := newCodexRouteDecision(context.Background(), "gpt-6-astra", "gpt-6-astra", database.APIKeyLimits{CodexRoutePolicy: "basispoints_prefer"}, 3)
	origin.historyCache, origin.historyOwner = cached, "key:1"
	attempt := &codexRouteAttemptState{decision: origin, account: a, path: "codex", release: func() {}}
	payload := `{"type":"response.completed","response":{"id":"resp-native","status":"completed","output":[{"type":"reasoning","encrypted_content":"from-native"}]}}`
	observed := observeCodexRouteBody(io.NopCloser(strings.NewReader("data: "+payload+"\n\n")), attempt, "text/event-stream")
	if _, err := io.Copy(io.Discard, observed); err != nil {
		t.Fatal(err)
	}
	observed.Close()
	for _, tc := range []struct{ owner, policy, body, path, errorCode string }{
		{"key:1", "basispoints_prefer", `{"input":[{"type":"reasoning","encrypted_content":"from-native"}]}`, "codex", ""},
		{"key:1", "basispoints_prefer", `{"previous_response_id":"resp-native"}`, "codex", ""},
		{"key:2", "basispoints_prefer", `{"previous_response_id":"resp-native"}`, "basispoints", ""},
		{"key:1", "basispoints_only", `{"previous_response_id":"resp-native"}`, "basispoints", "codex_route_history_policy_conflict"},
		{"key:1", "basispoints_models_only", `{"previous_response_id":"resp-native"}`, "basispoints", "codex_route_history_policy_conflict"},
	} {
		t.Run(tc.owner+tc.policy+tc.body, func(t *testing.T) {
			d := newCodexRouteDecision(context.Background(), "gpt-6-astra", "gpt-6-astra", database.APIKeyLimits{CodexRoutePolicy: tc.policy}, 3)
			d.historyCache, d.historyOwner = cached, tc.owner
			d.bindHistory([]byte(tc.body))
			if tc.errorCode != "" {
				if d.historyError == nil || d.historyError.Code != tc.errorCode {
					t.Fatalf("error = %v", d.historyError)
				}
			} else if d.historyError != nil || d.pinnedPath != tc.path || !d.NoSwitch {
				t.Fatalf("wrong history route: %+v", d)
			}
		})
	}
	// A single request containing opaque state from both paths is rejected.
	raw, _ := json.Marshal("basispoints")
	keys := codexHistoryKeys([]byte(`{"input":[{"type":"reasoning","encrypted_content":"from-bps"}]}`), false)
	if err := cached.SetRuntime(context.Background(), codexHistoryNamespace, "key:1:"+keys[0], raw, time.Hour); err != nil {
		t.Fatal(err)
	}
	origin.bindHistory([]byte(`{"input":[{"type":"reasoning","encrypted_content":"from-native"},{"type":"reasoning","encrypted_content":"from-bps"}]}`))
	if origin.historyError == nil || origin.historyError.Code != "codex_route_history_conflict" {
		t.Fatalf("mixed history = %v", origin.historyError)
	}
}

func TestCodexHistoryChecksOpaqueAgentAndToolPairs(t *testing.T) {
	for _, body := range []string{
		`{"input":[{"type":"message","content":[{"type":"encrypted_content","encrypted_content":"agent-state"}]}]}`,
		`{"input":[{"type":"context_compaction"}]}`,
		`{"input":[{"type":"function_call_output","call_id":"missing","output":"ok"}]}`,
		`{"input":[{"type":"function_call","call_id":"a"},{"type":"function_call_output","call_id":"a"},{"type":"function_call","call_id":"a"},{"type":"function_call_output","call_id":"a"}]}`,
	} {
		if _, err := safeCodexSwitchBody([]byte(body)); err == nil {
			t.Fatalf("unsafe history accepted: %s", body)
		}
	}
}

func TestCodexHistoryPersistsActualFallbackAcrossHTTPRequests(t *testing.T) {
	enableBasispointsForTest(t)
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, CodexBasispointsEnabled: true})
	t.Cleanup(store.Stop)
	a := &auth.Account{DBID: 98715, AccessToken: "token", AccountID: "workspace", PlanType: "pro"}
	store.AddAccount(a)
	h := NewHandler(store, nil, &config.Config{}, nil)
	cached := cache.NewMemory(1)
	t.Cleanup(func() { cached.Close() })
	h.SetRuntimeCache(cached)
	bpsCalls, nativeCalls := 0, 0
	installBasispointsTransport(t, a, func(*http.Request) (*http.Response, error) {
		bpsCalls++
		return routeTestResponse(403, `{"error":{"type":"server_error","code":null,"message":"403: This request was blocked by our usage policy."}}`), nil
	})
	installClaudeBoundaryTransport(t, a, func(*http.Request) (*http.Response, error) {
		nativeCalls++
		return routeTestResponse(200, "data: "+`{"type":"response.completed","response":{"id":"native-success","status":"completed","output":[{"type":"reasoning","encrypted_content":"gAAAAA_native_history"},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`+"\n\n"), nil
	})
	run := func(input string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(r)
		c.Request = httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"gpt-6-astra","stream":false,"input":`+input+`}`))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set(contextAPIKeyID, int64(5))
		c.Set(contextAPIKeyRow, &database.APIKeyRow{ID: 5, Limits: database.APIKeyLimits{CodexRoutePolicy: "basispoints_prefer"}})
		h.Responses(c)
		return r
	}
	if r := run(`"hello"`); r.Code != 200 || bpsCalls != 1 || nativeCalls != 1 {
		t.Fatalf("initial fallback: %d %s calls=%d/%d", r.Code, r.Body.String(), bpsCalls, nativeCalls)
	}
	if r := run(`[{"type":"reasoning","encrypted_content":"gAAAAA_native_history"},{"role":"user","content":"continue"}]`); r.Code != 200 || bpsCalls != 1 || nativeCalls != 2 {
		t.Fatalf("continuation changed upstream: %d %s calls=%d/%d", r.Code, r.Body.String(), bpsCalls, nativeCalls)
	}
	a.SetCodexPathCooldown("codex", "gpt-6-astra", "upstream_access", time.Now().Add(time.Second), time.Now().Add(time.Hour))
	if r := run(`[{"type":"reasoning","encrypted_content":"gAAAAA_native_history"}]`); r.Code != 503 || bpsCalls != 1 || nativeCalls != 2 {
		t.Fatalf("unavailable pinned route crossed: %d %s calls=%d/%d", r.Code, r.Body.String(), bpsCalls, nativeCalls)
	}
}

func TestCodexLargeOpaqueStateAndPortableToolPairsKeepProvenance(t *testing.T) {
	enableBasispointsForTest(t)
	cached := cache.NewMemory(1)
	t.Cleanup(func() { cached.Close() })
	d := newCodexRouteDecision(context.Background(), "gpt-6-astra", "gpt-6-astra", database.APIKeyLimits{CodexRoutePolicy: "basispoints_prefer"}, 3)
	d.historyCache, d.historyOwner = cached, "key:8"
	state := strings.Repeat("opaque", 20000)
	attempt := &codexRouteAttemptState{decision: d, account: &auth.Account{DBID: 98716}, path: "codex", release: func() {}}
	observer := observeCodexRouteBody(io.NopCloser(strings.NewReader(`{"object":"response.compaction","output":[{"type":"compaction","encrypted_content":"`+state+`"}]}`)), attempt, "application/json")
	if _, err := io.Copy(io.Discard, observer); err != nil {
		t.Fatal(err)
	}
	observer.Close()
	bps := &codexRouteAttemptState{decision: d, account: attempt.account, path: "basispoints"}
	bps.recordHistoryRoute([]byte(`{"type":"response.output_item.done","item":{"type":"function_call","call_id":"old-call"}}`), map[string]bool{})
	d.bindHistory([]byte(`{"input":[{"type":"function_call","call_id":"old-call","arguments":"{}"},{"type":"function_call_output","call_id":"old-call","output":"ok"},{"type":"compaction","encrypted_content":"` + state + `"}]}`))
	if d.historyError != nil || d.pinnedPath != "codex" {
		t.Fatalf("large state or portable pair lost correct route: %+v", d)
	}
}

func TestCodexWebSocketHistoryPinAndQuotaClassification(t *testing.T) {
	for _, exhausted := range []bool{false, true} {
		t.Run(map[bool]string{false: "pinned", true: "quota"}[exhausted], func(t *testing.T) {
			enableBasispointsForTest(t)
			store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, CodexBasispointsEnabled: true})
			t.Cleanup(store.Stop)
			a := &auth.Account{DBID: 98717, AccessToken: "token", AccountID: "workspace", PlanType: "pro"}
			store.AddAccount(a)
			cached := cache.NewMemory(1)
			t.Cleanup(func() { cached.Close() })
			h := NewHandler(store, nil, &config.Config{}, nil)
			h.SetRuntimeCache(cached)
			d := newCodexRouteDecision(context.Background(), "gpt-6-astra", "gpt-6-astra", database.APIKeyLimits{}, 3)
			d.historyCache, d.historyOwner = cached, "key:9"
			(&codexRouteAttemptState{decision: d, account: a, path: "codex"}).recordHistoryRoute([]byte(`{"type":"response.output_item.done","item":{"type":"compaction","encrypted_content":"ws-opaque"}}`), map[string]bool{})
			if err := h.recordCompactionProvenance(context.Background(), a, "ws-opaque"); err != nil {
				t.Fatal(err)
			}
			if exhausted {
				store.MarkResponsesRateLimited(a, time.Hour)
			}
			var bpsCalls, nativeCalls atomic.Int32
			installBasispointsTransport(t, a, func(*http.Request) (*http.Response, error) {
				bpsCalls.Add(1)
				return routeTestResponse(403, `{"error":{"code":"basispoints_model_access_changed"}}`), nil
			})
			installClaudeBoundaryTransport(t, a, func(*http.Request) (*http.Response, error) {
				nativeCalls.Add(1)
				return routeTestResponse(200, "data: "+`{"type":"response.completed","response":{"id":"ws-done","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`+"\n\n"), nil
			})
			router := gin.New()
			router.GET("/v1/responses", func(c *gin.Context) {
				c.Set(contextAPIKeyID, int64(9))
				c.Set(contextAPIKeyRow, &database.APIKeyRow{ID: 9, Limits: database.APIKeyLimits{CodexRoutePolicy: "basispoints_prefer"}})
				h.ResponsesWebSocket(c)
			})
			server := httptest.NewServer(router)
			defer server.Close()
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"gpt-6-astra","input":[{"type":"compaction","encrypted_content":"ws-opaque"},{"role":"user","content":"continue"}]}`)); err != nil {
				t.Fatal(err)
			}
			for {
				_, payload, err := conn.ReadMessage()
				if err != nil {
					t.Fatal(err)
				}
				kind := gjson.GetBytes(payload, "type").String()
				if exhausted {
					if kind != "error" || gjson.GetBytes(payload, "error.type").String() != "rate_limit_error" {
						t.Fatalf("quota hidden by provenance error: %s", payload)
					}
					break
				}
				if kind == "error" || kind == "response.failed" {
					t.Fatalf("WS failed: %s", payload)
				}
				if kind == "response.completed" {
					break
				}
			}
			wantNative := int32(1)
			if exhausted {
				wantNative = 0
			}
			if bpsCalls.Load() != 0 || nativeCalls.Load() != wantNative {
				t.Fatalf("WS calls bps=%d native=%d", bpsCalls.Load(), nativeCalls.Load())
			}
		})
	}
}
