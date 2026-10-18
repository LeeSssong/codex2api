package database

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func exerciseCodexProbePersistence(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	id, err := db.InsertAccount(ctx, "probe-evidence", "synthetic-refresh", "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	r := CodexProbeResult{AccountID: id, Model: "gpt-6-astra", Upstream: CodexPathBasispoints, Level: "basic", Outcome: "supported", BasicOutcome: "supported", Capability: CapabilitySupported, StartedAt: now, FinishedAt: now}
	f := CodexCapability{Upstream: r.Upstream, Model: r.Model, Capability: CapabilitySupported, Source: "bps_strong_probe", ObservedAt: now.UnixNano()}
	if applied, err := db.SaveCodexProbeResult(ctx, 1, r, &f); err != nil || !applied {
		t.Fatalf("save applied=%v err=%v", applied, err)
	}
	r.Outcome, r.StartedAt = "rate_limited", now.Add(time.Second)
	if applied, err := db.SaveCodexProbeResult(ctx, 1, r, nil); err != nil || !applied {
		t.Fatalf("transient save applied=%v err=%v", applied, err)
	}
	_, facts, err := db.GetCodexRoutes(ctx, id)
	if err != nil || len(facts) != 1 || facts[0].Capability != CapabilitySupported || facts[0].CredentialGeneration != 1 {
		t.Fatalf("transient replaced capability: %+v %v", facts, err)
	}
	r.Outcome, r.StartedAt = "unsupported", now.Add(-time.Second)
	f.Capability, f.ObservedAt = CapabilityUnsupported, r.StartedAt.UnixNano()
	if applied, err := db.SaveCodexProbeResult(ctx, 1, r, &f); err != nil || applied {
		t.Fatalf("stale saved applied=%v err=%v", applied, err)
	}
	latest, err := db.GetCodexCapabilityProbeResults(ctx, id)
	if err != nil || len(latest) != 1 || latest[0].Outcome != "rate_limited" {
		t.Fatalf("latest %+v %v", latest, err)
	}
	if _, err := db.conn.ExecContext(ctx, `UPDATE accounts SET credential_generation=2 WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	r.Outcome, r.StartedAt = "supported", now.Add(2*time.Second)
	f.Capability, f.ObservedAt = CapabilitySupported, r.StartedAt.UnixNano()
	if applied, err := db.SaveCodexProbeResult(ctx, 1, r, &f); err != nil || applied {
		t.Fatalf("old generation saved applied=%v err=%v", applied, err)
	}
	oldProduction := f
	oldProduction.CredentialGeneration = 1
	if applied, err := db.ObserveCodexCapability(ctx, id, oldProduction); err != nil || applied {
		t.Fatalf("old production generation saved applied=%v err=%v", applied, err)
	}
	latest, err = db.GetCodexCapabilityProbeResults(ctx, id)
	if err != nil || len(latest) != 0 {
		t.Fatalf("old generation exposed %+v %v", latest, err)
	}
	_, facts, err = db.GetCodexRoutes(ctx, id)
	if err != nil || len(facts) != 0 {
		t.Fatalf("old capability exposed %+v %v", facts, err)
	}
	if applied, err := db.SaveCodexProbeResult(ctx, 2, r, &f); err != nil || !applied {
		t.Fatalf("new generation save applied=%v err=%v", applied, err)
	}
	if err := db.ResetCodexCapabilities(ctx, id, CodexPathBasispoints, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	r.StartedAt = now.Add(3 * time.Second)
	f.ObservedAt = r.StartedAt.UnixNano()
	if applied, err := db.SaveCodexProbeResult(ctx, 2, r, &f); err != nil || applied {
		t.Fatalf("pre-reset probe saved applied=%v err=%v", applied, err)
	}
	latest, err = db.GetCodexCapabilityProbeResults(ctx, id)
	if err != nil || len(latest) != 1 || !latest[0].StartedAt.Equal(now.Add(2*time.Second)) {
		t.Fatalf("reset replaced previous diagnostic result: %+v %v", latest, err)
	}
	_, facts, err = db.GetCodexRoutes(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, fact := range facts {
		if fact.Capability != CapabilityUnknown {
			t.Fatalf("reset fence crossed: %+v", fact)
		}
	}
}

func TestSQLiteCodexProbePersistenceAndGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "probes.db")
	db, err := New("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	exerciseCodexProbePersistence(t, db)
	_ = db.Close()
	db, err = New("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.conn.QueryRow(`SELECT COUNT(*) FROM account_codex_probes`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("restart count=%d err=%v", count, err)
	}
}

func TestPostgresCodexProbePersistenceAndGeneration(t *testing.T) {
	dsn := os.Getenv("CODEX2API_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("CODEX2API_TEST_POSTGRES_DSN is not set")
	}
	db, err := New("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	exerciseCodexProbePersistence(t, db)
}
