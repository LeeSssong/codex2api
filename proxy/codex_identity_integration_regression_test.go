package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/codex2api/auth"
	"github.com/tidwall/gjson"
)

type identityRepairAttempt struct {
	ctx     context.Context
	account *auth.Account
	body    []byte
	headers http.Header
	session string
}

func captureIdentityRepairAttempts(t *testing.T) *[]identityRepairAttempt {
	t.Helper()
	t.Setenv("CODEX_IDENTITY_STORE", "memory")
	t.Setenv("CODEX_PERSONA_STORE", "memory")
	t.Setenv("CODEX_TELEMETRY_ENABLED", "false")
	resetCodexConversationIdentity()
	previous := WebsocketExecuteFunc
	t.Cleanup(func() { WebsocketExecuteFunc = previous; resetCodexConversationIdentity() })
	attempts := &[]identityRepairAttempt{}
	WebsocketExecuteFunc = func(ctx context.Context, a *auth.Account, b []byte, s, p, k string, d *DeviceProfileConfig, h http.Header, pool string) (*http.Response, error) {
		*attempts = append(*attempts, identityRepairAttempt{ctx, a, append([]byte(nil), b...), h.Clone(), s})
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	}
	return attempts
}

func sendIdentityRepairRequest(t *testing.T, account *auth.Account, key, body string, headers http.Header) {
	t.Helper()
	ctx := WithCodexTurnStateAffinityKey(context.Background(), "same-local-affinity")
	response, err := executeNativeCodexRequest(ctx, account, []byte(body), "", "", key, nil, headers, true)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
}

func TestIdentityRepairAPIKeyIsolation(t *testing.T) {
	attempts := captureIdentityRepairAttempts(t)
	a := fingerprintAccount(t, auth.CodexFingerprintModeSingleMachineMultiWindow)
	for _, h := range []http.Header{nil, {"Session-Id": {"shared-client-session"}, "Thread-Id": {"shared-client-thread"}}} {
		for _, key := range []string{"tenant-a", "tenant-b", "tenant-a"} {
			sendIdentityRepairRequest(t, a, key, `{"model":"gpt-5.5","input":"same opening","client_metadata":{}}`, h)
		}
		n := len(*attempts)
		x, y, z := (*attempts)[n-3], (*attempts)[n-2], (*attempts)[n-1]
		if x.session == y.session {
			t.Error("API keys share a transport/thread key")
		}
		if gjson.GetBytes(x.body, "prompt_cache_key").String() == gjson.GetBytes(y.body, "prompt_cache_key").String() {
			t.Error("API keys share a prompt cache key")
		}
		if x.session != z.session {
			t.Error("same API key lost stable convergence")
		}
	}
}

func TestIdentityRepairImmutableResponseOwnership(t *testing.T) {
	for _, secondAccountID := range []int64{42, 43} {
		t.Run(fmt.Sprint("second-account=", secondAccountID), func(t *testing.T) {
			attempts := captureIdentityRepairAttempts(t)
			a := fingerprintAccount(t, auth.CodexFingerprintModeSingleMachineMultiWindow)
			b := &auth.Account{DBID: secondAccountID, CodexFingerprintMode: auth.CodexFingerprintModeSingleMachineMultiWindow}
			sendIdentityRepairRequest(t, a, "tenant-a", `{"model":"gpt-5.5","input":"first"}`, nil)
			sendIdentityRepairRequest(t, b, "tenant-a", `{"model":"gpt-5.5","input":"second"}`, nil)
			x, y := (*attempts)[0], (*attempts)[1]
			ObserveCodexTurnStateFrame(x.ctx, []byte(`{"type":"response.completed","response":{"id":"resp-owned-a"},"headers":{"x-codex-turn-state":"token-a"}}`))
			ObserveCodexTurnStateFrame(y.ctx, []byte(`{"type":"response.completed","response":{"id":"resp-owned-b"},"headers":{"x-codex-turn-state":"token-b"}}`))
			// Reuse the same affinity while both original attempts are still observable.
			sendIdentityRepairRequest(t, a, "tenant-a", `{"model":"gpt-5.5","previous_response_id":"resp-owned-a","input":"next"}`, nil)
			sendIdentityRepairRequest(t, b, "tenant-a", `{"model":"gpt-5.5","previous_response_id":"resp-owned-b","input":"next"}`, nil)
			for i, want := range []identityRepairAttempt{x, y} {
				got := (*attempts)[i+2]
				if got.session != want.session {
					t.Errorf("account %d recalled a different conversation", want.account.ID())
				}
				token := fmt.Sprintf("token-%c", 'a'+i)
				if got.headers.Get(codexTurnStateHeader) != token {
					t.Errorf("account %d state = %q, want %q", want.account.ID(), got.headers.Get(codexTurnStateHeader), token)
				}
			}
		})
	}
}

func TestIdentityRepairInjectionPrecedence(t *testing.T) {
	attempts := captureIdentityRepairAttempts(t)
	a := fingerprintAccount(t, auth.CodexFingerprintModeSingleMachineMultiWindow)
	a.CodexTurnState = "configured-state"
	a.CodexTurnStateModels = "gpt-*"
	for _, cached := range []string{"", "observed-state"} {
		sendIdentityRepairRequest(t, a, "tenant-a", `{"model":"gpt-5.5","input":"same thread","client_metadata":{}}`, nil)
		got := (*attempts)[len(*attempts)-1]
		if got.headers.Get(codexTurnStateHeader) != "configured-state" || gjson.GetBytes(got.body, "client_metadata.x-codex-turn-state").String() != "configured-state" {
			t.Errorf("injection disagrees: header=%q body=%s", got.headers.Get(codexTurnStateHeader), got.body)
		}
		if cached == "" {
			ObserveCodexTurnStateFrame(got.ctx, []byte(`{"headers":{"x-codex-turn-state":"observed-state"}}`))
		}
	}
}

func TestIdentityRepairContinuationKeepsChildTopology(t *testing.T) {
	attempts := captureIdentityRepairAttempts(t)
	a := fingerprintAccount(t, auth.CodexFingerprintModeSingleMachineMultiWindow)
	sendIdentityRepairRequest(t, a, "tenant-a", `{"model":"gpt-5.5","input":"initial","client_metadata":{}}`, http.Header{"Session-Id": {"root"}, "Thread-Id": {"child"}})
	first := (*attempts)[0]
	ObserveCodexTurnStateFrame(first.ctx, []byte(`{"type":"response.completed","response":{"id":"resp-child"}}`))
	sendIdentityRepairRequest(t, a, "tenant-a", `{"model":"gpt-5.5","previous_response_id":"resp-child","input":"incremental user message","client_metadata":{}}`, nil)
	next := (*attempts)[1]
	if first.session != next.session {
		t.Fatal("continuation content replaced the recalled child thread")
	}
	for _, field := range []string{"session_id", "thread_id"} {
		if gjson.GetBytes(first.body, "client_metadata."+field).String() != gjson.GetBytes(next.body, "client_metadata."+field).String() {
			t.Errorf("continuation lost %s", field)
		}
	}
	sendIdentityRepairRequest(t, a, "tenant-b", `{"model":"gpt-5.5","previous_response_id":"resp-child","input":"incremental user message","client_metadata":{}}`, nil)
	if (*attempts)[2].session == first.session {
		t.Fatal("response recall crossed API keys")
	}
}

func TestIdentityRepairManagedStatePrecedence(t *testing.T) {
	previousIPv6, previousVerified := ipv6StateProvider.Load(), verifiedStateProvider.Load()
	t.Cleanup(func() { ipv6StateProvider.Store(previousIPv6); verifiedStateProvider.Store(previousVerified) })
	a := fingerprintAccount(t, auth.CodexFingerprintModeSingleMachineMultiWindow)
	a.CodexTurnState, a.CodexTurnStateModels = "configured-state", "gpt-*"
	for _, ipv6 := range []bool{false, true} {
		for _, token := range []string{"managed-state", ""} {
			t.Run(fmt.Sprintf("ipv6=%v/empty=%v", ipv6, token == ""), func(t *testing.T) {
				verifiedStateProvider.Store(&statePoolProvider{claims: func(*auth.Account, string, string, string) bool { return true }})
				ipv6StateProvider.Store(nil)
				if ipv6 {
					ipv6StateProvider.Store(&IPv6StateProvider{Applies: func(*auth.Account, string) bool { return true }})
					// A competing verified claim must not replace the IPv6 decision.
				}
				ctx, h, body := prepareCodexConversationIdentity(context.Background(), a, nil, []byte(`{"model":"gpt-5.5","input":"managed","client_metadata":{}}`), "managed-key")
				owner, _ := codexConversationOwnerFromContext(ctx)
				rememberCodexThreadTurnState(owner.accountID, owner.threadID, "observed-state")
				h.Set(codexTurnStateHeader, token)
				ctx, body, h = prepareCodexTurnStateInjection(ctx, a, body, h, true)
				body, h = applyCodexThreadTurnState(ctx, a, h, body)
				if h.Get(codexTurnStateHeader) != token || gjson.GetBytes(body, "client_metadata.x-codex-turn-state").String() != token {
					t.Fatalf("managed selection changed: header=%q body=%s", h.Get(codexTurnStateHeader), body)
				}
				h.Set(codexTurnStateHeader, "custom-header-state")
				applyCodexTurnStateInjectionHeader(ctx, h)
				if h.Get(codexTurnStateHeader) != token {
					t.Fatal("custom headers replaced managed selection")
				}
			})
		}
	}
}

func TestIdentityRepairHTTPAndCompactRecall(t *testing.T) {
	t.Setenv("CODEX_IDENTITY_STORE", "memory")
	t.Setenv("CODEX_PERSONA_STORE", "memory")
	t.Setenv("CODEX_TELEMETRY_ENABLED", "false")
	resetCodexConversationIdentity()
	t.Cleanup(resetCodexConversationIdentity)
	previous := GetResinConfig()
	t.Cleanup(func() { SetResinConfig(previous) })
	for _, compact := range []bool{false, true} {
		t.Run(fmt.Sprintf("compact=%v", compact), func(t *testing.T) {
			a := fingerprintAccount(t, auth.CodexFingerprintModeSingleMachineMultiWindow)
			a.AccessToken = "test-token"
			var frames []identityRepairAttempt
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				frames = append(frames, identityRepairAttempt{headers: r.Header.Clone(), body: readUpstreamRequestBody(r)})
				w.Header().Set(codexTurnStateHeader, "http-owned-state")
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"resp-http-child","status":"completed"}`)
			}))
			defer server.Close()
			SetResinConfig(&ResinConfig{BaseURL: server.URL, PlatformName: "identity-repair"})
			for i, key := range []string{"http-tenant-a", "http-tenant-a", "http-tenant-b"} {
				body := []byte(`{"model":"gpt-5.5","input":"initial","client_metadata":{}}`)
				h := http.Header{"Session-Id": {fmt.Sprint("root-", compact)}, "Thread-Id": {fmt.Sprint("child-", compact)}}
				if i > 0 {
					body = []byte(`{"model":"gpt-5.5","input":"incremental","previous_response_id":"resp-http-child","client_metadata":{}}`)
					h = nil
				}
				var response *http.Response
				var err error
				if compact {
					response, err = ExecuteCompactRequest(context.Background(), a, body, "", "", fmt.Sprint(key, compact), nil, h)
				} else {
					response, err = ExecuteRequest(context.Background(), a, body, "", "", fmt.Sprint(key, compact), nil, h, false)
				}
				if err != nil {
					t.Fatal(err)
				}
				_, err = io.Copy(io.Discard, response.Body)
				response.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			if frames[0].headers.Get("Thread-Id") != frames[1].headers.Get("Thread-Id") || frames[0].headers.Get("Session-Id") != frames[1].headers.Get("Session-Id") {
				t.Fatal("HTTP continuation lost child topology")
			}
			if frames[1].headers.Get(codexTurnStateHeader) != "http-owned-state" || gjson.GetBytes(frames[1].body, "client_metadata.x-codex-turn-state").String() != "http-owned-state" {
				t.Fatal("HTTP continuation lost captured response state")
			}
			if frames[2].headers.Get(codexTurnStateHeader) != "" || frames[2].headers.Get("Thread-Id") == frames[0].headers.Get("Thread-Id") {
				t.Fatal("HTTP recall/state crossed API keys")
			}
		})
	}
}

func TestIdentityRepairPersistencePathAndErrors(t *testing.T) {
	t.Setenv("CODEX_IDENTITY_STORE", "")
	t.Setenv("DATA_DIR", t.TempDir())
	if got, want := codexIdentityStorePath(), filepath.Join(codexPersistentStateDir(), "codex-conversation-identity.json"); got != want {
		t.Fatalf("default path = %q, want %q", got, want)
	}
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })
	privatePath := filepath.Join(t.TempDir(), "sensitive-path-marker")
	if err := os.Mkdir(privatePath, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_IDENTITY_STORE", privatePath)
	s := &codexConversationIdentityState{}
	s.loadLocked()
	s.saveLocked() // Renaming a file onto the directory must fail.
	if !strings.Contains(output.String(), "store read failed") || !strings.Contains(output.String(), "store rename failed") || strings.Contains(output.String(), privatePath) || strings.Contains(output.String(), "sensitive-path-marker") {
		t.Fatalf("missing/unsafe operation errors: %s", &output)
	}
	output.Reset()
	// Type errors can contain the original numeric token; none may reach logs.
	logCodexIdentityStoreError("decode", &json.UnmarshalTypeError{Value: "number 12345678901234567890", Type: reflect.TypeOf("")})
	if strings.Contains(output.String(), "12345678901234567890") {
		t.Fatal("decoder error exposed a stored value")
	}
	output.Reset()
	invalid := filepath.Join(t.TempDir(), "sensitive-content-marker.json")
	if err := os.WriteFile(invalid, []byte(`{"sensitive-content-marker":`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_IDENTITY_STORE", invalid)
	(&codexConversationIdentityState{}).loadLocked()
	if !strings.Contains(output.String(), "store decode failed") || strings.Contains(output.String(), "sensitive-content-marker") {
		t.Fatalf("missing/unsafe decode error: %s", &output)
	}
}
