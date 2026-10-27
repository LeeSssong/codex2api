package wsrelay

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/proxy"
	"github.com/gorilla/websocket"
)

// Quota changes while acquiring a connection must prevent response.create, not
// retry the deferred send or consume a pending lease indefinitely.
func TestAutomaticIntelligenceCooldownDuringWebsocketHandshake(t *testing.T) {
	account := &auth.Account{DBID: 998281, AccessToken: "fixture-token"}
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
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, _, err = conn.ReadMessage(); err == nil {
			frames.Add(1)
		}
	}))
	defer upstream.Close()
	old := proxy.GetResinConfig()
	proxy.SetResinConfig(&proxy.ResinConfig{BaseURL: upstream.URL, PlatformName: "intelligence-local"})
	t.Cleanup(func() { proxy.SetResinConfig(old) })
	manager := NewManager()
	defer manager.Stop()
	executor := NewExecutorWithManager(manager)
	ctx, cancel := context.WithTimeout(proxy.WithAutomaticIntelligenceCheck(context.Background()), 3*time.Second)
	defer cancel()
	_, err := executor.ExecuteRequestViaWebsocket(ctx, account, []byte(`{"model":"gpt-5.4","input":[]}`), "cooldown-handshake", "", "", nil, http.Header{}, "")
	if !errors.Is(err, proxy.ErrAutomaticIntelligenceCheckDeferred) || handshakes.Load() != 1 || frames.Load() != 0 {
		t.Fatalf("deferred websocket: err=%v handshakes=%d frames=%d", err, handshakes.Load(), frames.Load())
	}
	if shouldRetryWebsocketSendError(err) {
		t.Fatal("deferred dispatch must not retry")
	}
	count := 0
	manager.connections.Range(func(_, _ any) bool { count++; return true })
	if count != 0 {
		t.Fatalf("deferred connection leaked: %d", count)
	}
}

func TestAutomaticIntelligenceCooldownWhileWaitingForWriteLock(t *testing.T) {
	var frames atomic.Int32
	_, wc := newReadPumpTestConnection(t, func(conn *websocket.Conn) {
		if _, _, err := conn.ReadMessage(); err == nil {
			frames.Add(1)
		}
	})
	account := &auth.Account{}
	ctx := proxy.WithAutomaticIntelligenceCheck(context.Background())
	// A heartbeat owns writeMu. The send has passed the same outer check used by
	// executeRequestViaWebsocket, but must recheck after acquiring this lock.
	wc.writeMu.Lock()
	if err := proxy.CheckAutomaticIntelligenceCheckDispatch(ctx, account); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- (&Executor{}).sendRequest(wc, []byte(`{"type":"response.create"}`), "cooldown-lock", func() error { return proxy.CheckAutomaticIntelligenceCheckDispatch(ctx, account) })
	}()
	account.Mu().Lock()
	account.Status = auth.StatusCooldown
	account.CooldownUtil = time.Now().Add(time.Hour)
	account.Mu().Unlock()
	wc.writeMu.Unlock()
	select {
	case err := <-done:
		if !errors.Is(err, proxy.ErrAutomaticIntelligenceCheckDeferred) || frames.Load() != 0 {
			t.Fatalf("write-lock cooldown: err=%v frames=%d", err, frames.Load())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("send did not release write lock")
	}
	wc.cancelUnsentReadLease("cooldown-lock")
}
