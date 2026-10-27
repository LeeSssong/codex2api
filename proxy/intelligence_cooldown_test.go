package proxy

import (
	"context"
	"errors"
	"github.com/codex2api/auth"
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestAutomaticIntelligenceHTTPDispatchCooldownAndManualBypass(t *testing.T) {
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(204) }))
	defer upstream.Close()
	account := &auth.Account{Status: auth.StatusCooldown, CooldownUtil: time.Now().Add(time.Hour)}
	req, _ := http.NewRequestWithContext(WithAutomaticIntelligenceCheck(context.Background()), "POST", upstream.URL, nil)
	resp, err := doTracedUpstreamRequest(upstream.Client(), req, account, "")
	if !errors.Is(err, ErrAutomaticIntelligenceCheckDeferred) || resp != nil || requests.Load() != 0 {
		t.Fatalf("cooldown dispatch: %v requests=%d", err, requests.Load())
	}
	req, _ = http.NewRequestWithContext(WithCodexDetectorRequest(context.Background()), "POST", upstream.URL, nil)
	resp, err = doTracedUpstreamRequest(upstream.Client(), req, account, "")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if requests.Load() != 1 {
		t.Fatalf("manual blocked: %d", requests.Load())
	}
}

func TestAutomaticIntelligenceRelayWebsocketCooldownDuringHandshake(t *testing.T) {
	account := &auth.Account{DBID: 998282}
	var handshakes, frames atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handshakes.Add(1)
		account.Mu().Lock()
		account.Status = auth.StatusCooldown
		account.CooldownUtil = time.Now().Add(time.Hour)
		account.Mu().Unlock()
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		conn.SetReadDeadline(time.Now().Add(time.Second))
		if _, _, err = conn.ReadMessage(); err == nil {
			frames.Add(1)
		}
	}))
	defer upstream.Close()
	defer resetOpenAIResponsesWebsocketPool()
	ctx, cancel := context.WithTimeout(WithAutomaticIntelligenceCheck(context.Background()), 3*time.Second)
	defer cancel()
	resp, err := executeOpenAIResponsesWebsocket(ctx, account, []byte(`{"model":"gpt-5.4","input":[]}`), "", nil, upstream.URL, "fixture")
	if resp != nil {
		resp.Body.Close()
	}
	if !errors.Is(err, ErrAutomaticIntelligenceCheckDeferred) || handshakes.Load() != 1 || frames.Load() != 0 {
		t.Fatalf("relay websocket dispatched: err=%v handshakes=%d frames=%d", err, handshakes.Load(), frames.Load())
	}
}
