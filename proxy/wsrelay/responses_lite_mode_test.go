package wsrelay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/proxy"
	"github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const liteModeMetadataPath = "client_metadata.ws_request_header_x_openai_internal_codex_responses_lite"

// Models the upstream behavior reproduced on 2026-10-28: once a socket has
// served Lite, removing its per-frame flag does NOT restore hosted-tool support.
type liteModeObservation struct {
	socket  int32
	headers http.Header
	body    []byte
}

type liteModeFixture struct {
	executor *Executor
	account  *auth.Account
	ctx      context.Context
	frames   chan liteModeObservation
	dials    atomic.Int32
}

func newLiteModeFixture(t *testing.T, retainIDs bool) *liteModeFixture {
	t.Helper()
	t.Setenv("CODEX_WS_STATELESS_ONESHOT", "false")
	oldSettings := proxy.CurrentRuntimeSettings()
	settings := oldSettings
	settings.CodexWSStatelessSlots = 8
	proxy.ApplyRuntimeSettings(settings)
	t.Cleanup(func() { proxy.ApplyRuntimeSettings(oldSettings) })
	oldResin := proxy.GetResinConfig()
	t.Cleanup(func() { proxy.SetResinConfig(oldResin) })
	f := &liteModeFixture{frames: make(chan liteModeObservation, 32), account: &auth.Account{DBID: 42, AccessToken: "synthetic-token", DynamicConcurrencyLimit: 16}}
	var responses atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		socket := f.dials.Add(1)
		stickyLite := false
		for {
			_, body, err := conn.ReadMessage()
			if err != nil {
				return
			}
			f.frames <- liteModeObservation{socket: socket, headers: r.Header.Clone(), body: body}
			if strings.EqualFold(gjson.GetBytes(body, liteModeMetadataPath).String(), "true") {
				stickyLite = true
			}
			web := false
			for _, tool := range gjson.GetBytes(body, "tools").Array() {
				web = web || tool.Get("type").String() == "web_search"
			}
			if stickyLite && web {
				if conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"error","status":400,"error":{"message":"Hosted tool 'web_search' requires authorization and metering that are not supported by rustponsesapi.","type":"invalid_request_error","param":"tools","code":"unsupported_parameter"}}`)) != nil {
					return
				}
				continue
			}
			if web {
				if conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.output_item.done","item":{"type":"web_search_call","status":"completed"}}`)) != nil {
					return
				}
			}
			id := ""
			if retainIDs {
				id = fmt.Sprintf("resp_mode_%d", responses.Add(1))
			}
			completed := fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"status":"completed","output":[]}}`, id)
			if conn.WriteMessage(websocket.TextMessage, []byte(completed)) != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	proxy.SetResinConfig(&proxy.ResinConfig{BaseURL: server.URL, PlatformName: "lite-mode-local-test"})
	manager := NewManager()
	t.Cleanup(manager.Stop)
	manager.probeFunc = func(*WsConnection) bool { return true }
	f.executor = NewExecutorWithManager(manager)
	var cancel context.CancelFunc
	f.ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return f
}

func liteModeBody(lite, web bool) []byte {
	body := []byte(`{"model":"gpt-5.6-sol","input":[{"role":"user","content":"retained conversation"}],"prompt_cache_key":"application-cache","client_metadata":{"session_id":"application-session","thread_id":"application-thread"},"tools":[]}`)
	if lite {
		body, _ = sjson.SetBytes(body, liteModeMetadataPath, "true")
	}
	if web {
		body, _ = sjson.SetRawBytes(body, "tools", []byte(`[{"type":"web_search","search_context_size":"low"}]`))
	}
	return body
}

func (f *liteModeFixture) request(t *testing.T, body []byte, stateless bool) (*WsResponse, error) {
	t.Helper()
	session, route := "explicit-session", ""
	if stateless {
		session, route = "stateless-mode-fixture", "reusable-api-key-pool"
	}
	return f.executor.ExecuteRequestViaWebsocket(f.ctx, f.account, body, session, "", "synthetic-key", nil, http.Header{}, route)
}

func finishLiteModeResponse(t *testing.T, response *WsResponse, wantSearch bool) string {
	t.Helper()
	defer response.Close()
	terminal, search, responseID := "", false, ""
	if err := response.ReadStream(func(body []byte) bool {
		typ := gjson.GetBytes(body, "type").String()
		if typ == "error" || typ == "response.failed" {
			t.Errorf("hosted search reached an incompatible socket: %s", body)
		}
		search = search || gjson.GetBytes(body, "item.type").String() == "web_search_call"
		if typ == "response.completed" {
			terminal = typ
			responseID = gjson.GetBytes(body, "response.id").String()
		}
		return true
	}); err != nil {
		t.Fatalf("read synthetic upstream: %v", err)
	}
	if terminal != "response.completed" || search != wantSearch {
		t.Fatalf("terminal=%q search=%v, want completed and search=%v", terminal, search, wantSearch)
	}
	return responseID
}

func (f *liteModeFixture) observation(t *testing.T) liteModeObservation {
	t.Helper()
	select {
	case frame := <-f.frames:
		return frame
	case <-f.ctx.Done():
		t.Fatal("synthetic upstream did not receive frame")
		return liteModeObservation{}
	}
}

func TestResponsesLiteModeIsolatesWebsocketPools(t *testing.T) {
	for _, stateless := range []bool{false, true} {
		for _, liteFirst := range []bool{true, false} {
			t.Run(fmt.Sprintf("stateless=%v/lite_first=%v", stateless, liteFirst), func(t *testing.T) {
				f := newLiteModeFixture(t, false)
				seen := make(map[bool]*WsConnection)
				sockets := make(map[bool]int32)
				for _, lite := range []bool{liteFirst, !liteFirst, !liteFirst, liteFirst} {
					body := liteModeBody(lite, !lite)
					original := bytes.Clone(body)
					response, err := f.request(t, body, stateless)
					if err != nil {
						t.Fatal(err)
					}
					conn := response.conn
					finishLiteModeResponse(t, response, !lite)
					frame := f.observation(t)
					if !bytes.Equal(body, original) {
						t.Fatal("transport selection mutated original conversation")
					}
					if previous := seen[lite]; previous != nil && previous != conn {
						t.Fatal("same-mode reuse stopped working")
					}
					seen[lite], sockets[lite] = conn, frame.socket
					if seen[!lite] != nil && seen[!lite] == conn {
						t.Fatal("Lite and standard requests share a socket")
					}
					for _, path := range []string{"input", "tools", "client_metadata.session_id", "client_metadata.thread_id", liteModeMetadataPath} {
						if gjson.GetBytes(frame.body, path).Raw != gjson.GetBytes(body, path).Raw {
							t.Fatalf("transport isolation changed outbound %s: %s", path, frame.body)
						}
					}
					wantCache := "explicit-session"
					if stateless {
						wantCache = "application-cache"
					}
					if gjson.GetBytes(frame.body, "prompt_cache_key").String() != wantCache {
						t.Fatal("mode selection changed existing cache-key behavior")
					}
					if bytes.Contains(frame.body, []byte("|responses:")) {
						t.Fatal("local mode scope leaked into upstream body")
					}
					for name, values := range frame.headers {
						if strings.Contains(strings.Join(values, ""), "|responses:") || strings.Contains(strings.ToLower(name), "responses-lite") {
							t.Fatalf("local transport mode leaked in upstream header %s", name)
						}
					}
				}
				if f.dials.Load() != 2 || sockets[true] == sockets[false] {
					t.Fatalf("want two reusable mode-specific sockets, dials=%d sockets=%v", f.dials.Load(), sockets)
				}
			})
		}
	}
}

func TestResponsesLiteModeContinuationSafety(t *testing.T) {
	for _, stateless := range []bool{false, true} {
		for _, originalLite := range []bool{false, true} {
			t.Run(fmt.Sprintf("stateless=%v/original_lite=%v", stateless, originalLite), func(t *testing.T) {
				f := newLiteModeFixture(t, true)
				body := liteModeBody(originalLite, false)
				first, err := f.request(t, body, stateless)
				if err != nil {
					t.Fatal(err)
				}
				original := first.conn
				id := finishLiteModeResponse(t, first, false)
				f.observation(t)
				// Reject BEFORE acquiring/sending/redialing. Keep the original response
				// binding and read lease healthy so matching-mode continuation still works.
				changed := liteModeBody(!originalLite, originalLite)
				changed, _ = sjson.SetBytes(changed, "previous_response_id", id)
				snapshot := bytes.Clone(changed)
				response, err := f.request(t, changed, stateless)
				if response != nil {
					response.Close()
				}
				var lost *proxy.ResponsesContinuationLostError
				if !errors.As(err, &lost) {
					t.Fatalf("cross-mode continuation must request existing full-context recovery, got %v", err)
				}
				if !bytes.Equal(changed, snapshot) || f.dials.Load() != 1 || len(f.frames) != 0 || original.session.PendingCount() != 0 || !canReuseConnection(original) {
					t.Fatal("mode mismatch changed history, sent a stale ID, redialed or leaked a lease")
				}
				if got, _ := f.executor.manager.lookupResponseConn(id, f.account.ID(), "synthetic-key"); got != original {
					t.Fatal("mode mismatch destroyed original binding")
				}
				matching, _ := sjson.SetBytes(body, "previous_response_id", id)
				next, err := f.request(t, matching, stateless)
				if err != nil {
					t.Fatal(err)
				}
				if next.conn != original {
					t.Fatal("matching-mode continuation changed original socket")
				}
				finishLiteModeResponse(t, next, false)
				frame := f.observation(t)
				if gjson.GetBytes(frame.body, "previous_response_id").String() != id {
					t.Fatal("same-mode continuation dropped its original ID")
				}
			})
		}
	}
}

func TestResponsesLiteModeIdentityUsesFinalFrame(t *testing.T) {
	headers := http.Header{"User-Agent": {"codex/fixture"}, "Version": {"1.0"}, "Originator": {"codex-tui"}}
	// Even a stale ingress header must not override the finalized WS signal.
	headers.Set("X-OpenAI-Internal-Codex-Responses-Lite", "true")
	liteParent := withWebsocketResponsesLiteMode(context.Background(), liteModeBody(true, false))
	for _, tc := range []struct {
		name, marker string
		want         bool
	}{
		{"absent", "", false}, {"string", `"true"`, true}, {"case_and_spaces", `" TRUE "`, true},
		{"boolean", "true", true}, {"false", "false", false}, {"null", "null", false}, {"other", `"enabled"`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"model":"gpt-5.6-sol"}`)
			if tc.marker != "" {
				body, _ = sjson.SetRawBytes(body, liteModeMetadataPath, []byte(tc.marker))
			}
			copyBody := bytes.Clone(body)
			ctx := withWebsocketResponsesLiteMode(liteParent, body)
			got := websocketResponsesLiteFromContext(ctx)
			if got != tc.want || !bytes.Equal(body, copyBody) {
				t.Fatal("mode did not follow final frame or mutated caller body")
			}
			id, key := websocketTransportIdentity(headers, got), websocketTransportPoolKey("session", headers, got)
			if key != "session|client:"+id {
				t.Fatal("pool and dial identity disagree")
			}
			if got && id == websocketClientIdentity(headers) {
				t.Fatal("Lite has no protocol scope")
			}
			if !got && (id != websocketClientIdentity(headers) || key != websocketClientPoolKey("session", headers)) {
				t.Fatal("standard transport identity changed")
			}
			if websocketTransportPoolKey("", headers, got) != "" {
				t.Fatal("empty stateless pool key changed")
			}
		})
	}
	if !websocketResponsesLiteFromContext(liteParent) || websocketResponsesLiteFromContext(context.Background()) {
		t.Fatal("child frame mode modified its parent/default context")
	}
}

func TestResponsesLiteModeSendRetryKeepsDialIdentity(t *testing.T) {
	for _, lite := range []bool{false, true} {
		t.Run(fmt.Sprintf("lite=%v", lite), func(t *testing.T) {
			f := newLiteModeFixture(t, false)
			var stored atomic.Int32
			var failedConn *WsConnection
			f.executor.manager.afterConnectionStored = func(wc *WsConnection) {
				if stored.Add(1) != 1 {
					return
				}
				failedConn = wc
				// Set gorilla's cached write failure without disconnecting its reader.
				// The first business send must then exercise the executor's redial loop.
				_ = wc.conn.SetWriteDeadline(time.Now().Add(-time.Second))
				if err := wc.conn.WriteMessage(websocket.PingMessage, nil); err == nil {
					t.Error("fixture did not create a send error")
				}
				_ = wc.conn.SetWriteDeadline(time.Time{})
			}
			response, err := f.request(t, liteModeBody(lite, !lite), false)
			if err != nil {
				t.Fatal(err)
			}
			if response.conn == failedConn || stored.Load() != 2 {
				t.Fatal("send error did not redial exactly once")
			}
			frame := f.observation(t)
			if got, want := response.conn.upstreamClientIdentity, websocketTransportIdentity(frame.headers, lite); got != want {
				t.Fatalf("replacement lost protocol identity: %s want %s", got, want)
			}
			finishLiteModeResponse(t, response, !lite)
			if strings.EqualFold(gjson.GetBytes(frame.body, liteModeMetadataPath).String(), "true") != lite || f.dials.Load() != 2 || len(f.frames) != 0 {
				t.Fatal("redial changed mode, sent duplicate business frames or dialed again")
			}
		})
	}
}

func TestResponsesLiteModeScopesStatelessFallbackLanes(t *testing.T) {
	f := newLiteModeFixture(t, false)
	settings := proxy.CurrentRuntimeSettings()
	settings.CodexWSStatelessSlots = 1
	proxy.ApplyRuntimeSettings(settings)
	var responses []*WsResponse
	for _, lite := range []bool{true, true, false, false} {
		response, err := f.request(t, liteModeBody(lite, !lite), true)
		if err != nil {
			t.Fatalf("acquire mode=%v after %d requests: %v", lite, len(responses), err)
		}
		t.Logf("mode=%v session=%s", lite, response.sessionID)
		t.Cleanup(func() { response.Close() })
		responses = append(responses, response)
	}
	seen := map[*WsConnection]bool{}
	for i, response := range responses {
		if seen[response.conn] {
			t.Fatal("in-flight slot/fallback lanes shared a socket")
		}
		seen[response.conn] = true
		if lite := i < 2; strings.Contains(response.sessionID, "|responses:lite") != lite {
			t.Fatal("fallback lane lost local mode scope")
		}
		finishLiteModeResponse(t, response, i >= 2)
		frame := f.observation(t)
		if strings.EqualFold(gjson.GetBytes(frame.body, liteModeMetadataPath).String(), "true") != (i < 2) {
			t.Fatal("fallback sent the wrong mode")
		}
	}
	if f.dials.Load() != 4 {
		t.Fatalf("want four distinct in-flight sockets, got %d", f.dials.Load())
	}
}

func TestResponsesLiteModeScopesBusyOverflowLanes(t *testing.T) {
	f := newLiteModeFixture(t, false)
	settings := proxy.CurrentRuntimeSettings()
	settings.CodexWSBusyOverflow = true
	settings.CodexWSBusyPatienceSec = 0
	proxy.ApplyRuntimeSettings(settings)
	var responses []*WsResponse
	for _, lite := range []bool{true, true, false, false} {
		response, err := f.request(t, liteModeBody(lite, !lite), false)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { response.Close() })
		responses = append(responses, response)
	}
	seen := map[*WsConnection]bool{}
	for i, response := range responses {
		if seen[response.conn] {
			t.Fatal("busy explicit lanes shared a socket")
		}
		seen[response.conn] = true
		if strings.Contains(response.sessionID, "|responses:lite") != (i < 2) || isBusyOverflowSessionKey(response.sessionID) != (i%2 == 1) {
			t.Fatalf("busy overflow lost mode scope or lane: %s", response.sessionID)
		}
		finishLiteModeResponse(t, response, i >= 2)
		f.observation(t)
	}
	if f.dials.Load() != 4 {
		t.Fatalf("want original+overflow sockets per mode, got %d", f.dials.Load())
	}
}
