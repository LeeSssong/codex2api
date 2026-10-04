package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/codex2api/smartops"
	"testing"
	"time"
)

func TestQuality5xxDatabaseLifecycle(t *testing.T) {
	db := smartOpsDB(t)
	ctx := context.Background()
	group, err := db.CreateAccountGroup(ctx, "recovery", "", "#334455", 0, 0, sql.NullInt64{})
	if err != nil {
		t.Fatal(err)
	}
	c := smartops.DefaultOAuthAutoConfig()
	c.Enabled = true
	c.GroupIDs = []int64{group}
	c.UpgradeGroupIDs = []int64{group}
	c.UpgradeEnabled = true
	c.Concurrency = 9
	c.Revision = "test"
	c.SuccessesPerStep = 1
	c.UpgradeStep = 3
	c.CooldownSeconds = 1
	c.MaxConcurrency = 30
	c.Quality5xx = smartops.Quality5xxRampConfig{Enabled: true, Floor: 5, CooldownSeconds: 60, Models: []string{"target"}}
	id, err := db.InsertAutoConfiguredOAuthAccount(ctx, "test", "openai", "oauth", map[string]interface{}{"access_token": "synthetic-do-not-copy"}, "", c)
	if err != nil {
		t.Fatal(err)
	}
	row, err := db.GetAccountByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	gen := row.CredentialGeneration
	n, changed, err := db.RecordQuality5xxFailure(ctx, id, c, gen, c.Revision, c.Quality5xx.Models)
	if err != nil || n != 5 || !changed {
		t.Fatalf("failure: %d %v %v", n, changed, err)
	}
	attempt := db.Quality5xxAttempt(ctx, id)
	if _, changed, err := db.RecordSmartOpsConcurrency(ctx, id, c, true); err != nil || changed {
		t.Fatalf("premature upgrade: %v %v", changed, err)
	}
	if _, _, err := db.RecordQuality5xxFailure(ctx, id, c, gen, c.Revision, c.Quality5xx.Models); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.ApplyQuality5xxProbe(ctx, id, c, gen, c.Revision, true, true, attempt); err != nil {
		t.Fatal(err)
	}
	cooldowns, err := db.ListActiveModelCooldownsForAccount(ctx, id)
	if err != nil || len(cooldowns) != 1 {
		t.Fatalf("stale pass removed cooldown: %v %v", cooldowns, err)
	}
	attempt = db.Quality5xxAttempt(ctx, id)
	if n, _, err := db.ApplyQuality5xxProbe(ctx, id, c, gen, c.Revision, true, true, attempt); err != nil || n != 5 {
		t.Fatalf("probe must not upgrade: %d %v", n, err)
	}
	cooldowns, err = db.ListActiveModelCooldownsForAccount(ctx, id)
	if err != nil || len(cooldowns) != 0 {
		t.Fatalf("pass did not release models: %v %v", cooldowns, err)
	}
	for _, want := range []int64{8, 9, 9} {
		var stored string
		if err := db.conn.QueryRowContext(ctx, `SELECT payload FROM smart_ops_concurrency WHERE account_id=$1`, id).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		var env map[string]json.RawMessage
		_ = json.Unmarshal([]byte(stored), &env)
		var q smartops.Quality5xxRampState
		_ = json.Unmarshal(env["quality_5xx"], &q)
		q.Concurrency.PausedUntil = time.Time{}
		env["quality_5xx"], _ = json.Marshal(q)
		var native smartops.ConcurrencyState
		_ = json.Unmarshal(env["concurrency"], &native)
		native.PausedUntil = time.Time{}
		env["concurrency"], _ = json.Marshal(native)
		b, _ := json.Marshal(env)
		if _, err := db.conn.ExecContext(ctx, `UPDATE smart_ops_concurrency SET payload=$1 WHERE account_id=$2`, string(b), id); err != nil {
			t.Fatal(err)
		}
		got, _, err := db.RecordSmartOpsConcurrency(ctx, id, c, true)
		if err != nil || got != want {
			t.Fatalf("ramp got %d want %d: %v", got, want, err)
		}
	}
	var raw string
	if err = db.conn.QueryRowContext(ctx, `SELECT payload FROM smart_ops_concurrency WHERE account_id=$1`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err = json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["access_token"]; ok {
		t.Fatal("credentials copied into state")
	}
	// Credential replacement must fence recovery before touching the cooldown.
	if _, _, err = db.RecordQuality5xxFailure(ctx, id, c, gen, c.Revision, c.Quality5xx.Models); err != nil {
		t.Fatal(err)
	}
	attempt = db.Quality5xxAttempt(ctx, id)
	if _, _, err = db.ApplyQuality5xxProbe(ctx, id, c, gen+1, c.Revision, true, true, attempt); err != nil {
		t.Fatal(err)
	}
	cooldowns, err = db.ListActiveModelCooldownsForAccount(ctx, id)
	if err != nil || len(cooldowns) != 1 {
		t.Fatalf("generation fence: %v %v", cooldowns, err)
	}
}
