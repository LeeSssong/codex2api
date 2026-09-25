package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/config"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
)

func TestBasispointsAgentMessageIngress(t *testing.T) {
	const message = "请核对任务\nKeep \"quotes\" and tabs\tintact."
	for _, tool := range []string{"spawn_agent", "send_message", "followup_task"} {
		for _, transport := range []string{"json", "sse", "websocket"} {
			t.Run(tool+"/"+transport, func(t *testing.T) {
				enableBasispointsForTest(t)
				resetResponseCacheForTest()
				t.Cleanup(resetResponseCacheForTest)
				gin.SetMode(gin.TestMode)
				store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-6-astra", CodexBasispointsEnabled: true})
				t.Cleanup(store.Stop)
				account := &auth.Account{DBID: 91259, AccountID: "synthetic-agent", AccessToken: "synthetic-token", PlanType: "pro"}
				store.AddAccount(account)
				handler := NewHandler(store, nil, &config.Config{}, nil)
				handler.configKeys["synthetic-agent-key"] = true
				router := gin.New()
				handler.RegisterRoutes(router)
				upstream := make(chan []byte, 2)
				installBasispointsTransport(t, account, func(req *http.Request) (*http.Response, error) {
					body, err := io.ReadAll(req.Body)
					if err != nil {
						return nil, err
					}
					upstream <- body
					if bytes.Contains(body, []byte(`"agent_message"`)) {
						return basispointsTestCompleted("resp_child", []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "acknowledged"}}}}), nil
					}
					args := map[string]any{"message": message}
					if tool == "spawn_agent" {
						args["task_name"], args["fork_turns"] = "worker", "none"
					} else {
						args["target"] = "worker"
					}
					envelope, _ := json.Marshal(map[string]any{"name": "collaboration." + tool, "arguments": args})
					return basispointsTestCompleted("resp_parent", []any{map[string]any{
						"type": "function_call", "id": "fc_agent", "call_id": "call_agent", "name": "run_officejs", "status": "completed",
						"arguments": map[string]any{"code": string(envelope), "summary": "synthetic collaboration"},
					}}), nil
				})
				payload := map[string]any{"model": "gpt-6-astra", "input": "synthetic parent task", "stream": transport != "json",
					"tools": []any{map[string]any{"type": "namespace", "name": "collaboration", "tools": []any{
						map[string]any{"type": "function", "name": tool, "parameters": map[string]any{"type": "object", "properties": map[string]any{"message": map[string]any{"type": "string", "encrypted": true}}}},
					}}}}
				var terminal []byte
				if transport == "websocket" {
					server := httptest.NewServer(router)
					t.Cleanup(server.Close)
					headers := http.Header{"Authorization": []string{"Bearer synthetic-agent-key"}, "Session-Id": []string{"agent-parent"}}
					conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", headers)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = conn.Close() })
					payload["type"] = "response.create"
					if err := conn.WriteJSON(payload); err != nil {
						t.Fatal(err)
					}
					_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
					terminal = readResponsesWSTerminalEvent(t, conn)
				} else {
					body, _ := json.Marshal(payload)
					recorder := httptest.NewRecorder()
					req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("Authorization", "Bearer synthetic-agent-key")
					req.Header.Set("Session-Id", "agent-parent")
					router.ServeHTTP(recorder, req)
					if recorder.Code != http.StatusOK {
						t.Fatalf("parent failed: %d %s", recorder.Code, recorder.Body.String())
					}
					terminal = recorder.Body.Bytes()
					if transport == "sse" {
						terminal = nil
						for _, line := range bytes.Split(recorder.Body.Bytes(), []byte("\n")) {
							data := bytes.TrimPrefix(line, []byte("data: "))
							if gjson.GetBytes(data, "type").String() == "response.completed" {
								terminal = data
							}
						}
					}
				}
				path := "response.output.0"
				if transport == "json" {
					path = "output.0"
				}
				call := gjson.GetBytes(terminal, path)
				if call.Get("encrypted_function_args").Raw != "[]" || call.Get("name").String() != tool || call.Get("namespace").String() != "collaboration" {
					t.Fatalf("client lost plaintext marker or tool identity: %s", terminal)
				}
				actual := gjson.Get(call.Get("arguments").String(), "message").String()
				if actual != message {
					t.Fatalf("task changed: %q", actual)
				}
				<-upstream

				// A child uses its own session and sends the marked task as input_text.
				child, _ := json.Marshal(map[string]any{"model": "gpt-6-astra", "stream": false, "input": []any{
					map[string]any{"type": "agent_message", "author": "/root", "recipient": "/root/worker", "content": []any{map[string]any{"type": "input_text", "text": actual}}},
				}})
				recorder := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(child))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer synthetic-agent-key")
				req.Header.Set("Session-Id", "agent-child")
				router.ServeHTTP(recorder, req)
				if recorder.Code != http.StatusOK || gjson.GetBytes(recorder.Body.Bytes(), "status").String() != "completed" {
					t.Fatalf("child failed: %d %s", recorder.Code, recorder.Body.String())
				}
				select {
				case body := <-upstream:
					parts := gjson.GetBytes(body, `input.#(type=="agent_message").content.0`)
					if parts.Get("type").String() != "input_text" || parts.Get("text").String() != message {
						t.Fatalf("child input changed upstream: %s", body)
					}
				default:
					t.Fatal("child never reached the synthetic upstream")
				}
			})
		}
	}
}
