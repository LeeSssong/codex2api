package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestStateProbeConnectionsAreIndependentAndClosedWithBody(t *testing.T) {
	t.Setenv("CODEX_TRANSPORT_MODE", "standard")
	var mu sync.Mutex
	remotes := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		remotes[r.RemoteAddr] = true
		mu.Unlock()
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	for range 2 {
		client, finish, err := stateProbeClient(WithFreshStateProbe(context.Background()), CodexEgress{URL: server.URL})
		if err != nil {
			t.Fatal(err)
		}
		if client.Timeout != 0 || client.Transport.(*http.Transport).ResponseHeaderTimeout != 0 {
			t.Fatal("probe introduced a response timeout")
		}
		resp, err := client.Get(server.URL)
		if err != nil {
			finish(nil)
			t.Fatal(err)
		}
		finish(resp)
		if _, err := io.ReadAll(resp.Body); err != nil {
			t.Fatal(err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	count := len(remotes)
	mu.Unlock()
	if count != 2 {
		t.Fatal("candidate reused another candidate's connection")
	}
	if _, _, err := stateProbeClient(WithFreshStateProbe(context.Background()), CodexEgress{Kind: CodexEgressResin}); err == nil {
		t.Fatal("proxy selection silently overridden by Resin")
	}
}
