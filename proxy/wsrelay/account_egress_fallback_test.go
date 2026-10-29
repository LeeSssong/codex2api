package wsrelay

import (
	"context"
	"github.com/codex2api/auth"
	"github.com/codex2api/internal/egressroute"
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAccountProxyUnavailableFallsBackBeforeWebsocketFrame(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if e != nil {
			return
		}
		defer c.Close()
		for {
			if _, _, e = c.ReadMessage(); e != nil {
				return
			}
		}
	}))
	defer upstream.Close()
	m := NewManager()
	defer m.Stop()
	a := &auth.Account{DBID: 96511, ProxyURL: "http://127.0.0.1:1"}
	ctx, cancel := context.WithTimeout(egressroute.WithAudit(context.Background()), 3*time.Second)
	defer cancel()
	wc, pr, err := m.AcquireConnection(ctx, a, "ws"+strings.TrimPrefix(upstream.URL, "http"), "fallback-test", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	wc.session.RemovePendingRequest(pr.RequestID)
	state := wc.ensureReadState()
	state.mu.Lock()
	state.activeLease = ""
	state.leasePhase = readLeaseIdle
	state.leaseWrite = nil
	state.leaseTerminalQueued = false
	state.mu.Unlock()
	m.ReleaseConnection(wc)
	got := egressroute.Snapshot(ctx)
	if got.Kind != "direct" || got.FallbackFrom != "account_proxy" {
		t.Fatalf("audit %+v", got)
	}
	if !strings.Contains(wc.PoolKey, "fallback-direct") {
		t.Fatalf("fallback contaminated bound pool %q", wc.PoolKey)
	}
	second, pending, err := m.AcquireConnection(ctx, a, "ws"+strings.TrimPrefix(upstream.URL, "http"), "fallback-test", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	second.session.RemovePendingRequest(pending.RequestID)
	if second != wc {
		t.Fatal("fallback created another connection instead of reusing the safe direct route")
	}
}
