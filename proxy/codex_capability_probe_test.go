package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/tidwall/gjson"
)

func codexProbeTestAccount(t *testing.T) *auth.Account {
	t.Helper()
	enableBasispointsForTest(t)
	t.Setenv("BASISPOINTS_MODELS", "gpt-6-astra")
	return &auth.Account{DBID: 953020, AccessToken: "synthetic-token", AccountID: "synthetic-workspace", CredentialGeneration: 1}
}

func codexProbeTestText(text string) *http.Response {
	return basispointsTestCompleted("resp_probe", []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}}}})
}

func codexProbeTestTool(nonce string) *http.Response {
	envelope, _ := json.Marshal(map[string]any{"name": codexProbeTool, "arguments": map[string]any{"nonce": nonce}})
	return basispointsTestCompleted("resp_tool_probe", []any{map[string]any{"type": "function_call", "id": "fc_probe", "call_id": "call_probe", "name": "run_officejs", "arguments": map[string]any{"code": string(envelope), "summary": "echo test"}, "status": "completed"}})
}

func codexProbeTestNonce(t *testing.T, body []byte) string {
	t.Helper()
	nonce := regexp.MustCompile(`c2a_probe_[a-f0-9]+`).FindString(string(body))
	if nonce == "" {
		t.Fatal("missing diagnostic nonce")
	}
	return nonce
}

func TestCodexCapabilityProbePinnedRetestAndToolRoundtrip(t *testing.T) {
	for _, level := range []string{"basic", "tools"} {
		t.Run(level, func(t *testing.T) {
			a := codexProbeTestAccount(t)
			const model = "gpt-unknown-exact-2026-09-25"
			t.Setenv("BASISPOINTS_MODELS", model)
			a.ObserveCodexPath(context.Background(), database.CodexCapability{Upstream: "basispoints", Model: model, Capability: "unsupported", ObservedAt: time.Now().Add(-time.Hour).UnixNano()})
			calls := 0
			installBasispointsTransport(t, a, func(req *http.Request) (*http.Response, error) {
				calls++
				body, _ := io.ReadAll(req.Body)
				if req.URL.Host != "bps.openai.com" || gjson.GetBytes(body, "model").String() != model || req.Header.Get("Chatgpt-Account-Id") != "synthetic-workspace" {
					t.Fatalf("unpinned request: %s %s", req.URL, body)
				}
				nonce := codexProbeTestNonce(t, body)
				if calls == 2 {
					return codexProbeTestTool(nonce), nil
				}
				if calls == 3 && !strings.Contains(string(body), `function_call_output`) {
					t.Fatalf("tool result missing: %s", body)
				}
				return codexProbeTestText(nonce), nil
			})
			ctx := context.WithValue(context.Background(), codexRouteLimitsKey{}, database.APIKeyLimits{CodexRoutePolicy: "codex_only", CodexCapabilityFilter: "dual_supported"})
			result := ProbeCodexCapability(ctx, a, CodexCapabilityProbeOptions{Model: model, Level: level})
			wantCalls := 1
			if level == "tools" {
				wantCalls = 3
			}
			if result.Outcome != "supported" || result.BasicOutcome != "supported" || result.Capability != "supported" || result.Attempts != wantCalls || calls != wantCalls {
				t.Fatalf("probe: %+v calls=%d", result, calls)
			}
			if level == "tools" && result.ToolsOutcome != "supported" {
				t.Fatalf("tools: %+v", result)
			}
			if a.CodexPathSnapshot("basispoints", model, time.Now()).Capability != "supported" || a.CodexPathSnapshot("codex", model, time.Now()).Capability != "unknown" {
				t.Fatal("evidence crossed upstreams")
			}
			saved, err := a.GetCodexCapabilityProbeResults(ctx)
			if err != nil || len(saved) != 1 || saved[0].Outcome != "supported" {
				t.Fatalf("saved=%+v err=%v", saved, err)
			}
		})
	}
}

func TestCodexCapabilityProbeGlobalGuardsMakeNoRequests(t *testing.T) {
	for _, guard := range []string{"disabled", "model_allowlist"} {
		t.Run(guard, func(t *testing.T) {
			a := codexProbeTestAccount(t)
			if guard == "disabled" {
				settings := CurrentRuntimeSettings()
				settings.CodexBasispointsEnabled = false
				ApplyRuntimeSettings(settings)
			} else {
				t.Setenv("BASISPOINTS_MODELS", "different-model")
			}
			calls := 0
			installBasispointsTransport(t, a, func(*http.Request) (*http.Response, error) {
				calls++
				return codexProbeTestText("unexpected"), nil
			})
			r := ProbeCodexCapability(context.Background(), a, CodexCapabilityProbeOptions{Model: "gpt-6-astra", Level: "tools"})
			if r.Outcome != "blocked" || r.ErrorCode != "basispoints_disabled_or_model_not_allowed" || r.Attempts != 0 || calls != 0 {
				t.Fatalf("global gate bypassed: %+v calls=%d", r, calls)
			}
		})
	}
}

func TestCodexCapabilityProbeRechecksPolicyBetweenToolSteps(t *testing.T) {
	for _, changeAfter := range []int{1, 2} {
		for _, change := range []string{"persisted_disable", "cooldown", "recovering", "global_disable", "model_allowlist"} {
			t.Run(change+"/after_"+string(rune('0'+changeAfter)), func(t *testing.T) {
				a := codexProbeTestAccount(t)
				ctx := context.Background()
				var db *database.DB
				if change == "persisted_disable" {
					var err error
					db, err = database.New("sqlite", filepath.Join(t.TempDir(), "probe-policy.db"))
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = db.Close() })
					a.DBID, err = db.InsertAccount(ctx, "probe-policy", "synthetic-refresh", "")
					if err != nil {
						t.Fatal(err)
					}
					store := auth.NewStore(db, nil, nil)
					t.Cleanup(store.Stop)
					store.AddAccount(a)
				}
				calls := 0
				installBasispointsTransport(t, a, func(req *http.Request) (*http.Response, error) {
					calls++
					body, _ := io.ReadAll(req.Body)
					if calls == changeAfter {
						switch change {
						case "persisted_disable":
							if err := db.SetCodexPathAllowed(ctx, a.ID(), "basispoints", false); err != nil {
								t.Fatal(err)
							}
						case "cooldown":
							now := time.Now()
							a.SetCodexPathCooldown("basispoints", "gpt-6-astra", "access", now, now.Add(time.Minute))
						case "recovering":
							now := time.Now()
							a.SetCodexPathCooldown("basispoints", "gpt-6-astra", "access", now, now.Add(-time.Second))
							release, ok := a.BeginCodexPath("basispoints", "gpt-6-astra", now)
							if !ok {
								t.Fatal("could not start recovery fixture")
							}
							t.Cleanup(release)
						case "global_disable":
							settings := CurrentRuntimeSettings()
							settings.CodexBasispointsEnabled = false
							ApplyRuntimeSettings(settings)
						case "model_allowlist":
							t.Setenv("BASISPOINTS_MODELS", "different-model")
						}
					}
					nonce := codexProbeTestNonce(t, body)
					if calls == 2 {
						return codexProbeTestTool(nonce), nil
					}
					return codexProbeTestText(nonce), nil
				})
				r := ProbeCodexCapability(ctx, a, CodexCapabilityProbeOptions{Model: "gpt-6-astra", Level: "tools"})
				if r.Outcome != "blocked" || r.BasicOutcome != "supported" || r.ToolsOutcome != "blocked" || r.Attempts != changeAfter || calls != changeAfter {
					t.Fatalf("changed policy bypassed: %+v calls=%d", r, calls)
				}
				if change == "persisted_disable" && a.CodexPathSnapshot("basispoints", r.Model, time.Now()).Allowed {
					t.Fatal("probe result reenabled the path")
				}
			})
		}
	}
}

func TestCodexCapabilityProbeToolFailureUpdatesOnlyAuthoritativeEvidence(t *testing.T) {
	for _, rejectStep := range []int{2, 3} {
		for _, tc := range []struct {
			name                      string
			status                    int
			body, outcome, capability string
		}{
			{"model_http", 403, `{"error":{"code":"model_access_denied"}}`, "unsupported", "unsupported"},
			{"model_sse", 200, "data: " + `{"type":"response.failed","response":{"status":"failed","error":{"code":"basispoints_model_access_changed"}}}` + "\n\n", "unsupported", "unsupported"},
			{"network", 503, `{"error":{"message":"temporary"}}`, "network_error", "supported"},
			{"protocol", 400, `{"error":{"code":"basispoints_protocol_error"}}`, "protocol_error", "supported"},
		} {
			t.Run(tc.name+"/step_"+string(rune('0'+rejectStep)), func(t *testing.T) {
				a := codexProbeTestAccount(t)
				calls := 0
				installBasispointsTransport(t, a, func(req *http.Request) (*http.Response, error) {
					calls++
					if calls == rejectStep {
						return routeTestResponse(tc.status, tc.body), nil
					}
					body, _ := io.ReadAll(req.Body)
					nonce := codexProbeTestNonce(t, body)
					if calls == 2 {
						return codexProbeTestTool(nonce), nil
					}
					return codexProbeTestText(nonce), nil
				})
				r := ProbeCodexCapability(context.Background(), a, CodexCapabilityProbeOptions{Model: "gpt-6-astra", Level: "tools"})
				if r.BasicOutcome != "supported" || r.ToolsOutcome != tc.outcome || r.Outcome != tc.outcome || r.Capability != tc.capability || r.Attempts != rejectStep || calls != rejectStep {
					t.Fatalf("tool rejection: %+v calls=%d", r, calls)
				}
				if got := a.CodexPathSnapshot("basispoints", r.Model, time.Now()).Capability; got != tc.capability {
					t.Fatalf("evidence=%s, want %s", got, tc.capability)
				}
			})
		}
	}
}

func TestCodexCapabilityProbeFailureClassificationAndNoFallback(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		status                    int
		body, outcome, capability string
	}{
		{"ambiguous", 403, `{"error":{"code":null,"type":"server_error","message":"403: This request was blocked by our usage policy."}}`, "blocked", "unknown"},
		{"model", 403, `{"error":{"code":"model_access_denied"}}`, "unsupported", "unsupported"},
		{"workspace", 403, `{"error":{"code":"deactivated_workspace"}}`, "workspace_deactivated", "unknown"},
		{"auth", 401, `{"error":{"code":"invalid_api_key"}}`, "unauthorized", "unknown"},
		{"billing", 402, `{"error":{}}`, "blocked", "unknown"},
		{"limit", 429, `{"error":{}}`, "rate_limited", "unknown"},
		{"html", 403, `<html>secret-token</html>`, "network_error", "unknown"},
		{"server", 503, `{"error":{"message":"secret-token"}}`, "network_error", "unknown"},
		{"incomplete", 200, "data: " + `{"type":"response.incomplete","response":{"status":"incomplete","output":[]}}` + "\n\n", "protocol_error", "unknown"},
		{"failed_completed", 200, "data: " + `{"type":"response.completed","response":{"status":"failed","output":[]}}` + "\n\n", "protocol_error", "unknown"},
		{"empty_success", 200, "data: " + `{"type":"response.completed","response":{"status":"completed","output":[]}}` + "\n\n", "protocol_error", "unknown"},
		{"eof", 200, "data: " + `{"type":"response.created","response":{"status":"in_progress","output":[]}}` + "\n\n", "network_error", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enableBasispointsForTest(t)
			t.Setenv("BASISPOINTS_NATIVE_FALLBACK", "true")
			t.Setenv("CODEX_ROUTE_POLICY", "basispoints_prefer")
			a := codexProbeTestAccount(t)
			calls := 0
			installBasispointsTransport(t, a, func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Host != "bps.openai.com" {
					t.Fatal("native attempt")
				}
				return routeTestResponse(tc.status, tc.body), nil
			})
			result := ProbeCodexCapability(context.Background(), a, CodexCapabilityProbeOptions{Model: "gpt-6-astra"})
			if result.Outcome != tc.outcome || result.Capability != tc.capability || result.HTTPStatus != tc.status || calls != 1 || result.Attempts != 1 {
				t.Fatalf("unexpected result %+v calls=%d", result, calls)
			}
			if strings.Contains(result.Message, "secret-token") {
				t.Fatal("raw error leaked")
			}
			if a.CodexPathSnapshot("codex", "gpt-6-astra", time.Now()).Capability != "unknown" {
				t.Fatal("native evidence created")
			}
		})
	}
}

func TestCodexCapabilityProbePreservesBasicOnToolFailure(t *testing.T) {
	a := codexProbeTestAccount(t)
	calls := 0
	installBasispointsTransport(t, a, func(req *http.Request) (*http.Response, error) {
		calls++
		body, _ := io.ReadAll(req.Body)
		if calls == 1 {
			return codexProbeTestText(codexProbeTestNonce(t, body)), nil
		}
		return codexProbeTestText("I cannot call that tool"), nil
	})
	r := ProbeCodexCapability(context.Background(), a, CodexCapabilityProbeOptions{Model: "gpt-6-astra", Level: "tools"})
	if r.BasicOutcome != "supported" || r.ToolsOutcome != "protocol_error" || r.Capability != "supported" || r.Attempts != 2 {
		t.Fatalf("result %+v", r)
	}
	if a.CodexPathSnapshot("basispoints", r.Model, time.Now()).Capability != "supported" {
		t.Fatal("basic evidence discarded")
	}
}

func TestCodexCapabilityProbeRejectsInvalidToolMarkers(t *testing.T) {
	for _, marker := range []string{"", `,"encrypted_function_args":null`, `,"encrypted_function_args":"[]"`, `,"encrypted_function_args":["nonce"]`} {
		response := gjson.Parse(`{"output":[{"type":"function_call","name":"codex2api_probe_echo","call_id":"call","arguments":"{\"nonce\":\"expected\"}"` + marker + `}]}`)
		if _, ok := codexProbeValidatedCall(response, "expected"); ok {
			t.Fatalf("invalid marker accepted: %s", marker)
		}
	}
}

func TestCodexCapabilityProbeAdministrativeGuardsAndCredentialChange(t *testing.T) {
	a := codexProbeTestAccount(t)
	calls := 0
	installBasispointsTransport(t, a, func(req *http.Request) (*http.Response, error) {
		calls++
		body, _ := io.ReadAll(req.Body)
		a.Mu().Lock()
		a.CredentialGeneration++
		a.Mu().Unlock()
		return codexProbeTestText(codexProbeTestNonce(t, body)), nil
	})
	atomic.StoreInt32(&a.DispatchPaused, 1)
	r := ProbeCodexCapability(context.Background(), a, CodexCapabilityProbeOptions{Model: "gpt-6-astra"})
	if r.Outcome != "blocked" || calls != 0 {
		t.Fatalf("admin disable bypassed: %+v", r)
	}
	atomic.StoreInt32(&a.DispatchPaused, 0)
	r = ProbeCodexCapability(context.Background(), a, CodexCapabilityProbeOptions{Model: "gpt-6-astra"})
	if r.Outcome != "skipped" || r.ErrorCode != "credentials_changed" || a.CodexPathSnapshot("basispoints", r.Model, time.Now()).Capability != "unknown" {
		t.Fatalf("old credential result applied: %+v", r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r = ProbeCodexCapability(ctx, a, CodexCapabilityProbeOptions{Model: "gpt-6-astra"})
	if r.Outcome != "canceled" || calls != 1 {
		t.Fatalf("canceled probe used upstream: %+v", r)
	}
}

func TestCodexCapabilityProbeDuplicateDoesNotSupersedeActiveTest(t *testing.T) {
	a := codexProbeTestAccount(t)
	entered, proceed := make(chan struct{}), make(chan struct{})
	installBasispointsTransport(t, a, func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		close(entered)
		<-proceed
		return codexProbeTestText(codexProbeTestNonce(t, body)), nil
	})
	finished := make(chan CodexProbeResult, 1)
	go func() {
		finished <- ProbeCodexCapability(context.Background(), a, CodexCapabilityProbeOptions{Model: "gpt-6-astra"})
	}()
	<-entered
	duplicate := ProbeCodexCapability(context.Background(), a, CodexCapabilityProbeOptions{Model: "gpt-6-astra"})
	close(proceed)
	first := <-finished
	if duplicate.ErrorCode != "probe_in_progress" || first.Outcome != "supported" {
		t.Fatalf("duplicate=%+v first=%+v", duplicate, first)
	}
	saved, err := a.GetCodexCapabilityProbeResults(context.Background())
	if err != nil || len(saved) != 1 || saved[0].Outcome != "supported" {
		t.Fatalf("active result lost: %+v %v", saved, err)
	}
}

func TestCodexRouteObserverRejectsFalseCompleted(t *testing.T) {
	for _, data := range []string{
		`{"type":"response.completed","response":{"status":"failed","output":[]}}`,
		`{"type":"response.completed","response":{"status":"incomplete","output":[]}}`,
		`{"type":"response.completed","response":{"status":"completed","error":{"code":"failure"},"output":[]}}`,
		`{"type":"response.completed","response":{"status":"completed","incomplete_details":{"reason":"limit"},"output":[]}}`,
		`{"type":"response.failed","response":{"status":"failed"}}` + "\n\ndata: " + `{"type":"response.completed","response":{"status":"completed","output":[]}}`,
	} {
		a := codexProbeTestAccount(t)
		d := &CodexRouteDecision{client: context.Background()}
		attempt := &codexRouteAttemptState{account: a, decision: d, path: "basispoints", model: "m", started: time.Now(), release: func() {}}
		body := observeCodexRouteBody(io.NopCloser(strings.NewReader("data: "+data+"\n\n")), attempt, "text/event-stream")
		_, _ = io.Copy(io.Discard, body)
		_ = body.Close()
		if a.CodexPathSnapshot("basispoints", "m", time.Now()).Capability != "unknown" {
			t.Fatalf("false completed accepted: %s", data)
		}
	}
}
