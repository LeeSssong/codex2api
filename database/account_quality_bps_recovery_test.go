package database

import (
	"context"
	"testing"
	"time"

	"github.com/codex2api/accountops"
	"github.com/codex2api/plugins"
)

func TestQualityBPS403RecoveryOwnsOnlyUnchangedAccountAndPath(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			for _, mode := range []string{"recover", "manual_bps", "credential", "group", "disabled_plugin", "plugin_aba"} {
				t.Run(mode, func(t *testing.T) {
					db := guardTestDB(t, driver)
					ctx := context.Background()
					p, group := newQualitySafetyPlan(t, db, "disable_scheduling")
					if err := db.DeferAccountQualityPlan(ctx, p); err != nil {
						t.Fatal(err)
					}
					p.Action = "enable_bps"
					p.BPS = &accountops.QualityBPSPolicy{FailureThreshold: 1, AllModels: true, AutoDisableOn403: true, AutoRecoverOn403: true, AutoMoveOn403: true, TargetGroupID: group}
					p, err := db.SaveAccountQualityPlan(ctx, p)
					if err != nil {
						t.Fatal(err)
					}
					if err := db.TriggerAccountQualityPlan(ctx, p.ID); err != nil {
						t.Fatal(err)
					}
					claimed, err := db.ClaimAccountQualityPlan(ctx, time.Now())
					if err != nil || claimed == nil {
						t.Fatal("claim", err)
					}
					p = *claimed
					if action, err := db.ApplyAccountQualityOutcome(ctx, p, "failed"); err != nil || action != "bps_enabled" {
						t.Fatal(action, err)
					}
					row, err := db.GetAccountByID(ctx, p.AccountID)
					if err != nil {
						t.Fatal(err)
					}
					if changed, err := db.DisableQualityBPSOn403(ctx, p.AccountID, row.CredentialGeneration); err != nil || !changed {
						t.Fatal("disable", changed, err)
					}
					if changed, err := db.DisableQualityBPSOn403(ctx, p.AccountID, row.CredentialGeneration); err != nil || changed {
						t.Fatal("duplicate disable", changed, err)
					}
					candidate, err := db.ClaimQualityBPSRecovery(ctx, time.Now().Add(2*time.Hour))
					if err != nil || candidate == nil {
						t.Fatal("recovery claim", err)
					}
					switch mode {
					case "manual_bps":
						if err := db.SetCodexPathAllowed(ctx, p.AccountID, "basispoints", false); err != nil {
							t.Fatal(err)
						}
					case "credential":
						if _, err := db.conn.ExecContext(ctx, `UPDATE accounts SET credential_generation=credential_generation+1 WHERE id=$1`, p.AccountID); err != nil {
							t.Fatal(err)
						}
					case "group":
						if err := db.SetAccountGroups(ctx, p.AccountID, []int64{group}); err != nil {
							t.Fatal(err)
						}
					case "disabled_plugin", "plugin_aba":
						if err := NewPluginStore(db).Put(ctx, plugins.Setting{ID: "quality-ops", Enabled: false}); err != nil {
							t.Fatal(err)
						}
						if mode == "plugin_aba" {
							if err := NewPluginStore(db).Put(ctx, plugins.Setting{ID: "quality-ops", Enabled: true}); err != nil {
								t.Fatal(err)
							}
						}
					}
					changed, err := db.CompleteQualityBPSRecovery(ctx, *candidate)
					if err != nil {
						t.Fatal(err)
					}
					if changed != (mode == "recover") {
						t.Fatalf("%s: recovery=%v", mode, changed)
					}
					paths, _, err := db.GetCodexRoutes(ctx, p.AccountID)
					if err != nil {
						t.Fatal(err)
					}
					if paths[0].Allowed != (mode == "recover") {
						t.Fatal("manual/credential change overwritten")
					}
					if mode == "recover" {
						if action, err := db.ApplyAccountQualityOutcome(ctx, p, "passed"); err != nil || action != "restored" {
							t.Fatal("quality recovery after BPS recovery", action, err)
						}
					}
					if mode == "plugin_aba" {
						fresh, err := db.ClaimQualityBPSRecovery(ctx, time.Now().Add(4*time.Hour))
						if err != nil || fresh == nil {
							t.Fatal("fresh recovery", err)
						}
						if applied, err := db.CompleteQualityBPSRecovery(ctx, *fresh); err != nil || !applied {
							t.Fatal("fresh recovery should still work", applied, err)
						}
					}
				})
			}
		})
	}
}
