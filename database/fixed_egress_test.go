package database

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixedTestDB(t *testing.T) *DB {
	t.Helper()
	t.Setenv("CODEX_FIXED_EGRESS_KEY", "fixed-test-key")
	driver, dsn := "sqlite", filepath.Join(t.TempDir(), "fixed.db")
	if testDSN := os.Getenv("CODEX_FIXED_TEST_DSN"); testDSN != "" {
		driver = "postgres"
		dsn = testDSN
	}
	db, e := New(driver, dsn)
	if e != nil {
		t.Fatal(e)
	}
	if driver == "postgres" {
		_, e = db.conn.Exec(`TRUNCATE fixed_egress_bindings,fixed_egress_enrollments,fixed_egress_resources,fixed_egress_tasks,fixed_egress_providers,fixed_egress_pools CASCADE`)
		if e != nil {
			t.Fatal(e)
		}
	}
	for _, id := range []int64{1, 2} {
		_, e = db.conn.Exec(`INSERT INTO accounts(id,name,credentials) VALUES($1,$2,'{}') ON CONFLICT(id) DO NOTHING`, id, "test-fixed")
		if e != nil {
			t.Fatal(e)
		}
	}
	if e = db.SaveFixedEgressConfig(context.Background(), FixedEgressConfig{Enabled: true}); e != nil {
		t.Fatal(e)
	}
	if e = db.SaveFixedEgressPool(context.Background(), FixedEgressPool{ID: "p", Enabled: true, Strategy: "stable"}); e != nil {
		t.Fatal(e)
	}
	return db
}
func TestFixedEgressSameExitExclusive(t *testing.T) {
	db := fixedTestDB(t)
	ctx := context.Background()
	for _, r := range []FixedEgressResource{{ID: "a", PoolID: "p", ProxyURL: "http://a:1", ObservedExitIP: "1.1.1.1"}, {ID: "b", PoolID: "p", ProxyURL: "http://b:1", ObservedExitIP: "1.1.1.1"}} {
		if e := db.SaveFixedEgressResource(ctx, r); e != nil {
			t.Fatal(e)
		}
	}
	if e := db.EnrollFixedEgress(ctx, 1, "p"); e != nil {
		t.Fatal(e)
	}
	if e := db.EnrollFixedEgress(ctx, 2, "p"); e != nil {
		t.Fatal(e)
	}
	if _, e := db.EnsureFixedEgressBinding(ctx, 1); e != nil {
		t.Fatal(e)
	}
	if _, e := db.EnsureFixedEgressBinding(ctx, 2); e == nil {
		t.Fatal("same exit bound twice")
	}
}
func TestFixedEgressConcurrentClaims(t *testing.T) {
	db := fixedTestDB(t)
	ctx := context.Background()
	for _, r := range []FixedEgressResource{{ID: "a", PoolID: "p", ProxyURL: "http://a:1", ObservedExitIP: "1.1.1.1"}, {ID: "b", PoolID: "p", ProxyURL: "http://b:1", ObservedExitIP: "2.2.2.2"}} {
		if e := db.SaveFixedEgressResource(ctx, r); e != nil {
			t.Fatal(e)
		}
	}
	for i := int64(1); i <= 2; i++ {
		if e := db.EnrollFixedEgress(ctx, i, "p"); e != nil {
			t.Fatal(e)
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := int64(1); i <= 2; i++ {
		wg.Add(1)
		go func(a int64) { defer wg.Done(); _, e := db.EnsureFixedEgressBinding(ctx, a); errs <- e }(i)
	}
	wg.Wait()
	close(errs)
	n := 0
	for e := range errs {
		if e == nil {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("claims=%d", n)
	}
}

func TestFixedEgressStableStrictAndStaleFailure(t *testing.T) {
	db := fixedTestDB(t)
	defer db.Close()
	ctx := context.Background()
	for _, r := range []FixedEgressResource{{ID: "a", PoolID: "p", ProxyURL: "http://a:1", ObservedExitIP: "1.1.1.1"}, {ID: "b", PoolID: "p", ProxyURL: "http://b:1", ObservedExitIP: "2.2.2.2"}} {
		if e := db.SaveFixedEgressResource(ctx, r); e != nil {
			t.Fatal(e)
		}
	}
	db.EnrollFixedEgress(ctx, 1, "p")
	first, e := db.EnsureFixedEgressBinding(ctx, 1)
	if e != nil {
		t.Fatal(e)
	}
	if e = db.MarkFixedEgressFailure(ctx, 1, first.Generation, first.ResourceID, "connect_failed"); e != nil {
		t.Fatal(e)
	}
	second, e := db.EnsureFixedEgressBinding(ctx, 1)
	if e != nil || second.ResourceID == first.ResourceID || second.Generation <= first.Generation {
		t.Fatalf("no stable switch %#v %v", second, e)
	}
	if e = db.MarkFixedEgressFailure(ctx, 1, first.Generation, first.ResourceID, "connect_failed"); e == nil {
		t.Fatal("stale failure accepted")
	}
	pool, _ := db.GetFixedEgressPool(ctx, "p")
	pool.Strategy = "strict"
	db.SaveFixedEgressPool(ctx, pool)
	db.MarkFixedEgressFailure(ctx, 1, second.Generation, second.ResourceID, "connect_failed")
	if _, e = db.EnsureFixedEgressBinding(ctx, 1); e == nil {
		t.Fatal("strict failed route allowed")
	}
	rs, _ := db.ListFixedEgress(ctx)
	for _, r := range rs {
		if r.ID == second.ResourceID && (r.CooldownUntil == nil || !r.CooldownUntil.After(time.Now())) {
			t.Fatal("cooldown not in future")
		}
	}
}
func TestFixedEgressSecretCorruptionAndConfigCAS(t *testing.T) {
	db := fixedTestDB(t)
	defer db.Close()
	ctx := context.Background()
	c, _ := db.GetFixedEgressConfig(ctx)
	if e := db.SetFixedEgressConfigRevision(ctx, true, c.Revision); e != nil {
		t.Fatal(e)
	}
	if e := db.SetFixedEgressConfigRevision(ctx, false, c.Revision); e == nil {
		t.Fatal("stale config accepted")
	}
	p := FixedEgressProvider{ID: "secret", APIKey: "sensitive-api-secret", BaseURL: "https://ipapi.cliproxy.com", Protocol: "http", AuthFormat: "3", Enabled: true}
	if e := db.SaveFixedEgressProvider(ctx, p); e != nil {
		t.Fatal(e)
	}
	var raw string
	db.conn.QueryRow(`SELECT api_key FROM fixed_egress_providers WHERE id='secret'`).Scan(&raw)
	if strings.Contains(raw, p.APIKey) {
		t.Fatal("plaintext key")
	}
	t.Setenv("CODEX_FIXED_EGRESS_KEY", "wrong-key")
	if _, e := db.GetFixedEgressProvider(ctx, p.ID); e == nil {
		t.Fatal("wrong key did not fail closed")
	}
}
func TestFixedEgressLeaseBlocksRetirement(t *testing.T) {
	db := fixedTestDB(t)
	defer db.Close()
	ctx := context.Background()
	db.SaveFixedEgressResource(ctx, FixedEgressResource{ID: "lease", PoolID: "p", ProxyURL: "http://lease:1", ObservedExitIP: "9.9.9.9"})
	if e := db.ClaimFixedEgressLease(ctx, "stream", "lease"); e != nil {
		t.Fatal(e)
	}
	if e := db.RetireFixedEgressResource(ctx, "lease", false); e == nil {
		t.Fatal("retired live route")
	}
	db.ReleaseFixedEgressLease(ctx, "stream")
	if e := db.RetireFixedEgressResource(ctx, "lease", false); e != nil {
		t.Fatal(e)
	}
	if e := db.ClaimFixedEgressLease(ctx, "late", "lease"); e == nil {
		t.Fatal("used retired route")
	}
}
func TestFixedEgressConcurrentProviderReservation(t *testing.T) {
	db := fixedTestDB(t)
	defer db.Close()
	ctx := context.Background()
	p := FixedEgressProvider{ID: "provider", APIKey: "key", BaseURL: "https://ipapi.cliproxy.com", Protocol: "http", AuthFormat: "3", PortStart: 10443, PortEnd: 10444, Capacity: 5, RefillIntervalSeconds: 30, Enabled: true}
	db.SaveFixedEgressProvider(ctx, p)
	var wg sync.WaitGroup
	ok := make(chan bool, 2)
	for _, id := range []string{"a", "b"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			ok <- db.ReserveFixedEgressTask(ctx, FixedEgressTask{ID: id, ProviderID: p.ID, Port: 10443}) == nil
		}(id)
	}
	wg.Wait()
	close(ok)
	n := 0
	for yes := range ok {
		if yes {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("duplicate start reservation %d", n)
	}
}
func TestFixedEgressExpiredLeaseStillRequiresDrainProof(t *testing.T) {
	db := fixedTestDB(t)
	defer db.Close()
	ctx := context.Background()
	db.SaveFixedEgressResource(ctx, FixedEgressResource{ID: "lease", PoolID: "p", ProxyURL: "http://lease:1", ObservedExitIP: "9.9.9.9"})
	db.ClaimFixedEgressLease(ctx, "paused-worker", "lease")
	db.conn.Exec(`UPDATE fixed_egress_leases SET expires_at=$1`, time.Now().Add(-time.Minute))
	if e := db.RetireFixedEgressResource(ctx, "lease", false); e == nil {
		t.Fatal("timed-out heartbeat treated as proof stream drained")
	}
}
func TestFixedEgressPersistenceRejectsProxyPath(t *testing.T) {
	db := fixedTestDB(t)
	defer db.Close()
	for _, proxy := range []string{"http://host:443/path", "http://host:443?key=x", "http://host:443#x"} {
		if e := db.SaveFixedEgressResource(context.Background(), FixedEgressResource{ID: "r", PoolID: "p", ProxyURL: proxy}); e == nil {
			t.Fatal("malformed persistence proxy accepted")
		}
	}
}
