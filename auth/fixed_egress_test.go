package auth

import (
	"context"
	"github.com/codex2api/database"
	"github.com/codex2api/fixedegress"
	"path/filepath"
	"testing"
)

func TestFixedEgressSchedulingIgnoresEmptyLegacyPool(t *testing.T) {
	t.Setenv("CODEX_FIXED_EGRESS_KEY", "test-fixed")
	db, e := database.New("sqlite", filepath.Join(t.TempDir(), "test.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	id, e := db.InsertAccount(ctx, "fixed", "rt", "")
	if e != nil {
		t.Fatal(e)
	}
	db.SaveFixedEgressConfig(ctx, database.FixedEgressConfig{Enabled: true})
	db.SaveFixedEgressPool(ctx, database.FixedEgressPool{ID: "p", Enabled: true, Strategy: "stable"})
	db.SaveFixedEgressResource(ctx, database.FixedEgressResource{ID: "r", PoolID: "p", ProxyURL: "http://127.0.0.1:10443", ObservedExitIP: "8.8.8.8"})
	m := fixedegress.NewManager(db, nil)
	old := fixedegress.Current()
	fixedegress.Install(m)
	defer fixedegress.Install(old)
	if _, e = m.Enroll(ctx, id, "p"); e != nil {
		t.Fatal(e)
	}
	s := NewStore(db, nil, nil)
	defer s.Stop()
	s.SetProxyPoolEnabled(true)
	a := &Account{DBID: id, AccessToken: "at", Status: StatusReady}
	s.AddAccount(a)
	p, usable := s.ResolveUsableProxyForAccount(a)
	if !usable || p != "http://127.0.0.1:10443" {
		t.Fatalf("fixed blocked by legacy pool %q %v", p, usable)
	}
	c, _ := db.GetFixedEgressConfig(ctx)
	db.SetFixedEgressConfigRevision(ctx, false, c.Revision)
	if _, usable = s.ResolveUsableProxyForAccount(a); usable {
		t.Fatal("disabled fixed routed via legacy")
	}
	if got := s.ResolveProxyForAccount(a); got == "" {
		t.Fatal("legacy string resolver turned fixed failure into direct")
	}

}
