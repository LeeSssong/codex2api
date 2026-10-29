package proxy

import (
	"context"
	"fmt"
	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/codex2api/egressipv6"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeIPv6RequiresAccountMembership(t *testing.T) {
	if NativeIPv6Account(&auth.Account{DBID: 1}) {
		t.Fatal("legacy native account enrolled implicitly")
	}
}

func TestIPv6MembershipWithoutManagerFailsClosed(t *testing.T) {
	previous := egressipv6.Current()
	egressipv6.Install(nil)
	t.Cleanup(func() { egressipv6.Install(previous) })
	account := &auth.Account{DBID: 1, IPv6EgressEnabled: true, IPv6EgressConfigured: true}
	base := &http.Client{}
	client := wrapIPv6Client(account, base, false, false)
	req, _ := http.NewRequest("GET", "https://example.invalid", nil)
	if _, err := client.Transport.RoundTrip(req); err == nil {
		t.Fatal("missing manager allowed fallback")
	}
	route := resolveIPv6Websocket(context.Background(), account, CodexEgress{})
	if route.Err == nil {
		t.Fatal("websocket allowed missing manager")
	}
}

func TestEnrolledStateDirectCannotBypassIPv6(t *testing.T) {
	previous := egressipv6.Current()
	egressipv6.Install(nil)
	t.Cleanup(func() { egressipv6.Install(previous) })
	a := &auth.Account{DBID: 1, IPv6EgressEnabled: true, IPv6EgressConfigured: true}
	e := ResolveCodexEgress(a, "https://example.invalid", ipv6StateDirectRoute)
	req, _ := http.NewRequest("GET", "https://example.invalid", nil)
	if _, err := e.Client().Transport.RoundTrip(req); err == nil {
		t.Fatal("State-direct HTTP bypassed membership")
	}
	e = ResolveCodexRequestEgress(context.Background(), a, "wss://example.invalid", ipv6StateDirectRoute, true)
	if e.Err == nil {
		t.Fatal("State-direct WS bypassed membership")
	}
}

func TestIPv6OAuthUsesDurableMembershipAndNeverReplays(t *testing.T) {
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "oauth-ipv6.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	id, err := db.InsertAccountWithCredentials(ctx, "oauth", map[string]interface{}{"access_token": "fixture", "ipv6_egress_enabled": false}, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := db.IPv6EgressConfig(ctx)
	cfg.Enabled = true
	db.SaveIPv6EgressConfig(ctx, cfg)
	manager := egressipv6.New(db)
	manager.LocalIPs = func() ([]string, error) { return []string{"2604::1", "2604::2"}, nil }
	old := egressipv6.Current()
	egressipv6.Install(manager)
	defer egressipv6.Install(old)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(201) }))
	defer server.Close()
	baseCalls := 0
	base := &http.Client{Transport: ipv6TestRoundTrip(func(*http.Request) (*http.Response, error) {
		baseCalls++
		return nil, fmt.Errorf("legacy transport used")
	})}
	client := WrapOAuthIPv6Client(id, base, false)
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 201 || baseCalls != 0 {
		t.Fatal("explicit false did not use host IPv4")
	}
	db.UpdateCredentials(ctx, id, map[string]interface{}{"ipv6_egress_enabled": true})
	cfg, _ = db.IPv6EgressConfig(ctx)
	cfg.Enabled = false
	db.SaveIPv6EgressConfig(ctx, cfg)
	if fallback, e := client.Get(server.URL); e != nil {
		t.Fatal(e)
	} else {
		fallback.Body.Close()
	}
	cfg, _ = db.IPv6EgressConfig(ctx)
	cfg.Enabled = true
	db.SaveIPv6EgressConfig(ctx, cfg)
	for _, ip := range []string{"2604::1", "2604::2"} {
		key := fmt.Sprintf("ipv6|%d|%s|%s|%s", id, ip, codexTransportModeStandard, "oauth-refresh")
		entry := &poolEntry{client: &http.Client{Transport: ipv6TestRoundTrip(func(*http.Request) (*http.Response, error) {
			baseCalls++
			return &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":"server_error"}`))}, nil
		})}}
		entry.touch()
		clientPool.Store(key, entry)
		defer clientPool.Delete(key)
	}
	resp, err = client.Post(server.URL, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 503 || baseCalls != 1 {
		t.Fatalf("OAuth replayed: status=%d calls=%d", resp.StatusCode, baseCalls)
	}
}

type ipv6TestRoundTrip func(*http.Request) (*http.Response, error)

func (f ipv6TestRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestIPv6MaintenanceAndBoundTelemetryCannotBypassSelection(t *testing.T) {
	old := egressipv6.Current()
	egressipv6.Install(nil)
	defer egressipv6.Install(old)
	a := &auth.Account{DBID: 4561, IPv6EgressEnabled: true, IPv6EgressConfigured: true}
	ctx := WithAccountBoundEgress(context.Background(), "")
	for _, client := range []*http.Client{getCodexMaintenanceClient(a, ""), getRequestClient(ctx, a, "")} {
		req, _ := http.NewRequest("GET", "https://fixture.invalid", nil)
		if _, err := client.Transport.RoundTrip(req); err == nil {
			t.Fatal("membership bypass")
		}
	}
	if route := ResolveCodexRequestEgress(ctx, a, "wss://fixture.invalid", "", true); route.Err == nil {
		t.Fatal("bound telemetry WS bypass")
	}
}
