package proxy

import (
	"context"
	"github.com/codex2api/auth"
	"github.com/codex2api/internal/egressroute"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestAccountProxyPriorityAndDirectFallback(t *testing.T) {
	t.Setenv("CODEX_TRANSPORT_MODE", "standard")
	var direct, bound, inherited atomic.Int32
	server := func(label string, n *atomic.Int32) *httptest.Server {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { n.Add(1); io.WriteString(w, label) }))
		t.Cleanup(s.Close)
		return s
	}
	target := server("direct", &direct)
	p := server("bound", &bound)
	other := server("inherited", &inherited)
	a := &auth.Account{DBID: 96501, ProxyURL: p.URL}
	withResin(t, &ResinConfig{BaseURL: other.URL, PlatformName: "test"})
	for _, client := range []*http.Client{getPooledClient(a, other.URL), getCodexMaintenanceClient(a, other.URL), ResolveCodexEgress(a, target.URL, other.URL).Client(), getRequestClient(WithAccountBoundEgress(context.Background(), ""), a, other.URL)} {
		ctx := egressroute.WithAudit(context.Background())
		req, _ := http.NewRequestWithContext(ctx, "GET", target.URL, nil)
		r, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if string(b) != "bound" {
			t.Fatalf("got %s", b)
		}
	}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	dead := "http://" + listener.Addr().String()
	listener.Close()
	a.ProxyURL = dead
	ctx := egressroute.WithAudit(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", target.URL, nil)
	r, e := getPooledClient(a, other.URL).Do(req)
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	if direct.Load() != 1 || bound.Load() != 4 || inherited.Load() != 0 {
		t.Fatalf("direct=%d bound=%d inherited=%d", direct.Load(), bound.Load(), inherited.Load())
	}
	if got := egressroute.Snapshot(ctx); got.Kind != "direct" || got.FallbackFrom != "account_proxy" {
		t.Fatalf("%+v", got)
	}
}
