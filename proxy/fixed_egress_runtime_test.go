package proxy

import (
	"context"
	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/codex2api/fixedegress"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func setupFixedRuntime(t *testing.T, urls []string) (*database.DB, *fixedegress.Manager, *auth.Account) {
	t.Helper()
	t.Setenv("CODEX_FIXED_EGRESS_KEY", "runtime-test")
	t.Setenv("CODEX_TRANSPORT_MODE", "standard")
	db, e := database.New("sqlite", filepath.Join(t.TempDir(), "fixed.db"))
	if e != nil {
		t.Fatal(e)
	}
	old := fixedegress.Current()
	m := fixedegress.NewManager(db, nil)
	fixedegress.Install(m)
	t.Cleanup(func() { fixedegress.Install(old); db.Close() })
	ctx := context.Background()
	id, e := db.InsertAccount(ctx, "runtime-account", "rt", "")
	if e != nil {
		t.Fatal(e)
	}
	db.SaveFixedEgressConfig(ctx, database.FixedEgressConfig{Enabled: true})
	db.SaveFixedEgressPool(ctx, database.FixedEgressPool{ID: "p", Enabled: true, Strategy: "stable", CooldownSeconds: 600, MaxAttempts: 3})
	for i, u := range urls {
		if e = db.SaveFixedEgressResource(ctx, database.FixedEgressResource{ID: string(rune('a' + i)), PoolID: "p", ProxyURL: u, ObservedExitIP: []string{"8.8.8.8", "9.9.9.9"}[i], State: "healthy"}); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = m.Enroll(ctx, id, "p"); e != nil {
		t.Fatal(e)
	}
	return db, m, &auth.Account{DBID: id, AccessToken: "access"}
}
func TestFixedRuntimeHTTPMaintenanceOAuthAndNoDirect(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.Write([]byte("through-fixed-proxy")) }))
	defer s.Close()
	db, _, a := setupFixedRuntime(t, []string{s.URL})
	base := &http.Client{Transport: newCodexStandardTransport("")}
	for _, client := range []*http.Client{ResolveCodexEgress(a, "http://unreachable.example/", "").Client(), getMaintenanceClient(a, "", "test-fixed", false), WrapOAuthFixedClient(a.ID(), base, false)} {
		resp, e := client.Get("http://unreachable.example/")
		if e != nil {
			t.Fatal(e)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(body) != "through-fixed-proxy" {
			t.Fatal("wrong route")
		}
	}
	if calls != 3 {
		t.Fatal(calls)
	}
	cfg, _ := db.GetFixedEgressConfig(context.Background())
	db.SetFixedEgressConfigRevision(context.Background(), false, cfg.Revision)
	_, e := WrapOAuthFixedClient(a.ID(), base, false).Get("http://unreachable.example/")
	if e == nil || calls != 3 {
		t.Fatal("disabled enrolled route fell through")
	}
}
func TestFixedRuntimeQuotaDoesNotRotateAndNoReplayOAuth(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(429)
		w.Write([]byte(`{"error":{"code":"insufficient_quota"}}`))
	}))
	defer s.Close()
	spare := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("quota incorrectly replayed") }))
	defer spare.Close()
	db, _, a := setupFixedRuntime(t, []string{s.URL, spare.URL})
	p, _ := db.GetFixedEgressPool(context.Background(), "p")
	p.Rotate429 = true
	db.SaveFixedEgressPool(context.Background(), p)
	base := &http.Client{Transport: newCodexStandardTransport("")}
	resp, e := WrapOAuthFixedClient(a.ID(), base, false).Post("http://unreachable.example/refresh", "application/json", strings.NewReader(`{}`))
	if e != nil {
		t.Fatal(e)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "insufficient_quota") || calls != 1 {
		t.Fatal("body/replay changed")
	}
	binding, _ := db.ResolveFixedEgress(context.Background(), a.ID())
	if binding.ResourceID != "a" {
		t.Fatal("quota rotated")
	}
}
func TestFixedRuntimeConnectionFailureUsesSpare(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	badURL := bad.URL
	bad.Close()
	calls := 0
	spare := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.Write([]byte("ok")) }))
	defer spare.Close()
	db, _, a := setupFixedRuntime(t, []string{badURL, spare.URL})
	resp, e := ResolveCodexEgress(a, "http://unreachable.example/", "").Client().Get("http://unreachable.example/")
	if e != nil {
		t.Fatal(e)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	b, _ := db.ResolveFixedEgress(context.Background(), a.ID())
	if b.ResourceID != "b" || calls != 1 {
		t.Fatal("did not switch", b, calls)
	}
}
