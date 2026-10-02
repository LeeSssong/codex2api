package database

import (
	"context"
	"github.com/codex2api/accountops"
	"testing"
	"time"
)

func TestQualityBPSMoveRejectsNonCodexTargetAtSaveAndExecution(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			for _, phase := range []string{"save", "execution"} {
				t.Run(phase, func(t *testing.T) {
					db := guardTestDB(t, driver)
					ctx := context.Background()
					p, original := newQualitySafetyPlan(t, db, "disable_scheduling")
					if err := db.DeferAccountQualityPlan(ctx, p); err != nil {
						t.Fatal(err)
					}
					var target int64
					if err := db.conn.QueryRowContext(ctx, `INSERT INTO account_groups(name,channel) VALUES('bps-target','codex') RETURNING id`).Scan(&target); err != nil {
						t.Fatal(err)
					}
					p.Action = "enable_bps"
					p.BPS = &accountops.QualityBPSPolicy{FailureThreshold: 1, AllModels: true, AutoDisableOn403: true, AutoMoveOn403: true, TargetGroupID: target}
					changeChannel := func() {
						t.Helper()
						if _, err := db.conn.ExecContext(ctx, `UPDATE account_groups SET channel='grok' WHERE id=$1`, target); err != nil {
							t.Fatal(err)
						}
					}
					if phase == "save" {
						changeChannel()
						if _, err := db.SaveAccountQualityPlan(ctx, p); err == nil {
							t.Fatal("accepted Grok target for OpenAI BPS")
						}
						return
					}
					var err error
					p, err = db.SaveAccountQualityPlan(ctx, p)
					if err != nil {
						t.Fatal(err)
					}
					if err = db.TriggerAccountQualityPlan(ctx, p.ID); err != nil {
						t.Fatal(err)
					}
					claim, err := db.ClaimAccountQualityPlan(ctx, time.Now())
					if err != nil || claim == nil {
						t.Fatal(err)
					}
					if action, err := db.ApplyAccountQualityOutcome(ctx, *claim, "failed"); err != nil || action != "bps_enabled" {
						t.Fatal(action, err)
					}
					changeChannel()
					row, err := db.GetAccountByID(ctx, p.AccountID)
					if err != nil {
						t.Fatal(err)
					}
					if changed, err := db.DisableQualityBPSOn403(ctx, p.AccountID, row.CredentialGeneration); changed {
						t.Fatal("moved OpenAI account to Grok", err)
					}
					var group int64
					if err := db.conn.QueryRowContext(ctx, `SELECT group_id FROM account_group_members WHERE account_id=$1`, p.AccountID).Scan(&group); err != nil || group != original {
						t.Fatal("original membership changed", group, err)
					}
				})
			}
		})
	}
}
