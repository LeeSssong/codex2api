package database

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func exerciseCodexRoutes(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	id, err := db.InsertAccount(ctx, "dual-upstream-test", "synthetic-refresh", "")
	if err != nil {
		t.Fatal(err)
	}
	paths, facts, err := db.GetCodexRoutes(ctx, id)
	if err != nil || len(paths) != 0 || len(facts) != 0 {
		t.Fatalf("legacy defaults: %v %v %v", paths, facts, err)
	}
	if err = db.SetCodexPathAllowed(ctx, id, "basispoints", false); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	obs := CodexCapability{Upstream: "basispoints", Model: "GPT-6-ASTRA", Capability: "supported", Source: "upstream_completed", ObservedAt: now.UnixNano()}
	if ok, err := db.ObserveCodexCapability(ctx, id, obs); !ok || err != nil {
		t.Fatal("observation", ok, err)
	}
	obs.Capability = "unsupported"
	obs.ObservedAt = now.Add(-time.Second).UnixNano()
	if ok, err := db.ObserveCodexCapability(ctx, id, obs); ok || err != nil {
		t.Fatal("stale observation accepted", ok, err)
	}
	paths, facts, err = db.GetCodexRoutes(ctx, id)
	if err != nil || len(paths) != 1 || paths[0].Allowed || len(facts) != 1 || facts[0].Model != "gpt-6-astra" || facts[0].Capability != "supported" {
		t.Fatalf("roundtrip %+v %+v %v", paths, facts, err)
	}
	if err = db.ResetCodexCapabilities(ctx, id, "basispoints", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	obs.Model = "new-model"
	obs.ObservedAt = now.UnixNano()
	if ok, err := db.ObserveCodexCapability(ctx, id, obs); ok || err != nil {
		t.Fatal("late unseen-model result crossed reset", ok, err)
	}
	obs.ObservedAt = now.Add(2 * time.Second).UnixNano()
	if ok, err := db.ObserveCodexCapability(ctx, id, obs); !ok || err != nil {
		t.Fatal("new observation rejected", ok, err)
	}
	records, err := db.ListCodexRouteRecords(ctx)
	if err != nil || len(records[id].Facts) != 3 || records[id].Paths[0].Allowed {
		t.Fatal("list lost reset/config", records, err)
	}
	if err = db.ensureCodexRoutesSchema(ctx); err != nil {
		t.Fatal("idempotent migration", err)
	}
}
func TestSQLiteCodexRoutesMigrationAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes.db")
	db, err := New("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a pre-feature database, retaining the legacy accounts schema.
	for _, table := range []string{"account_codex_capabilities", "account_codex_paths"} {
		if _, err = db.conn.Exec("DROP TABLE " + table); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	db, err = New("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	exerciseCodexRoutes(t, db)
	db.Close()
	db, err = New("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	records, err := db.ListCodexRouteRecords(context.Background())
	if err != nil || len(records) != 1 {
		t.Fatal("restart lost routes", records, err)
	}
}
func TestPostgresCodexRoutes(t *testing.T) {
	dsn := os.Getenv("CODEX2API_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("CODEX2API_TEST_POSTGRES_DSN is not set")
	}
	db, err := New("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	exerciseCodexRoutes(t, db)
}
func TestCodexRouteKeyCompatibility(t *testing.T) {
	var old APIKeyLimits
	if err := json.Unmarshal([]byte(`{}`), &old); err != nil || !old.IsZero() {
		t.Fatal("old Key changed", err)
	}
	for _, policy := range []string{"inherit", "codex_only", "basispoints_only", "codex_prefer", "basispoints_prefer", "basispoints_models_only"} {
		l := APIKeyLimits{CodexRoutePolicy: policy, CodexCapabilityFilter: "dual_supported"}
		if err := l.ValidateCodexRouting(); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(l)
		var restored APIKeyLimits
		json.Unmarshal(raw, &restored)
		if restored.CodexRoutePolicy != policy || restored.CodexCapabilityFilter != "dual_supported" {
			t.Fatal("key fields dropped")
		}
	}
	for _, l := range []APIKeyLimits{{CodexRoutePolicy: "bad"}, {CodexCapabilityFilter: "unknown-only"}, {UpstreamChannel: "grok", CodexRoutePolicy: "codex_only"}} {
		if l.ValidateCodexRouting() == nil {
			t.Fatal("invalid Key accepted")
		}
	}
}
