package auth

import (
	"context"
	"github.com/codex2api/internal/egressroute"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOAuthProxyUnavailableFallsBackDirect(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "direct") }))
	defer s.Close()
	ctx := egressroute.WithAudit(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", s.URL, nil)
	resp, err := buildHTTPClient("http://127.0.0.1:1").Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := egressroute.Snapshot(ctx); got.Kind != "direct" || got.FallbackFrom != "account_proxy" {
		t.Fatalf("%+v", got)
	}
}
func TestOAuthBoundContextSuppressesResin(t *testing.T) {
	old := ResinRequestDecorator
	defer func() { ResinRequestDecorator = old }()
	ResinRequestDecorator = func(u, id string) string { return "http://resin.invalid" }
	if useResinForOAuth(WithAccountBoundEgress(context.Background(), "http://bound.invalid"), "7") {
		t.Fatal("bound OAuth was rewritten to Resin")
	}
}

func TestOAuthFallbackClientRetainsPooledIdentity(t *testing.T) {
	const proxyURL = "http://pool-identity.invalid:18080"
	first := BuildHTTPClient(proxyURL)
	if next := BuildHTTPClient(proxyURL); first != next {
		t.Fatal("same route must reuse the fallback-aware auth client")
	}
	strict, err := BuildHTTPClientChecked(proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	if strict == first {
		t.Fatal("strict and fallback clients must not share policy")
	}
}
