package database

import (
	"context"
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
