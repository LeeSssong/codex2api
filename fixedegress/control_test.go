package fixedegress

import (
	"context"
	"github.com/codex2api/database"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func controlTest(t *testing.T) (*Manager, *database.DB) {
	t.Helper()
	t.Setenv("CODEX_FIXED_EGRESS_KEY", "control-test-key")
	db, e := database.New("sqlite", filepath.Join(t.TempDir(), "control.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	db.SaveFixedEgressPool(context.Background(), database.FixedEgressPool{ID: "pool", Enabled: true, Strategy: "stable"})
	return NewManager(db, nil), db
}
func TestControlStaticImportProbeAndNoHealthyRebalance(t *testing.T) {
	m, db := controlTest(t)
	ctx := context.Background()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.Write([]byte(`{"ip":"8.8.8.8"}`)) }))
	defer srv.Close()
	if e := m.Import(ctx, "pool", "http", 1, srv.Listener.Addr().String()); e != nil {
		t.Fatal(e)
	}
	rs, _ := db.ListFixedEgress(ctx)
	if len(rs) != 1 || rs[0].State != "pending" {
		t.Fatal(rs)
	}
	if e := m.testResource(ctx, rs[0].ID, "http://probe.example/ip"); e != nil {
		t.Fatal(e)
	}
	if e := m.Import(ctx, "pool", "http", 1, srv.Listener.Addr().String()); e != nil {
		t.Fatal(e)
	}
	after, _ := db.ListFixedEgress(ctx)
	if after[0].State != "healthy" || after[0].ObservedExitIP != "8.8.8.8" || calls != 1 {
		t.Fatal("import reset resource", after)
	}
}
func TestControlInventoryReconcilesUnknownAndPreservesMissing(t *testing.T) {
	m, db := controlTest(t)
	ctx := context.Background()
	p := database.FixedEgressProvider{ID: "provider", APIKey: "secret", BaseURL: "https://ipapi.cliproxy.com", Protocol: "http", AuthFormat: "3", PortStart: 10443, PortEnd: 10445, Capacity: 5, RefillIntervalSeconds: 30, Enabled: true}
	if e := db.SaveFixedEgressProvider(ctx, p); e != nil {
		t.Fatal(e)
	}
	if e := db.ReserveFixedEgressTask(ctx, database.FixedEgressTask{ID: "task", ProviderID: p.ID, Port: 10443}); e != nil {
		t.Fatal(e)
	}
	if e := m.syncInventory(ctx, p, "pool", []CliProxyEndpoint{{"http://u:p@203.0.113.1:10443", 10443}}); e != nil {
		t.Fatal(e)
	}
	rs, _ := db.ListFixedEgress(ctx)
	if len(rs) != 1 || !rs[0].Owned || rs[0].State != "pending" {
		t.Fatal(rs)
	}
	if e := m.syncInventory(ctx, p, "pool", nil); e != nil {
		t.Fatal(e)
	}
	after, _ := db.ListFixedEgress(ctx)
	if len(after) != 1 {
		t.Fatal("missing inventory deleted resource")
	}
	if e := db.ReserveFixedEgressTask(ctx, database.FixedEgressTask{ID: "again", ProviderID: p.ID, Port: 10443}); e == nil {
		t.Fatal("ambiguous port recycled")
	}
}
func TestControlReconcilesAmbiguousRemoteStop(t *testing.T) {
	m, db := controlTest(t)
	ctx := context.Background()
	p := database.FixedEgressProvider{ID: "provider", APIKey: "secret", BaseURL: "https://ipapi.cliproxy.com", Protocol: "http", AuthFormat: "3", PortStart: 10443, PortEnd: 10445, Capacity: 5, RefillIntervalSeconds: 30, Enabled: true}
	db.SaveFixedEgressProvider(ctx, p)
	db.ReserveFixedEgressTask(ctx, database.FixedEgressTask{ID: "task", ProviderID: p.ID, Port: 10443})
	m.syncInventory(ctx, p, "pool", []CliProxyEndpoint{{"http://u:p@203.0.113.1:10443", 10443}})
	rs, _ := db.ListFixedEgress(ctx)
	if e := db.RetireFixedEgressResource(ctx, rs[0].ID, true); e != nil {
		t.Fatal(e)
	}
	if e := m.syncInventory(ctx, p, "pool", nil); e != nil {
		t.Fatal(e)
	}
	ts, _ := db.ListFixedEgressTasks(ctx)
	if ts[0].State != "stopped" {
		t.Fatal("stop unknown not reconciled", ts)
	}
}
