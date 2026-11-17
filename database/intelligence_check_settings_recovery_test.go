package database

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestIntelligenceCheckSettingsSavePreservesOwnedRecoveryInterval(t *testing.T) {
	for _, tc := range []struct {
		action          string
		recoverySeconds int
	}{
		{action: "pause", recoverySeconds: 3600},
		{action: "switch_group", recoverySeconds: 7200},
	} {
		t.Run(tc.action, func(t *testing.T) {
			db := intelligenceDB(t)
			ctx := context.Background()
			id := intelligenceAccount(t, db)
			settings, err := db.GetIntelligenceCheckSettings(ctx)
			if err != nil {
				t.Fatal(err)
			}
			settings.Enabled, settings.Action, settings.DegradeThreshold = true, tc.action, 1
			settings.IntervalSeconds, settings.RecoveryIntervalSeconds = 300, tc.recoverySeconds
			if tc.action == "switch_group" {
				settings.TargetGroupID, err = db.CreateAccountGroup(ctx, "recovery", "", "", 0, 0, sql.NullInt64{})
				if err != nil {
					t.Fatal(err)
				}
			}
			settings, err = db.SaveIntelligenceCheckSettings(ctx, settings)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
			intelligenceRun(t, db, id, now, "22")
			before, _, err := db.ListIntelligenceCheckAccounts(ctx, IntelligenceCheckAccountFilter{AccountID: id})
			if err != nil || len(before) != 1 {
				t.Fatalf("before=%+v err=%v", before, err)
			}
			if before[0].OwnedAction != tc.action || before[0].NextIntervalSeconds != tc.recoverySeconds || before[0].NextDueAt == nil {
				t.Fatalf("expected owned recovery before save: %+v", before[0])
			}
			settings.Concurrency++
			if _, err := db.SaveIntelligenceCheckSettings(ctx, settings); err != nil {
				t.Fatal(err)
			}
			after, _, err := db.ListIntelligenceCheckAccounts(ctx, IntelligenceCheckAccountFilter{AccountID: id})
			if err != nil || len(after) != 1 {
				t.Fatalf("after=%+v err=%v", after, err)
			}
			if after[0].NextIntervalSeconds != tc.recoverySeconds || after[0].NextDueAt == nil || !after[0].NextDueAt.Equal(*before[0].NextDueAt) {
				t.Fatalf("settings save shortened recovery: before=%+v after=%+v", before[0], after[0])
			}
			if after[0].OwnedAction != tc.action {
				t.Fatalf("save lost ownership: %+v", after[0])
			}
		})
	}
}

func TestIntelligenceCheckDisablingAdaptiveUsesRecoveryInterval(t *testing.T) {
	db := intelligenceDB(t)
	ctx := context.Background()
	id := intelligenceAccount(t, db)
	settings, err := db.GetIntelligenceCheckSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.Enabled, settings.AdaptiveEnabled, settings.Action, settings.DegradeThreshold = true, true, "pause", 1
	settings.IntervalSeconds, settings.RecoveryIntervalSeconds = 300, 3600
	settings, err = db.SaveIntelligenceCheckSettings(ctx, settings)
	if err != nil {
		t.Fatal(err)
	}
	intelligenceRun(t, db, id, time.Now().UTC(), "22")
	settings.AdaptiveEnabled = false
	savedAt := time.Now()
	if _, err := db.SaveIntelligenceCheckSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	accounts, _, err := db.ListIntelligenceCheckAccounts(ctx, IntelligenceCheckAccountFilter{AccountID: id})
	if err != nil || len(accounts) != 1 {
		t.Fatalf("accounts=%+v err=%v", accounts, err)
	}
	a := accounts[0]
	if a.OwnedAction != "pause" || a.NextIntervalSeconds != 3600 || a.NextDueAt == nil || a.NextDueAt.Before(savedAt.Add(3590*time.Second)) {
		t.Fatalf("disabling adaptive did not adopt fixed recovery cadence: %+v", a)
	}
}
