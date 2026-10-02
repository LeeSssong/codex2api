package database

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/codex2api/accountops"
)

func TestQualityBPSActionThresholdUsageRecoveryAndManualOwnership(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			for _, mode := range []string{"count", "usage", "manual_disabled", "manual_edit"} {
				t.Run(mode, func(t *testing.T) {
					db := guardTestDB(t, driver)
					ctx := context.Background()
					p, _ := newQualitySafetyPlan(t, db, "disable_scheduling")
					if err := db.DeferAccountQualityPlan(ctx, p); err != nil {
						t.Fatal(err)
					}
					p.Action = "enable_bps"
					p.BPS = &accountops.QualityBPSPolicy{FailureThreshold: 2, UsagePercent: 80, PassThreshold: 2, HoldOnUsage: true, Models: []string{"gpt-6-astra"}}
					p, err := db.SaveAccountQualityPlan(ctx, p)
					if err != nil {
						t.Fatal(err)
					}
					if err := db.TriggerAccountQualityPlan(ctx, p.ID); err != nil {
						t.Fatal(err)
					}
					claimed, err := db.ClaimAccountQualityPlan(ctx, time.Now())
					if err != nil || claimed == nil {
						t.Fatalf("claim %v", err)
					}
					p = *claimed
					apply := func(outcome string) string {
						t.Helper()
						a, e := db.ApplyAccountQualityOutcome(ctx, p, outcome)
						if e != nil {
							t.Fatal(e)
						}
						return a
					}
					if mode == "manual_disabled" {
						if err := db.SetCodexPathAllowed(ctx, p.AccountID, "basispoints", false); err != nil {
							t.Fatal(err)
						}
					}
					if mode == "usage" {
						p.HasUsage = true
						p.UsagePercent = 85
					} else {
						if got := apply("failed"); got != "failure_counted:1/2" {
							t.Fatal(got)
						}
						if got := apply("inconclusive"); got != "inconclusive" {
							t.Fatal(got)
						}
					}
					got := apply("failed")
					if mode == "manual_disabled" {
						if got != "bps_blocked_manual" {
							t.Fatal(got)
						}
						return
					}
					want := "bps_enabled"
					if mode == "usage" {
						want = "bps_enabled_usage"
					}
					if got != want {
						t.Fatalf("enable %s want %s", got, want)
					}
					row, err := db.GetAccountByID(ctx, p.AccountID)
					if err != nil || !row.Enabled {
						t.Fatal("BPS action disabled scheduling", err)
					}
					groups, err := db.GetAccountGroupIDs(ctx, p.AccountID)
					if err != nil || len(groups) != 1 {
						t.Fatal("BPS action changed groups", err)
					}
					paths, _, err := db.GetCodexRoutes(ctx, p.AccountID)
					if err != nil || len(paths) != 1 || paths[0].QualityBPS == nil || fmt.Sprint(paths[0].QualityBPS.Models) != "[gpt-6-astra]" {
						t.Fatalf("real path policy missing %+v %v", paths, err)
					}
					if got := apply("passed"); got != "restore_counted:1/2" {
						t.Fatal(got)
					}
					if mode == "usage" {
						if got := apply("passed"); got != "bps_kept_usage" {
							t.Fatal(got)
						}
						p.UsagePercent = 20
					}
					if mode == "manual_edit" {
						if err := db.SetCodexPathAllowed(ctx, p.AccountID, "basispoints", true); err != nil {
							t.Fatal(err)
						}
					}
					got = apply("passed")
					want = "restored"
					if mode == "manual_edit" {
						want = "restore_conflict"
					}
					if got != want {
						t.Fatalf("restore %s want %s", got, want)
					}
					paths, _, err = db.GetCodexRoutes(ctx, p.AccountID)
					if err != nil {
						t.Fatal(err)
					}
					if (paths[0].QualityBPS == nil) != (mode != "manual_edit") {
						raw, _ := json.Marshal(paths)
						t.Fatalf("ownership restored incorrectly %s", raw)
					}
				})
			}
		})
	}
}
