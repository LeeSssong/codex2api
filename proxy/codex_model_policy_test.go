package proxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/config"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
)

func verifyBPSModelForTest(a *auth.Account, model string) {
	a.ObserveCodexPath(context.Background(), database.CodexCapability{Upstream: database.CodexPathBasispoints, Model: model, Capability: database.CapabilitySupported, Source: "synthetic_probe", ObservedAt: time.Now().Add(-time.Minute).UnixNano()})
}

func TestCodexBPSModelPolicyCatalogAndVerification(t *testing.T) {
	enableBasispointsForTest(t)
	t.Setenv("BASISPOINTS_MODELS", "")
	t.Setenv("BASISPOINTS_NATIVE_FALLBACK", "true")
	for _, model := range []string{"gpt-6-astra", "gpt-5.6-sol", "GPT-6-ASTRA", "gpt-6-astra-2026-09-25", "gpt-6-astra-pro", "gpt-6-sol"} {
		t.Run(model, func(t *testing.T) {
			d := newCodexRouteDecision(context.Background(), "client-alias", model, database.APIKeyLimits{CodexRoutePolicy: database.CodexRouteBasispointsModelsOnly}, 3)
			listed := basispointsModelAllowed(model)
			want := database.CodexPathNative
			if listed {
				want = database.CodexPathBasispoints
			}
			if len(d.Paths) != 1 || d.Preferred != want {
				t.Fatalf("wrong model route: %v", d.Paths)
			}
			a := &auth.Account{DBID: 996001, AccessToken: "test", AccountID: "workspace"}
			if d.pathEligible(a, want, model, nil) == listed {
				t.Fatal("unknown BPS capability admitted or unlisted model blocked")
			}
			verifyBPSModelForTest(a, "other-model")
			verifyBPSModelForTest(a, "")
			if listed && d.pathEligible(a, want, model, nil) {
				t.Fatal("other-model or account-wide evidence admitted")
			}
			verifyBPSModelForTest(a, model)
			if !d.pathEligible(a, want, model, nil) {
				t.Fatal("exact verified model rejected")
			}
			other := database.CodexPathBasispoints
			if listed {
				other = database.CodexPathNative
			}
			if d.pathEligible(a, other, model, nil) || d.begin(a, other, model) == nil {
				t.Fatal("alternate path escaped model policy")
			}
			if listed {
				d.CapabilityFilter = "dual_supported"
				if d.pathEligible(a, want, model, nil) {
					t.Fatal("explicit additional capability filter bypassed")
				}
			}
		})
	}
	for _, catalog := range []string{"*", "all", "gpt-6-sol"} {
		t.Setenv("BASISPOINTS_MODELS", catalog)
		d := newCodexRouteDecision(context.Background(), "alias", "gpt-6-sol", database.APIKeyLimits{CodexRoutePolicy: database.CodexRouteBasispointsModelsOnly}, 3)
		if !d.requiresVerifiedBasispoints() {
			t.Fatal("configured catalog ignored")
		}
	}
	t.Setenv("CODEX_ROUTE_POLICY", database.CodexRouteBasispointsModelsOnly)
	d := newCodexRouteDecision(context.Background(), "alias", "gpt-6-sol", database.APIKeyLimits{}, 3)
	if !d.requiresVerifiedBasispoints() || !d.routeConstrained {
		t.Fatal("inherited policy lost its constraint")
	}
}

func TestCodexBPSModelPolicyNeverFallsBackOnFailure(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		status        int
	}{
		{"HTTP403", `{"error":{"type":"server_error","code":null,"message":"403: This request was blocked by our usage policy."}}`, 403},
		{"SSE403", "data: " + `{"type":"response.failed","response":{"error":{"type":"server_error","code":null,"message":"403: This request was blocked by our usage policy."}}}` + "\n\n", 200},
		{"model access", `{"error":{"code":"basispoints_model_access_changed"}}`, 403},
		{"rate limit", `{"error":{"code":"rate_limit_exceeded"}}`, 429},
		{"quota", `{"error":{"code":"insufficient_quota"}}`, 429},
		{"authentication", `{"error":{"code":"invalid_api_key"}}`, 401},
		{"server", `{"error":{"code":"server_error"}}`, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enableBasispointsForTest(t)
			t.Setenv("BASISPOINTS_MODELS", "")
			t.Setenv("BASISPOINTS_NATIVE_FALLBACK", "true")
			a := &auth.Account{DBID: 996002, AccessToken: "test", AccountID: "workspace"}
			verifyBPSModelForTest(a, "gpt-6-astra")
			bpsCalls, nativeCalls := 0, 0
			installBasispointsTransport(t, a, func(*http.Request) (*http.Response, error) {
				bpsCalls++
				return routeTestResponse(tc.status, tc.payload), nil
			})
			native := func(context.Context, *auth.Account, []byte, string, string, string, *DeviceProfileConfig, http.Header) (*http.Response, error) {
				nativeCalls++
				return basispointsTestCompleted("unexpected-native", nil), nil
			}
			d := newCodexRouteDecision(context.Background(), "alias", "gpt-6-astra", database.APIKeyLimits{CodexRoutePolicy: database.CodexRouteBasispointsModelsOnly}, 8)
			ctx := context.WithValue(context.Background(), codexRouteKey{}, d)
			resp, err := executeCodexRoute(ctx, a, []byte(`{"model":"gpt-6-astra","input":"hello"}`), "", "", "test-key", nil, nil, false, native)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if nativeCalls != 0 || bpsCalls != 1 || d.Switched || d.FinalPath != database.CodexPathBasispoints {
				t.Fatalf("failure escaped BPS: bps=%d native=%d decision=%+v", bpsCalls, nativeCalls, d)
			}
			// A new request after an account-level model rejection still cannot use native.
			next := newCodexRouteDecision(context.Background(), "alias", "gpt-6-astra", d.limits, 8)
			if next.pathEligible(a, database.CodexPathNative, "gpt-6-astra", nil) {
				t.Fatal("failure changed catalog membership")
			}
		})
	}
}

func TestCodexBPSModelPolicyIngress(t *testing.T) {
	for _, protocol := range []string{"responses", "responses-stream", "chat", "chat-stream", "messages", "messages-stream", "compact", "websocket"} {
		t.Run(protocol, func(t *testing.T) {
			enableBasispointsForTest(t)
			t.Setenv("BASISPOINTS_MODELS", "")
			store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, CodexBasispointsEnabled: true})
			t.Cleanup(store.Stop)
			store.SetModelMapping(`{"test-alias":"gpt-6-astra"}`)
			store.SetCodexModelMapping(`{"test-alias":"gpt-6-astra"}`)
			store.SetAPIKeyAllowedGroups(9, []int64{10})
			var bpsCalls, otherCalls atomic.Int32
			for i := int64(0); i < 4; i++ {
				a := &auth.Account{DBID: 996010 + i, AccessToken: "test", AccountID: fmt.Sprint(i), PlanType: "pro", GroupIDs: []int64{10}}
				if i == 1 {
					a.GroupIDs = []int64{30}
				}
				if i == 3 {
					a.UpstreamType, a.BaseURL, a.Models = "openai_responses", "http://127.0.0.1:1", []string{"gpt-6-astra"}
					a.APIKey = "synthetic-relay-key"
				}
				if i != 0 {
					verifyBPSModelForTest(a, "gpt-6-astra")
				}
				store.AddAccount(a)
				seedContinuousRetryLocalHealth(a)
				installBasispointsTransport(t, a, func(req *http.Request) (*http.Response, error) {
					bpsCalls.Add(1)
					if a.ID() != 996012 || req.Header.Get("Chatgpt-Account-Id") != "2" {
						t.Error("selected unverified, relay or unauthorized account")
					}
					if protocol == "compact" {
						return basispointsTestCompleted("resp-compact", []any{map[string]any{"type": "compaction", "encrypted_content": "synthetic"}}), nil
					}
					return basispointsTestCompleted("resp-verified", []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "verified bps"}}}}), nil
				})
				installClaudeBoundaryTransport(t, a, func(*http.Request) (*http.Response, error) {
					otherCalls.Add(1)
					return routeTestResponse(500, `{"error":{"message":"unexpected native or relay"}}`), nil
				})
			}
			h := NewHandler(store, nil, &config.Config{}, nil)
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set(contextAPIKeyID, int64(9))
				c.Set(contextAPIKeyRow, &database.APIKeyRow{ID: 9, AllowedGroupIDs: []int64{10}, Limits: database.APIKeyLimits{CodexRoutePolicy: database.CodexRouteBasispointsModelsOnly}})
			})
			router.POST("/v1/responses", h.Responses)
			router.POST("/v1/responses/compact", h.ResponsesCompact)
			router.POST("/v1/chat/completions", h.ChatCompletions)
			router.POST("/v1/messages", h.Messages)
			router.GET("/v1/responses", h.ResponsesWebSocket)
			if protocol == "websocket" {
				server := httptest.NewServer(router)
				defer server.Close()
				conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"test-alias","input":"hello"}`)); err != nil {
					t.Fatal(err)
				}
				terminal := readResponsesWSTerminalEvent(t, conn)
				if gjson.GetBytes(terminal, "type").String() != "response.completed" {
					t.Fatalf("WS failed: %s", terminal)
				}
			} else {
				endpoint := "/v1/responses"
				input := `"input":"hello"`
				if strings.HasPrefix(protocol, "chat") || strings.HasPrefix(protocol, "messages") {
					endpoint = "/v1/chat/completions"
					if strings.HasPrefix(protocol, "messages") {
						endpoint = "/v1/messages"
					}
					input = `"messages":[{"role":"user","content":"hello"}],"max_tokens":50`
				} else if protocol == "compact" {
					endpoint += "/compact"
				}
				body := fmt.Sprintf(`{"model":"test-alias",%s,"stream":%t}`, input, strings.HasSuffix(protocol, "-stream"))
				r := httptest.NewRecorder()
				req := httptest.NewRequest("POST", endpoint, strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				router.ServeHTTP(r, req)
				if r.Code != 200 {
					t.Fatalf("ingress failed: %d %s", r.Code, r.Body.String())
				}
			}
			if bpsCalls.Load() != 1 || otherCalls.Load() != 0 {
				t.Fatalf("incorrect transports: bps=%d other=%d", bpsCalls.Load(), otherCalls.Load())
			}
		})
	}
}

func TestCodexBPSModelPolicyUnavailableFailsClosed(t *testing.T) {
	for _, scenario := range []string{"unknown", "other model", "cooldown", "unsupported", "disabled", "web search", "structured output", "only unauthorized", "only relay"} {
		t.Run(scenario, func(t *testing.T) {
			enableBasispointsForTest(t)
			t.Setenv("BASISPOINTS_MODELS", "")
			store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, CodexBasispointsEnabled: true})
			t.Cleanup(store.Stop)
			a := &auth.Account{DBID: 996020, AccessToken: "test", AccountID: "workspace", PlanType: "pro", GroupIDs: []int64{10}}
			body := `{"model":"gpt-6-astra","input":"hello","codex_route_policy":"codex_only","codex_capability_filter":"any"}`
			wantCode, wantStatus := "codex_route_no_candidates", 503
			switch scenario {
			case "other model":
				verifyBPSModelForTest(a, "gpt-5.6-sol")
			case "cooldown":
				verifyBPSModelForTest(a, "gpt-6-astra")
				a.SetCodexPathCooldown("basispoints", "gpt-6-astra", "upstream_access", time.Now(), time.Now().Add(time.Minute))
				wantCode = "codex_route_cooldown"
			case "unsupported":
				a.ObserveCodexPath(context.Background(), database.CodexCapability{Upstream: "basispoints", Model: "gpt-6-astra", Capability: "unsupported", ObservedAt: time.Now().UnixNano()})
			case "disabled":
				settings := CurrentRuntimeSettings()
				settings.CodexBasispointsEnabled = false
				ApplyRuntimeSettings(settings)
				wantCode = "codex_route_basispoints_disabled"
			case "web search", "structured output":
				verifyBPSModelForTest(a, "gpt-6-astra")
				body = `{"model":"gpt-6-astra","input":"hello","tools":[{"type":"web_search"}]}`
				if scenario == "structured output" {
					body = `{"model":"gpt-6-astra","input":"hello","text":{"format":{"type":"json_object"}}}`
				}
				wantCode, wantStatus = "codex_route_basispoints_protocol", 400
			case "only unauthorized":
				verifyBPSModelForTest(a, "gpt-6-astra")
				a.GroupIDs = []int64{30}
			case "only relay":
				a.UpstreamType, a.BaseURL, a.Models = "openai_responses", "http://127.0.0.1:1", []string{"gpt-6-astra"}
				a.APIKey = "synthetic-relay-key"
			}
			store.AddAccount(a)
			seedContinuousRetryLocalHealth(a)
			store.SetAPIKeyAllowedGroups(9, []int64{10})
			unexpected := func(*http.Request) (*http.Response, error) {
				t.Error("ineligible account reached a transport")
				return routeTestResponse(500, `{}`), nil
			}
			installBasispointsTransport(t, a, unexpected)
			installClaudeBoundaryTransport(t, a, unexpected)
			h := NewHandler(store, nil, &config.Config{}, nil)
			r := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(r)
			c.Request = httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Set(contextAPIKeyID, int64(9))
			c.Set(contextAPIKeyRow, &database.APIKeyRow{ID: 9, Limits: database.APIKeyLimits{CodexRoutePolicy: database.CodexRouteBasispointsModelsOnly}})
			h.Responses(c)
			if r.Code != wantStatus || !strings.Contains(r.Body.String(), wantCode) {
				t.Fatalf("wrong failure: %d %s; want %d %s", r.Code, r.Body.String(), wantStatus, wantCode)
			}
		})
	}
}

func TestCodexBPSModelPolicyUnlistedUsesNativeAndPreservesState(t *testing.T) {
	enableBasispointsForTest(t)
	t.Setenv("BASISPOINTS_MODELS", "")
	t.Setenv("BASISPOINTS_NATIVE_FALLBACK", "false")
	a := &auth.Account{DBID: 996021, AccessToken: "test", AccountID: "workspace"}
	ctx := context.WithValue(context.Background(), codexRouteLimitsKey{}, database.APIKeyLimits{CodexRoutePolicy: database.CodexRouteBasispointsModelsOnly})
	nativeCalls := 0
	native := func(context.Context, *auth.Account, []byte, string, string, string, *DeviceProfileConfig, http.Header) (*http.Response, error) {
		nativeCalls++
		return routeTestResponse(200, "data: "+`{"type":"response.completed","response":{"output":[]}}`+"\n\n"), nil
	}
	resp, err := executeCodexRoute(ctx, a, []byte(`{"model":"gpt-5.6-luna","input":"hello"}`), "", "", "test-key", nil, nil, false, native)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if nativeCalls != 1 {
		t.Fatal("unlisted model incorrectly requires BPS evidence")
	}
	old := ipv6StateProvider.Load()
	t.Cleanup(func() { SetIPv6StateProvider(old) })
	SetIPv6StateProvider(&IPv6StateProvider{EligibleAccounts: func(string) (bool, map[int64]bool) { return true, map[int64]bool{} }})
	_, err = executeCodexRoute(ctx, a, []byte(`{"model":"gpt-5.6-luna","input":"hello"}`), "", "", "test-key", nil, nil, false, native)
	if err == nil || nativeCalls != 1 {
		t.Fatal("native State requirements were bypassed")
	}
}

func TestCodexBPSModelPolicyRelayModelMapping(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint, model, mapping string
		wantRelay                      bool
	}{
		{"unlisted", "/v1/responses", "gpt-5.6-luna", "", true},
		{"listed", "/v1/responses", "gpt-6-astra", "", false},
		{"relay alias to listed", "/v1/responses", "relay-alias", `{"relay-alias":"gpt-6-astra"}`, false},
		{"relay alias to unlisted", "/v1/responses", "relay-alias", `{"relay-alias":"gpt-5.6-luna"}`, true},
		{"compact alias to listed", "/v1/responses/compact", "gpt-5.6-luna", `{"gpt-5.6-luna-openai-compact":"gpt-6-astra"}`, false},
		{"chat alias to listed", "/v1/chat/completions", "relay-alias", `{"relay-alias":"gpt-6-astra"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enableBasispointsForTest(t)
			t.Setenv("BASISPOINTS_MODELS", "")
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				body, _ := io.ReadAll(r.Body)
				if basispointsModelAllowed(gjson.GetBytes(body, "model").String()) {
					t.Error("BPS-listed wire model escaped through a relay")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"resp-relay","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"relay ok"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`)
			}))
			defer server.Close()
			store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 1, TestConcurrency: 1, CodexBasispointsEnabled: true})
			t.Cleanup(store.Stop)
			a := &auth.Account{DBID: 996022, UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: server.URL, APIKey: "synthetic-key", Models: []string{"gpt-6-astra", "gpt-5.6-luna"}, ModelMapping: tc.mapping}
			store.AddAccount(a)
			seedContinuousRetryLocalHealth(a)
			h := NewHandler(store, nil, &config.Config{}, nil)
			input := `"input":"hello"`
			if tc.endpoint == "/v1/chat/completions" {
				input = `"messages":[{"role":"user","content":"hello"}]`
			}
			r := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(r)
			c.Request = httptest.NewRequest("POST", tc.endpoint, strings.NewReader(fmt.Sprintf(`{"model":%q,%s}`, tc.model, input)))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Set(contextAPIKeyID, int64(9))
			c.Set(contextAPIKeyRow, &database.APIKeyRow{ID: 9, Limits: database.APIKeyLimits{CodexRoutePolicy: database.CodexRouteBasispointsModelsOnly}})
			switch tc.endpoint {
			case "/v1/responses/compact":
				h.ResponsesCompact(c)
			case "/v1/chat/completions":
				h.ChatCompletions(c)
			default:
				h.Responses(c)
			}
			if tc.wantRelay {
				if r.Code != 200 || calls.Load() != 1 {
					t.Fatalf("authorized unlisted relay failed: %d calls=%d %s", r.Code, calls.Load(), r.Body.String())
				}
			} else if r.Code != 503 || calls.Load() != 0 || !strings.Contains(r.Body.String(), "codex_route_no_candidates") {
				t.Fatalf("relay bypass: %d calls=%d %s", r.Code, calls.Load(), r.Body.String())
			}
		})
	}
}

func TestCodexBPSModelPolicyWebSocketRejectionNeverUsesNative(t *testing.T) {
	enableBasispointsForTest(t)
	t.Setenv("BASISPOINTS_NATIVE_FALLBACK", "true")
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 1, TestConcurrency: 1, CodexBasispointsEnabled: true})
	t.Cleanup(store.Stop)
	a := &auth.Account{DBID: 996023, AccessToken: "test", AccountID: "workspace", PlanType: "pro"}
	verifyBPSModelForTest(a, "gpt-6-astra")
	store.AddAccount(a)
	seedContinuousRetryLocalHealth(a)
	var bpsCalls, nativeCalls atomic.Int32
	installBasispointsTransport(t, a, func(*http.Request) (*http.Response, error) {
		bpsCalls.Add(1)
		return routeTestResponse(403, `{"error":{"type":"server_error","code":null,"message":"403: This request was blocked by our usage policy."}}`), nil
	})
	installClaudeBoundaryTransport(t, a, func(*http.Request) (*http.Response, error) {
		nativeCalls.Add(1)
		return basispointsTestCompleted("unexpected-native", nil), nil
	})
	h := NewHandler(store, nil, &config.Config{}, nil)
	router := gin.New()
	router.GET("/v1/responses", func(c *gin.Context) {
		c.Set(contextAPIKeyID, int64(9))
		c.Set(contextAPIKeyRow, &database.APIKeyRow{ID: 9, Limits: database.APIKeyLimits{CodexRoutePolicy: database.CodexRouteBasispointsModelsOnly}})
		h.ResponsesWebSocket(c)
	})
	server := httptest.NewServer(router)
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"gpt-6-astra","input":"hello"}`)); err != nil {
		t.Fatal(err)
	}
	terminal := readResponsesWSTerminalEvent(t, conn)
	if gjson.GetBytes(terminal, "type").String() == "response.completed" || bpsCalls.Load() != 1 || nativeCalls.Load() != 0 {
		t.Fatalf("WS rejection escaped BPS: bps=%d native=%d frame=%s", bpsCalls.Load(), nativeCalls.Load(), terminal)
	}
}
