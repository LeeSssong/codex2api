package database

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestBasispointsSettingsPersist(t *testing.T) {
	db, e := New("sqlite", filepath.Join(t.TempDir(), "bps.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	t.Setenv("BASISPOINTS_MODELS", "env-model")
	s, e := db.GetBasispointsSettings(ctx)
	if e != nil || !s.AllowsModel("env-model") {
		t.Fatal(s, e)
	}
	s.Enabled = true
	s.ModelScope = "selected"
	s.Models = []string{}
	s.ImageRelayEnabled = true
	s.ImageRelayPublicOrigin = "https://images.example.com"
	if e = db.SaveBasispointsSettings(ctx, s); e != nil {
		t.Fatal(e)
	}
	s, e = db.GetBasispointsSettings(ctx)
	if e != nil || s.AllowsModel("env-model") || !s.Enabled {
		t.Fatal(s, e)
	}
	epoch := s.ImageRelayEpoch
	s.ImageRelayEnabled = false
	if e = db.SaveBasispointsSettings(ctx, s); e != nil {
		t.Fatal(e)
	}
	s, _ = db.GetBasispointsSettings(ctx)
	if s.ImageRelayEpoch <= epoch {
		t.Fatal("epoch unchanged")
	}
	s.ImageRelayPublicOrigin = "http://example.com"
	if db.SaveBasispointsSettings(ctx, s) == nil {
		t.Fatal("unsafe origin")
	}
}
func TestBasispointsPolicyFences(t *testing.T) {
	db, e := New("sqlite", filepath.Join(t.TempDir(), "bps.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	id, e := db.InsertAccount(ctx, "bps", "synthetic-refresh", "")
	if e != nil {
		t.Fatal(e)
	}
	p := BasispointsAccountPolicy{ModelScope: "selected", Models: []string{"gpt-test"}, AutoDisableOn403: true}
	if e = db.SetBasispointsAccountPolicy(ctx, id, p); e != nil {
		t.Fatal(e)
	}
	p, _ = db.GetBasispointsAccountPolicy(ctx, id)
	row, _ := db.GetAccountByID(ctx, id)
	if e = db.SetCodexPathAllowed(ctx, id, "basispoints", true); e != nil {
		t.Fatal(e)
	}
	if ok, e := db.DisableBasispointsForHTTP403(ctx, id, row.CredentialGeneration, p.Revision); ok || e != nil {
		t.Fatal("manual edit crossed", ok, e)
	}
	p, _ = db.GetBasispointsAccountPolicy(ctx, id)
	if ok, e := db.DisableBasispointsForHTTP403(ctx, id, row.CredentialGeneration+1, p.Revision); ok || e != nil {
		t.Fatal("generation crossed", ok, e)
	}
	if ok, e := db.DisableBasispointsForHTTP403(ctx, id, row.CredentialGeneration, p.Revision); !ok || e != nil {
		t.Fatal("disable", ok, e)
	}
	if ok, e := db.DisableBasispointsForHTTP403(ctx, id, row.CredentialGeneration, p.Revision); ok || e != nil {
		t.Fatal("duplicate", ok, e)
	}
	p, _ = db.GetBasispointsAccountPolicy(ctx, id)
	if p.DisabledBy != "auto_403" {
		t.Fatal(p)
	}
	if e = db.SetCodexPathAllowed(ctx, id, "basispoints", true); e != nil {
		t.Fatal(e)
	}
	p, _ = db.GetBasispointsAccountPolicy(ctx, id)
	if p.DisabledBy != "" {
		t.Fatal(p)
	}
}

func TestBasispointsLegacyEnableRevokesEpoch(t *testing.T) {
	db, e := New("sqlite", filepath.Join(t.TempDir(), "bps.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	s := DefaultBasispointsSettings()
	s.Enabled = true
	if e = db.SaveBasispointsSettings(ctx, s); e != nil {
		t.Fatal(e)
	}
	s, _ = db.GetBasispointsSettings(ctx)
	oldEpoch := s.ImageRelayEpoch
	legacy, e := db.GetSystemSettings(ctx)
	if e != nil {
		t.Fatal(e)
	}
	legacy.CodexBasispointsEnabled = false
	if e = db.UpdateSystemSettings(ctx, legacy); e != nil {
		t.Fatal(e)
	}
	s, e = db.GetBasispointsSettings(ctx)
	if e != nil || s.Enabled || s.ImageRelayEpoch <= oldEpoch {
		t.Fatal("legacy disable retained epoch", s, e)
	}
}

func exerciseBasispointsPolicyCAS(t *testing.T, db *DB) {
	ctx := context.Background()
	id, e := db.InsertAccount(ctx, "bps-concurrent", "synthetic-rt", "")
	if e != nil {
		t.Fatal(e)
	}
	p := BasispointsAccountPolicy{ModelScope: "all", AutoDisableOn403: true}
	if e = db.SetBasispointsAccountPolicy(ctx, id, p); e != nil {
		t.Fatal(e)
	}
	row, _ := db.GetAccountByID(ctx, id)
	p, _ = db.GetBasispointsAccountPolicy(ctx, id)
	var changed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, e := db.DisableBasispointsForHTTP403(ctx, id, row.CredentialGeneration, p.Revision)
			if e != nil {
				t.Error(e)
			}
			if ok {
				changed.Add(1)
			}
		}()
	}
	wg.Wait()
	if changed.Load() != 1 {
		t.Fatal("CAS winners", changed.Load())
	}
	var n int
	if e = db.conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM account_events WHERE account_id=$1 AND source='basispoints_auto_403'", id).Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	for _, kind := range []string{"same_policy", "revoke", "generation", "deleted", "agent", "relay"} {
		t.Run(kind, func(t *testing.T) {
			id, e := db.InsertAccount(ctx, "bps-fence-"+kind, "synthetic-rt-"+kind, "")
			if e != nil {
				t.Fatal(e)
			}
			policy := BasispointsAccountPolicy{ModelScope: "all", AutoDisableOn403: true}
			if e = db.SetBasispointsAccountPolicy(ctx, id, policy); e != nil {
				t.Fatal(e)
			}
			policy, _ = db.GetBasispointsAccountPolicy(ctx, id)
			row, _ := db.GetAccountByID(ctx, id)
			switch kind {
			case "same_policy":
				e = db.SetBasispointsAccountPolicy(ctx, id, policy)
			case "revoke":
				policy.AutoDisableOn403 = false
				e = db.SetBasispointsAccountPolicy(ctx, id, policy)
			case "generation":
				_, e = db.conn.ExecContext(ctx, "UPDATE accounts SET credential_generation=credential_generation+1 WHERE id=$1", id)
			case "deleted":
				_, e = db.conn.ExecContext(ctx, "UPDATE accounts SET deleted_at=CURRENT_TIMESTAMP,status='deleted' WHERE id=$1", id)
			case "agent":
				_, e = db.conn.ExecContext(ctx, "UPDATE accounts SET credentials=$1 WHERE id=$2", "{\"auth_mode\":\"agentIdentity\",\"refresh_token\":\"synthetic\"}", id)
			case "relay":
				_, e = db.conn.ExecContext(ctx, "UPDATE accounts SET credentials=$1 WHERE id=$2", "{\"upstream_type\":\"openai\",\"access_token\":\"synthetic\"}", id)
			}
			if e != nil {
				t.Fatal(e)
			}
			ok, e := db.DisableBasispointsForHTTP403(ctx, id, row.CredentialGeneration, policy.Revision)
			if ok {
				t.Fatal("stale/invalid request changed permission")
			}
			if kind != "agent" && kind != "relay" && e != nil {
				t.Fatal(e)
			}
			paths, _, e := db.GetCodexRoutes(ctx, id)
			if e != nil || len(paths) != 1 || !paths[0].Allowed {
				t.Fatal(paths, e)
			}
		})
	}
}
func TestBasispointsPolicyConcurrentCAS(t *testing.T) {
	db, e := New("sqlite", filepath.Join(t.TempDir(), "cas.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	exerciseBasispointsPolicyCAS(t, db)
}
func TestBasispointsPolicyPostgres(t *testing.T) {
	dsn := os.Getenv("CODEX2API_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("CODEX2API_TEST_POSTGRES_DSN is not set")
	}
	db, e := New("postgres", dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	exerciseBasispointsPolicyCAS(t, db)
}
func TestBasispointsModelPolicyIntersection(t *testing.T) {
	global := BasispointsSettings{ModelScope: "selected", Models: []string{"gpt-test"}}
	for _, tc := range []struct {
		scope, model string
		models       []string
		want         bool
	}{
		{"inherit", "GPT-test-2026-09-26", nil, true}, {"all", "outside", nil, false}, {"selected", "gpt-test", nil, false}, {"selected", "gpt-test", []string{"gpt-test"}, true}, {"selected", "gpt-test-2026-02-30", []string{"gpt-test"}, false}, {"all", "gpt-test-alias", nil, false},
	} {
		p := BasispointsAccountPolicy{ModelScope: tc.scope, Models: tc.models}
		if got := p.AllowsModel(tc.model, global); got != tc.want {
			t.Fatal(tc, got)
		}
	}
}
func TestBasispointsCombinedManualEditAdvancesOnce(t *testing.T) {
	db, e := New("sqlite", filepath.Join(t.TempDir(), "edit.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	id, e := db.InsertAccount(ctx, "combined", "synthetic-rt", "")
	if e != nil {
		t.Fatal(e)
	}
	p := BasispointsAccountPolicy{ModelScope: "selected", Models: []string{}, AutoDisableOn403: true}
	allowed := false
	if e = db.UpdateBasispointsAccountRoute(ctx, id, &allowed, &p); e != nil {
		t.Fatal(e)
	}
	p, e = db.GetBasispointsAccountPolicy(ctx, id)
	if e != nil || p.Revision != 1 {
		t.Fatal(p, e)
	}
	paths, _, _ := db.GetCodexRoutes(ctx, id)
	if len(paths) != 1 || paths[0].Allowed {
		t.Fatal(paths)
	}
}
func TestBasispointsLegacyImageEnvironmentDefaults(t *testing.T) {
	t.Setenv("BASISPOINTS_IMAGE_PUBLIC_ORIGIN", "")
	t.Setenv("BASISPOINTS_IMAGE_RELAY_ENABLED", "")
	t.Setenv("IMAGE_ASSET_PUBLIC_BASE_URL", "https://images.example.com/")
	s := DefaultBasispointsSettings()
	if !s.ImageRelayEnabled || s.ImageRelayPublicOrigin != "https://images.example.com" {
		t.Fatal(s)
	}
	t.Setenv("BASISPOINTS_IMAGE_RELAY_ENABLED", "false")
	if DefaultBasispointsSettings().ImageRelayEnabled {
		t.Fatal("explicit disable ignored")
	}
	t.Setenv("BASISPOINTS_IMAGE_RELAY_ENABLED", "")
	t.Setenv("IMAGE_ASSET_PUBLIC_BASE_URL", "http://unsafe.example.com")
	if DefaultBasispointsSettings().ImageRelayEnabled {
		t.Fatal("unsafe legacy origin enabled")
	}
}
func TestBasispointsPolicyRejectsOversizedModelConfiguration(t *testing.T) {
	models := make([]string, 257)
	for i := range models {
		models[i] = "model"
	}
	if (BasispointsSettings{ModelScope: "selected", Models: models}).Validate() == nil {
		t.Fatal("unbounded settings models")
	}
	if (BasispointsAccountPolicy{ModelScope: "selected", Models: models}).Validate() == nil {
		t.Fatal("unbounded account models")
	}
}
