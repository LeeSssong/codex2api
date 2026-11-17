package wsrelay

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/proxy"
	"github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
)

// Run through the real executor and reusable slot selection. Inspect each
// frame against its socket's frozen handshake, not just the pool-key helper.
func TestSessionIdentityFingerprintReusableWebsocket(t *testing.T) {
	t.Setenv("CODEX_WS_STATELESS_ONESHOT", "false")
	t.Setenv("CODEX_SESSION_HEADER_MODE", "native")
	previous := proxy.GetResinConfig()
	t.Cleanup(func() { proxy.SetResinConfig(previous) })
	type observation struct {
		headers http.Header
		body    []byte
	}
	frames := make(chan observation, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		for {
			_, frame, err := conn.ReadMessage()
			if err != nil {
				return
			}
			frames <- observation{r.Header.Clone(), frame}
			if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"status":"completed","output":[]}}`)); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	proxy.SetResinConfig(&proxy.ResinConfig{BaseURL: server.URL, PlatformName: "test"})
	manager := NewManager()
	t.Cleanup(manager.Stop)
	manager.probeFunc = func(*WsConnection) bool { return true }
	executor := NewExecutorWithManager(manager)
	account := &auth.Account{DBID: 42, AccessToken: "fixture-token", CodexFingerprintMode: auth.CodexFingerprintModeSingleMachineMultiWindow}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var sockets []*WsConnection
	for i, thread := range []string{"root", "child", "root"} {
		original := []byte(fmt.Sprintf(`{"model":"gpt-5.4","input":"test","prompt_cache_key":"private-cache","client_metadata":{"session_id":"root","thread_id":%q}}`, thread))
		headers := proxy.PrepareCodexFingerprintHeaders(account, nil, original)
		body := proxy.ApplyCodexFingerprintToBody(original, account, headers)
		response, err := executor.ExecuteRequestViaWebsocket(ctx, account, body, fmt.Sprintf("stateless-fixture-%d", i), "", "fixture-key", nil, headers, "fixture-key-pool")
		if err != nil {
			t.Fatal(err)
		}
		sockets = append(sockets, response.conn)
		httpResponse := websocketResponseToHTTP(ctx, response, http.StatusOK, nil)
		_, readErr := io.Copy(io.Discard, httpResponse.Body)
		httpResponse.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		select {
		case frame := <-frames:
			session, mappedThread := proxy.ConvergedCodexSessionIdentity(account, headers)
			if frame.headers.Get("Session-Id") != session || frame.headers.Get("Thread-Id") != mappedThread ||
				gjson.GetBytes(frame.body, "client_metadata.thread_id").String() != mappedThread {
				t.Fatalf("frame identity differs from its frozen handshake: session=%s thread=%s want=(%s,%s) body=%s",
					frame.headers.Get("Session-Id"), frame.headers.Get("Thread-Id"), session, mappedThread, frame.body)
			}
			if gjson.GetBytes(frame.body, "prompt_cache_key").String() != "private-cache" {
				t.Fatal("cache partition changed")
			}
		case <-ctx.Done():
			t.Fatal("no local WebSocket frame")
		}
	}
	if sockets[0] == sockets[1] {
		t.Fatal("parent and child reused the same frozen handshake")
	}
	if sockets[0] != sockets[2] {
		t.Fatal("same conversation lost connection reuse")
	}
}

func TestSingleMachineHTTPAndWebsocketShareIdentityHeaders(t *testing.T) {
	t.Setenv("CODEX_SESSION_HEADER_MODE", "native")
	prev := proxy.CurrentRuntimeSettings()
	t.Cleanup(func() { proxy.ApplyRuntimeSettings(prev) })
	proxy.ApplyRuntimeSettings(proxy.RuntimeSettings{ClientCompatMode: proxy.ClientCompatModePreserve, CodexForceWebsocket: false})
	previous := proxy.GetResinConfig()
	t.Cleanup(func() { proxy.SetResinConfig(previous) })
	var httpHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	t.Cleanup(server.Close)
	proxy.SetResinConfig(&proxy.ResinConfig{BaseURL: server.URL, PlatformName: "test"})

	account := &auth.Account{DBID: 424242, AccountID: "424242", AccessToken: "fixture-token", CodexFingerprintMode: auth.CodexFingerprintModeSingleMachineMultiWindow}
	body := []byte(`{"model":"gpt-5.6-codex","input":[],"prompt_cache_key":"conversation-a","client_metadata":{"sandbox":"workspace-write"}}`)
	resp, err := proxy.ExecuteRequest(context.Background(), account, body, "", "", "fixture-key", nil, http.Header{}, false)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	wsHeaders := NewExecutor().prepareWebsocketHeaders(context.Background(), "fixture-token", account, account.AccountID, "conversation-a", "fixture-key", nil, http.Header{}, body, "")
	for _, name := range []string{"User-Agent", "Originator", "Version", "Session-Id", "Thread-Id"} {
		if httpHeaders.Get(name) == "" || httpHeaders.Get(name) != wsHeaders.Get(name) {
			t.Fatalf("%s http=%q ws=%q", name, httpHeaders.Get(name), wsHeaders.Get(name))
		}
	}
	httpInstall := gjson.Get(httpHeaders.Get("X-Codex-Turn-Metadata"), "installation_id").String()
	wsInstall := gjson.Get(wsHeaders.Get("X-Codex-Turn-Metadata"), "installation_id").String()
	if httpInstall == "" || httpInstall != wsInstall {
		t.Fatalf("installation http=%q ws=%q", httpInstall, wsInstall)
	}
	if gjson.Get(httpHeaders.Get("X-Codex-Turn-Metadata"), "session_id").String() != httpHeaders.Get("Session-Id") {
		t.Fatal("HTTP session header and metadata differ")
	}
	for _, headers := range []http.Header{httpHeaders, wsHeaders} {
		if headers.Get("Conversation_id") != "" || headers.Get("Session_id") != "" || headers.Get("X-Codex-Installation-Id") != "" {
			t.Fatalf("unexpected identity header: %v", headers)
		}
	}

	shared := NewExecutor().prepareWebsocketHeaders(context.Background(), "fixture-token", account, account.AccountID, "", "fixture-key", nil, http.Header{}, body, "")
	if shared.Get("Session-Id") != "" || shared.Get("Thread-Id") != "" || shared.Get("Conversation_id") != "" {
		t.Fatalf("shared handshake carried a session: %v", shared)
	}
	if gjson.Get(shared.Get("X-Codex-Turn-Metadata"), "installation_id").String() != httpInstall || shared.Get("User-Agent") != httpHeaders.Get("User-Agent") {
		t.Fatal("shared handshake lost the device identity")
	}
	account.CustomHeaders = map[string]string{"User-Agent": "evil-ua", "X-Debug": "1"}
	blocked := NewExecutor().prepareWebsocketHeaders(context.Background(), "fixture-token", account, account.AccountID, "conversation-a", "fixture-key", nil, http.Header{}, body, "")
	if blocked.Get("User-Agent") == "evil-ua" || blocked.Get("User-Agent") != httpHeaders.Get("User-Agent") || blocked.Get("X-Debug") != "1" {
		t.Fatalf("custom headers = UA %q debug %q", blocked.Get("User-Agent"), blocked.Get("X-Debug"))
	}
}
