package database

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/codex2api/plugins"
)

func TestPluginDisableFencesBackgroundPublicationsIndependently(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			db := guardTestDB(t, driver)
			ctx := context.Background()
			store := NewPluginStore(db)
			if err := store.Put(ctx, plugins.Setting{ID: "account-ops", Enabled: false}); err != nil {
				t.Fatal(err)
			}
			_, version, err := db.TokenGuardConfig(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.CreateTokenGuardJob(ctx, "cycle", 0, version); err != nil {
				t.Fatalf("alerts disable blocked token guard: %v", err)
			}
			job, err := db.ClaimTokenGuardJob(ctx, "publication-test", time.Now())
			if err != nil || job == nil {
				t.Fatalf("claim: %v", err)
			}
			if err := store.Put(ctx, plugins.Setting{ID: "token-guard", Enabled: false}); err != nil {
				t.Fatal(err)
			}
			if err := db.RenewTokenGuardJob(ctx, *job, time.Now()); !errors.Is(err, ErrTokenGuardDisabled) {
				t.Fatalf("disabled token result accepted: %v", err)
			}
			p, _ := newQualitySafetyPlan(t, db, "disable_scheduling")
			if action, err := db.ApplyAccountQualityOutcome(ctx, p, "failed"); err != nil || action != "scheduling_disabled" {
				t.Fatalf("alerts disable blocked quality: %s %v", action, err)
			}
			if err := store.Put(ctx, plugins.Setting{ID: "quality-ops", Enabled: false}); err != nil {
				t.Fatal(err)
			}
			if action, err := db.ApplyAccountQualityOutcome(ctx, p, "passed"); err != nil || action != "stale_run" {
				t.Fatalf("disabled quality published: %s %v", action, err)
			}
		})
	}
}

func TestCredentialPluginDisableRevokesTaskAndMonitorLeases(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			db := guardTestDB(t, driver)
			ctx := context.Background()
			for _, query := range []string{
				`CREATE TABLE credential_ops_tasks(status TEXT,stage TEXT,lease_owner TEXT,lease_until TIMESTAMP,finished_at TIMESTAMP,updated_at TIMESTAMP)`,
				`CREATE TABLE credential_ops_monitors(lease_owner TEXT,lease_until TIMESTAMP,updated_at TIMESTAMP)`,
				`INSERT INTO credential_ops_tasks(status,stage,lease_owner,lease_until) VALUES('running','login','worker',CURRENT_TIMESTAMP)`,
				`INSERT INTO credential_ops_monitors(lease_owner,lease_until) VALUES('probe',CURRENT_TIMESTAMP)`,
			} {
				if _, err := db.conn.ExecContext(ctx, query); err != nil {
					t.Fatal(err)
				}
			}
			store := NewPluginStore(db)
			for _, enabled := range []bool{false, true} {
				if err := store.Put(ctx, plugins.Setting{ID: "credential-ops", Enabled: enabled}); err != nil {
					t.Fatal(err)
				}
			}
			var status, owner string
			var lease sql.NullTime
			if err := db.conn.QueryRowContext(ctx, `SELECT status,lease_owner,lease_until FROM credential_ops_tasks`).Scan(&status, &owner, &lease); err != nil {
				t.Fatal(err)
			}
			if status != "cancelled" || owner != "" || lease.Valid {
				t.Fatal("credential task survived plugin ABA")
			}
			if err := db.conn.QueryRowContext(ctx, `SELECT lease_owner,lease_until FROM credential_ops_monitors`).Scan(&owner, &lease); err != nil {
				t.Fatal(err)
			}
			if owner != "" || lease.Valid {
				t.Fatal("credential probe lease survived plugin ABA")
			}
		})
	}
}
