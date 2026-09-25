package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/codex2api/auth"
	"github.com/codex2api/config"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func basispointsTestFailed(code, message string) *http.Response {
	raw, _ := json.Marshal(map[string]any{
		"type": "response.failed",
		"response": map[string]any{
			"status": "failed", "output": []any{},
			"error": map[string]any{"code": code, "message": message},
		},
	})
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: " + string(raw) + "\n\n"))}
}

// Basispoints always returns HTTP 200 and surfaces upstream failures inside the
// SSE stream, so an invalid_encrypted_content response.failed never reached the
// HTTP-status recovery. The streaming path now strips the ciphertext the backend
// cannot decrypt and retries once, before any downstream bytes.
func TestBasispointsStreamInvalidEncryptedContentStripsAndRetries(t *testing.T) {
	enableBasispointsForTest(t)
	resetResponseCacheForTest()
	t.Cleanup(resetResponseCacheForTest)
	gin.SetMode(gin.TestMode)
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-6-astra", CodexBasispointsEnabled: true})
	t.Cleanup(store.Stop)
	account := &auth.Account{DBID: 91270, AccountID: "synthetic-workspace", AccessToken: "synthetic-token", PlanType: "pro"}
	store.AddAccount(account)
	seedContinuousRetryLocalHealth(account)
	handler := NewHandler(store, nil, &config.Config{}, nil)
	handler.configKeys["synthetic-client-key"] = true
	router := gin.New()
	handler.RegisterRoutes(router)

	var sent [][]byte
	installBasispointsTransport(t, account, func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		sent = append(sent, body)
		if len(sent) == 1 {
			return basispointsTestFailed("invalid_encrypted_content", "Encrypted function output content could not be decrypted or decoded."), nil
		}
		return basispointsTestCompleted("resp_recovered", []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "recovered answer"}}}}), nil
	})

	requestBody := map[string]any{
		"model": "gpt-6-astra", "stream": true,
		"input": []any{
			map[string]any{"type": "reasoning", "encrypted_content": "gAAAAOPAQUE_ENC_SENTINEL", "summary": []any{}},
			map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "continue the task"}}},
		},
	}
	raw, _ := json.Marshal(requestBody)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer synthetic-client-key")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Session-Id", "synthetic-enc-session")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("ingress returned %d: %s", recorder.Code, recorder.Body.String())
	}
	if len(sent) != 2 {
		t.Fatalf("expected exactly two upstream attempts (fail then recovered), got %d", len(sent))
	}
	if !bytes.Contains(sent[0], []byte("gAAAAOPAQUE_ENC_SENTINEL")) {
		t.Fatalf("first attempt should carry the encrypted content: %s", sent[0])
	}
	if bytes.Contains(sent[1], []byte("gAAAAOPAQUE_ENC_SENTINEL")) || bytes.Contains(sent[1], []byte("encrypted_content")) {
		t.Fatalf("retry must drop the rejected encrypted content: %s", sent[1])
	}
	var completedText string
	for _, line := range strings.Split(recorder.Body.String(), "\n") {
		if strings.HasPrefix(line, "data: ") {
			event := gjson.Parse(strings.TrimPrefix(line, "data: "))
			if event.Get("type").String() == "response.completed" {
				completedText = event.Get("response.output.0.content.0.text").String()
			}
		}
	}
	if completedText != "recovered answer" {
		t.Fatalf("client did not receive the recovered response: %s", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "invalid_encrypted_content") || strings.Contains(recorder.Body.String(), "response.failed") {
		t.Fatalf("the rejected failure must not reach the client: %s", recorder.Body.String())
	}
}

// A second invalid_encrypted_content (or one with no ciphertext to drop) must not
// loop: the recovery fires at most once, then the failure is reported normally.
func TestBasispointsStreamInvalidEncryptedContentRetriesOnlyOnce(t *testing.T) {
	enableBasispointsForTest(t)
	resetResponseCacheForTest()
	t.Cleanup(resetResponseCacheForTest)
	gin.SetMode(gin.TestMode)
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-6-astra", CodexBasispointsEnabled: true})
	t.Cleanup(store.Stop)
	account := &auth.Account{DBID: 91271, AccountID: "synthetic-workspace", AccessToken: "synthetic-token", PlanType: "pro"}
	store.AddAccount(account)
	seedContinuousRetryLocalHealth(account)
	handler := NewHandler(store, nil, &config.Config{}, nil)
	handler.configKeys["synthetic-client-key"] = true
	router := gin.New()
	handler.RegisterRoutes(router)

	var sent [][]byte
	installBasispointsTransport(t, account, func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		sent = append(sent, body)
		return basispointsTestFailed("invalid_encrypted_content", "Encrypted function output content could not be decrypted or decoded."), nil
	})

	requestBody := map[string]any{
		"model": "gpt-6-astra", "stream": true,
		"input": []any{
			map[string]any{"type": "reasoning", "encrypted_content": "gAAAAOPAQUE_ENC_SENTINEL", "summary": []any{}},
			map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "continue"}}},
		},
	}
	raw, _ := json.Marshal(requestBody)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer synthetic-client-key")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Session-Id", "synthetic-enc-session-loop")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	// One original attempt plus exactly one stripped retry; the second still fails,
	// but has no ciphertext left to strip, so the loop stops and the error surfaces.
	if len(sent) != 2 {
		t.Fatalf("recovery must fire at most once, got %d attempts", len(sent))
	}
	if bytes.Contains(sent[1], []byte("gAAAAOPAQUE_ENC_SENTINEL")) {
		t.Fatalf("retry must drop the rejected encrypted content: %s", sent[1])
	}
}
