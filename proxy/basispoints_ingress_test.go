package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
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

func TestBasispointsRecognizesExplicitClientSessionHeaders(t *testing.T) {
	enableBasispointsForTest(t)
	account := &auth.Account{DBID: 91237, AccountID: "workspace", AccessToken: "test-token"}
	var bodies [][]byte
	installBasispointsTransport(t, account, func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, body)
		return basispointsTestCompleted("resp_session", nil), nil
	})
	for _, header := range []string{"Session-Id", "Session_id", "Conversation-Id", "Conversation_id", "X-Session-Id", "X-Session-Affinity", "Idempotency-Key"} {
		headers := make(http.Header)
		headers.Set(header, "stable-client-session")
		resp, err := ExecuteRequest(context.Background(), account, []byte(`{"model":"gpt-6-astra","input":"synthetic","prompt_cache_key":"lower-priority-body-session"}`), "rotating-native-"+header, "", "client-key", nil, headers)
		if err != nil {
			t.Fatal(err)
		}
		_, readErr := io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
	}
	for _, body := range bodies {
		for _, field := range []string{"prompt_cache_key", "metadata.task_id"} {
			got := gjson.GetBytes(body, field).String()
			if got == "" || got != gjson.GetBytes(bodies[0], field).String() {
				t.Fatalf("equivalent explicit session headers changed %s", field)
			}
		}
		if bytes.Contains(body, []byte("stable-client-session")) || bytes.Contains(body, []byte("lower-priority-body-session")) {
			t.Fatal("client session identity must be scoped and hashed before going upstream")
		}
	}
}

func TestBasispointsWebSocketIngressToolRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		storeResponse bool
		writePolicy   string
	}{
		{true, database.ResponseCacheWritePolicyAlways},
		{false, database.ResponseCacheWritePolicyAlways},
		{true, database.ResponseCacheWritePolicyOnDemand},
		{false, database.ResponseCacheWritePolicyOnDemand},
	} {
		storeResponse := tc.storeResponse
		name := "store_false"
		if storeResponse {
			name = "store_true"
		}
		t.Run(name+"/"+tc.writePolicy, func(t *testing.T) {
			enableBasispointsForTest(t)
			cacheConfig := defaultResponseCacheConfig()
			cacheConfig.writePolicy = tc.writePolicy
			resetResponseCacheStateForTest(cacheConfig)
			t.Cleanup(resetResponseCacheForTest)
			gin.SetMode(gin.TestMode)
			store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-6-astra", CodexBasispointsEnabled: true})
			t.Cleanup(store.Stop)
			account := &auth.Account{DBID: 91248, AccountID: "synthetic-workspace", AccessToken: "synthetic-token", PlanType: "pro"}
			store.AddAccount(account)
			handler := NewHandler(store, nil, &config.Config{}, nil)
			handler.configKeys["synthetic-ws-key"] = true
			router := gin.New()
			handler.RegisterRoutes(router)
			var calls atomic.Int32
			sent := make(chan []byte, 4)
			installBasispointsTransport(t, account, func(req *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(req.Body)
				if err != nil {
					return nil, err
				}
				if gjson.GetBytes(body, "store").Type != gjson.False {
					t.Error("BPS continuation must never enable upstream storage")
				}
				sent <- body
				if calls.Add(1) == 1 {
					native := map[string]any{
						"type": "function_call", "id": "fc_ws_native", "call_id": "call_ws_native", "name": "run_officejs", "status": "completed",
						"arguments": map[string]any{"code": `{"name":"get_value","arguments":{"value":"synthetic"}}`, "summary": "synthetic", "extended_summary": "preserve", "references": []any{}, "destructive": false},
					}
					return basispointsTestCompleted("resp_ws_native", []any{native}), nil
				}
				return basispointsTestCompleted("resp_ws_done", []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "synthetic completion"}}}}), nil
			})
			server := httptest.NewServer(router)
			t.Cleanup(server.Close)
			headers := make(http.Header)
			headers.Set("Authorization", "Bearer synthetic-ws-key")
			headers.Set("Session-Id", "synthetic-ws-session")
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", headers)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			tool := map[string]any{"type": "function", "name": "get_value", "parameters": map[string]any{"type": "object"}}
			frame := map[string]any{"type": "response.create", "model": "gpt-6-astra", "store": storeResponse, "tools": []any{tool}, "input": []any{map[string]any{"role": "user", "content": "synthetic request"}}}
			if err := conn.WriteJSON(frame); err != nil {
				t.Fatal(err)
			}
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			first := readResponsesWSTerminalEvent(t, conn)
			if gjson.GetBytes(first, "type").String() != "response.completed" || gjson.GetBytes(first, "response.output.0.name").String() != "get_value" {
				t.Fatalf("first WS turn did not return the client tool: %s", first)
			}
			frame["previous_response_id"] = gjson.GetBytes(first, "response.id").String()
			frame["input"] = []any{map[string]any{"type": "function_call_output", "call_id": "call_ws_native", "output": "synthetic-result"}}
			if err := conn.WriteJSON(frame); err != nil {
				t.Fatal(err)
			}
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			second := readResponsesWSTerminalEvent(t, conn)
			if gjson.GetBytes(second, "type").String() != "response.completed" {
				t.Fatalf("second WS turn failed with store=%v: %s", storeResponse, second)
			}
			if calls.Load() != 2 {
				t.Fatalf("upstream calls = %d, want 2", calls.Load())
			}
			firstBody, secondBody := <-sent, <-sent
			if gjson.GetBytes(secondBody, "previous_response_id").Exists() || gjson.GetBytes(secondBody, `input.#(type=="function_call").name`).String() != "run_officejs" {
				t.Fatalf("WS continuation was not expanded into original BPS history: %s", secondBody)
			}
			for _, field := range []string{"metadata.task_id", "metadata.turn_id"} {
				if gjson.GetBytes(firstBody, field).String() != gjson.GetBytes(secondBody, field).String() {
					t.Fatalf("WS continuation changed %s", field)
				}
			}
		})
	}
}

func TestBasispointsChangePreservesNativeWebSocketStoreFalse(t *testing.T) {
	enableBasispointsForTest(t)
	settings := CurrentRuntimeSettings()
	settings.CodexBasispointsEnabled = false
	ApplyRuntimeSettings(settings)
	resetResponseCacheForTest()
	t.Cleanup(resetResponseCacheForTest)
	var calls atomic.Int32
	conn, _ := newReviewWSClient(t, func(_ context.Context, _ *auth.Account, body []byte, _, _, _ string, _ *DeviceProfileConfig, _ http.Header, _ string) (*http.Response, error) {
		if gjson.GetBytes(body, "store").Type != gjson.False {
			t.Error("native store=false changed")
		}
		if calls.Add(1) == 1 {
			return basispointsTestCompleted("resp_native_store_false", []any{map[string]any{"type": "function_call", "id": "fc_native_store_false", "call_id": "call_native_store_false", "name": "get_value", "arguments": "{}", "status": "completed"}}), nil
		}
		if gjson.GetBytes(body, "previous_response_id").String() != "resp_native_store_false" {
			t.Error("native WS continuation stopped using the original upstream response ID")
		}
		return basispointsTestCompleted("resp_native_second", nil), nil
	})
	for index, frame := range []string{
		`{"type":"response.create","model":"gpt-5.5","store":false,"tools":[{"type":"function","name":"get_value","parameters":{"type":"object"}}],"input":[{"role":"user","content":"synthetic request"}]}`,
		`{"type":"response.create","model":"gpt-5.5","store":false,"previous_response_id":"resp_native_store_false","tools":[{"type":"function","name":"get_value","parameters":{"type":"object"}}],"input":[{"type":"function_call_output","call_id":"call_native_store_false","output":"synthetic result"}]}`,
	} {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		terminal := readResponsesWSTerminalEvent(t, conn)
		if gjson.GetBytes(terminal, "type").String() != "response.completed" {
			t.Fatalf("native turn %d did not complete: %s", index+1, terminal)
		}
		if cached := getResponseCache("anon", gjson.GetBytes(terminal, "response.id").String()); cached != nil {
			t.Fatal("native store=false unexpectedly populated the local response cache")
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("native upstream calls = %d, want 2", calls.Load())
	}
}

func TestBasispointsPreparationErrorIsTypedAndLogsOnlyCategory(t *testing.T) {
	enableBasispointsForTest(t)
	account := &auth.Account{DBID: 91238, AccountID: "workspace", AccessToken: "PRIVATE_TOKEN_SENTINEL"}
	installBasispointsTransport(t, account, func(*http.Request) (*http.Response, error) {
		t.Fatal("a local validation failure must not contact BPS")
		return nil, nil
	})
	var logged bytes.Buffer
	previousWriter := log.Writer()
	log.SetOutput(&logged)
	defer log.SetOutput(previousWriter)
	_, err := ExecuteRequest(context.Background(), account, []byte(`{"model":"gpt-6-astra","input":"PRIVATE_PROMPT_SENTINEL","reasoning":{"mode":"PRIVATE_MODE_SENTINEL"},"tools":[{"type":"function","name":"PRIVATE_TOOL_SENTINEL"}]}`), "", "", "PRIVATE_KEY_SENTINEL", nil, nil)
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != ErrorCodeBasispointsInvalidRequest || typed.HTTPStatus != 400 || typed.Type != ErrorTypeInvalidRequest || typed.Retryable {
		t.Fatalf("local preparation error lost its classification: %#v", err)
	}
	if !strings.Contains(logged.String(), "stage=prepare") || !strings.Contains(logged.String(), "category=reasoning_configuration") {
		t.Fatalf("local preparation failure lacks a diagnostic stage/category: %s", logged.String())
	}
	for _, forbidden := range []string{"PRIVATE_TOKEN_SENTINEL", "PRIVATE_PROMPT_SENTINEL", "PRIVATE_MODE_SENTINEL", "PRIVATE_TOOL_SENTINEL", "PRIVATE_KEY_SENTINEL"} {
		if strings.Contains(logged.String(), forbidden) {
			t.Fatal("preparation diagnostic logged caller-controlled content")
		}
	}
	generalRetries := 0
	if shouldRetryRequestError(err, &generalRetries, 4, database.ContinuousRetryPolicy{Enabled: true, CatchAll: true}) || generalRetries != 0 {
		t.Fatal("local preparation failure must not enter continuous retries")
	}
}

func TestBasispointsWebSocketPreparationErrorKeepsLocalCode(t *testing.T) {
	enableBasispointsForTest(t)
	gin.SetMode(gin.TestMode)
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 1, TestConcurrency: 1, TestModel: "gpt-6-astra", CodexBasispointsEnabled: true})
	t.Cleanup(store.Stop)
	account := &auth.Account{DBID: 91249, AccountID: "synthetic-workspace", AccessToken: "synthetic-token", PlanType: "pro"}
	store.AddAccount(account)
	seedContinuousRetryLocalHealth(account)
	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)
	router := gin.New()
	handler.RegisterRoutes(router)
	var calls atomic.Int32
	installBasispointsTransport(t, account, func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return basispointsTestCompleted("unexpected", nil), nil
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"gpt-6-astra","input":[{"role":"user","content":"synthetic"}],"reasoning":{"mode":"pro"}}`)); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	terminal := readResponsesWSTerminalEvent(t, conn)
	if gjson.GetBytes(terminal, "type").String() != "error" || gjson.GetBytes(terminal, "error.code").String() != ErrorCodeBasispointsInvalidRequest || gjson.GetBytes(terminal, "error.type").String() != ErrorTypeInvalidRequest {
		t.Fatalf("WS swallowed the local validation error identity: %s", terminal)
	}
	if gjson.GetBytes(terminal, "error.details.stage").String() != "prepare" || gjson.GetBytes(terminal, "error.details.category").String() != "reasoning_configuration" {
		t.Fatalf("WS validation error omitted the local stage/category: %s", terminal)
	}
	if calls.Load() != 0 {
		t.Fatal("local WS validation failure reached BPS")
	}
	assertContinuousRetryLocalHealthUnchanged(t, account)
}

func basispointsTestCompleted(responseID string, output []any) *http.Response {
	if output == nil {
		output = []any{}
	}
	raw, _ := json.Marshal(map[string]any{
		"type":     "response.completed",
		"response": map[string]any{"id": responseID, "status": "completed", "output": output, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}},
	})
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: " + string(raw) + "\n\n"))}
}

// This exercises the registered HTTP route, standard authentication, account
// selection, ordinary Responses preparation, BPS transport, SSE rewriting and
// history replay. The final transport alone is mocked; no account or network
// credentials are needed, and no live BPS acceptance is inferred from this test.
func TestBasispointsHTTPIngressToolRoundTrip(t *testing.T) {
	for index, tc := range []struct {
		kind         string
		continuation bool
		rawCustom    bool
	}{
		{"function", false, false}, {"custom", false, false}, {"function", true, false}, {"custom", true, false},
		{"custom", false, true}, {"custom", true, true},
	} {
		name := tc.kind + "/full_history"
		if tc.continuation {
			name = tc.kind + "/previous_response_id"
		}
		if tc.rawCustom {
			name += "/raw_transport"
		}
		t.Run(name, func(t *testing.T) {
			enableBasispointsForTest(t)
			resetResponseCacheForTest()
			t.Cleanup(resetResponseCacheForTest)
			gin.SetMode(gin.TestMode)
			store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-6-astra", CodexBasispointsEnabled: true})
			t.Cleanup(store.Stop)
			account := &auth.Account{DBID: int64(91240 + index), AccountID: "synthetic-workspace", AccessToken: "synthetic-token", PlanType: "pro"}
			store.AddAccount(account)
			handler := NewHandler(store, nil, &config.Config{}, nil)
			handler.configKeys["synthetic-client-key"] = true
			router := gin.New()
			handler.RegisterRoutes(router)
			envelope := map[string]any{"name": "functions.run", "arguments": map[string]any{"value": "synthetic"}}
			if tc.kind == "custom" {
				delete(envelope, "arguments")
				envelope["input"] = "*** Begin Patch\n*** End Patch"
			}
			code, _ := json.Marshal(envelope)
			native := map[string]any{
				"type": "function_call", "id": "fc_http_native", "call_id": "call_http_native", "name": "run_officejs", "status": "completed",
				"arguments": map[string]any{"code": string(code), "summary": "synthetic summary", "extended_summary": "synthetic original item", "references": []any{"synthetic reference"}, "destructive": false},
			}
			if tc.rawCustom {
				arguments := native["arguments"].(map[string]any)
				arguments["summary"] = "codex2api.custom/functions.run"
				arguments["code"] = envelope["input"]
			}
			var sent [][]byte
			installBasispointsTransport(t, account, func(req *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatal(err)
				}
				sent = append(sent, body)
				if gjson.GetBytes(body, "tools").Exists() || gjson.GetBytes(body, "previous_response_id").Exists() {
					t.Fatal("Codex-only fields escaped the complete ingress preparation")
				}
				if len(sent) == 1 {
					return basispointsTestCompleted("resp_http_native", []any{native}), nil
				}
				var restored map[string]any
				for _, item := range gjson.GetBytes(body, "input").Array() {
					if item.Get("type").String() == "function_call" {
						if err := json.Unmarshal([]byte(item.Raw), &restored); err != nil {
							t.Fatal(err)
						}
					}
				}
				if !reflect.DeepEqual(restored, native) {
					t.Fatalf("ingress replay did not preserve the complete native item: got=%v want=%v", restored, native)
				}
				last := gjson.GetBytes(body, "input.@reverse.0")
				if last.Get("type").String() != "function_call_output" || last.Get("call_id").String() != "call_http_native" || last.Get("output").String() != "synthetic-result" || last.Get("id").String() == "" {
					t.Fatalf("tool result was not normalized for BPS: %s", last.Raw)
				}
				return basispointsTestCompleted("resp_http_done", []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "synthetic completion"}}}}), nil
			})
			userItem := map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "synthetic tool request"}}}
			requestBody := map[string]any{
				"model": "gpt-6-astra", "stream": true, "input": []any{userItem},
				"tools": []any{map[string]any{"type": "namespace", "name": "functions", "tools": []any{map[string]any{"type": tc.kind, "name": "run", "parameters": map[string]any{"type": "object"}}}}},
			}
			requestBody["tools"] = append(requestBody["tools"].([]any), map[string]any{"type": "web_search"})
			invoke := func(body map[string]any) gjson.Result {
				t.Helper()
				raw, _ := json.Marshal(body)
				req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(raw))
				req.Header.Set("Authorization", "Bearer synthetic-client-key")
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Session-Id", "synthetic-client-session")
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, req)
				if recorder.Code != 200 {
					t.Fatalf("registered ingress returned %d: %s", recorder.Code, recorder.Body.String())
				}
				if recorder.Header().Get("X-Codex2API-Upstream") != "basispoints" || !strings.Contains(recorder.Header().Get("X-Codex2API-Basispoints-Warnings"), "web_search") {
					t.Fatal("ingress omitted Basispoints capability response headers")
				}
				for _, line := range strings.Split(recorder.Body.String(), "\n") {
					if strings.HasPrefix(line, "data: ") {
						event := gjson.Parse(strings.TrimPrefix(line, "data: "))
						if event.Get("type").String() == "response.completed" {
							return event.Get("response")
						}
					}
				}
				t.Fatalf("ingress did not complete: %s", recorder.Body.String())
				return gjson.Result{}
			}
			first := invoke(requestBody)
			call := first.Get("output.0")
			wantType := "function_call"
			if tc.kind == "custom" {
				wantType = "custom_tool_call"
			}
			if call.Get("type").String() != wantType || call.Get("name").String() != "run" || call.Get("namespace").String() != "functions" {
				t.Fatalf("downstream tool identity was not restored: %s", call.Raw)
			}
			output := map[string]any{"type": wantType + "_output", "call_id": call.Get("call_id").String(), "output": "synthetic-result"}
			if tc.continuation {
				requestBody["previous_response_id"] = first.Get("id").String()
				requestBody["input"] = []any{output}
			} else {
				requestBody["input"] = []any{userItem, json.RawMessage(call.Raw), output}
			}
			second := invoke(requestBody)
			if second.Get("output.0.content.0.text").String() != "synthetic completion" || len(sent) != 2 {
				t.Fatal("complete tool round trip did not finish exactly two upstream calls")
			}
			for _, field := range []string{"prompt_cache_key", "metadata.task_id", "metadata.turn_id"} {
				if gjson.GetBytes(sent[0], field).String() == "" || gjson.GetBytes(sent[0], field).String() != gjson.GetBytes(sent[1], field).String() {
					t.Fatalf("tool continuation changed %s", field)
				}
			}
			if gjson.GetBytes(sent[0], "metadata.agent_iteration").String() != "1" || gjson.GetBytes(sent[1], "metadata.agent_iteration").String() != "2" {
				t.Fatal("tool continuation did not advance agent_iteration")
			}
		})
	}
}
