package database

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestIntelligenceCheckAutomaticCooldownPauseResume(t *testing.T) {
	db := intelligenceDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	s, err := db.GetIntelligenceCheckSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s.Enabled = true
	s, err = db.SaveIntelligenceCheckSettings(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	id := intelligenceAccount(t, db)
	before, _, err := db.ListIntelligenceCheckAccounts(ctx, IntelligenceCheckAccountFilter{AccountID: id})
	if err != nil {
		t.Fatal(err)
	}
	until := now.Add(time.Hour)
	if err = db.SetCooldown(ctx, id, "rate_limited_7d", until); err != nil {
		t.Fatal(err)
	}
	if n, err := db.EnqueueDueIntelligenceChecks(ctx, now, 100); err != nil || n != 0 {
		t.Fatalf("cooling account enqueued: count=%d err=%v", n, err)
	}
	after, _, err := db.ListIntelligenceCheckAccounts(ctx, IntelligenceCheckAccountFilter{AccountID: id})
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("cooldown changed enrollment/history: before=%+v after=%+v err=%v", before, after, err)
	}
	if n, err := db.EnqueueDueIntelligenceChecks(ctx, until, 100); err != nil || n != 1 {
		t.Fatalf("expired cooldown did not resume: count=%d err=%v", n, err)
	}
	run, err := db.ClaimIntelligenceCheck(ctx, "worker", until, time.Minute)
	if err != nil || run == nil || run.AccountID != id || run.Source != "automatic" {
		t.Fatalf("resume claim=%+v err=%v", run, err)
	}
	persisted, err := db.GetIntelligenceCheckSettings(ctx)
	if err != nil || !reflect.DeepEqual(s, persisted) {
		t.Fatalf("user settings changed: %+v err=%v", persisted, err)
	}
}

func TestIntelligenceCheckQueuedCooldownDoesNotStarveAvailable(t *testing.T) {
	db := intelligenceDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	s, err := db.GetIntelligenceCheckSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s.Enabled = true
	if _, err = db.SaveIntelligenceCheckSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, 101)
	for i := range ids {
		ids[i] = intelligenceAccount(t, db)
	}
	if n, err := db.EnqueueDueIntelligenceChecks(ctx, now, 200); err != nil || n != len(ids) {
		t.Fatal(n, err)
	}
	until := now.Add(time.Hour)
	for _, id := range ids {
		if err = db.SetCooldown(ctx, id, "rate_limited_5h", until); err != nil {
			t.Fatal(err)
		}
	}
	available := intelligenceAccount(t, db)
	if n, err := db.EnqueueDueIntelligenceChecks(ctx, now, 100); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	run, err := db.ClaimIntelligenceCheck(ctx, "worker", now, time.Minute)
	if err != nil || run == nil || run.AccountID != available {
		t.Fatalf("cooling queue blocked available account: %+v err=%v", run, err)
	}
	if run, err = db.ClaimIntelligenceCheck(ctx, "cooldown", now, time.Minute); err != nil || run != nil {
		t.Fatalf("queued cooling account claimed: %+v err=%v", run, err)
	}
	if err = db.ClearCooldown(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}
	run, err = db.ClaimIntelligenceCheck(ctx, "recovered", now, time.Minute)
	if err != nil || run == nil || run.AccountID != ids[0] {
		t.Fatalf("cleared cooldown did not resume queued check: %+v err=%v", run, err)
	}
	// Explicit manual checks keep their existing behavior, including promotion.
	if _, err = db.EnqueueIntelligenceChecks(ctx, []int64{ids[1]}, now); err != nil {
		t.Fatal(err)
	}
	run, err = db.ClaimIntelligenceCheck(ctx, "manual", now, time.Minute)
	if err != nil || run == nil || run.AccountID != ids[1] || run.Source != "manual" {
		t.Fatalf("manual check blocked: %+v err=%v", run, err)
	}
}

func TestIntelligenceCheckCooldownDoesNotEnableManualSwitches(t *testing.T) {
	for _, mode := range []string{"global", "enrollment", "account"} {
		t.Run(mode, func(t *testing.T) {
			db := intelligenceDB(t)
			ctx := context.Background()
			now := time.Now().UTC()
			s, _ := db.GetIntelligenceCheckSettings(ctx)
			s.Enabled = true
			if _, err := db.SaveIntelligenceCheckSettings(ctx, s); err != nil {
				t.Fatal(err)
			}
			id := intelligenceAccount(t, db)
			if n, err := db.EnqueueDueIntelligenceChecks(ctx, now, 100); err != nil || n != 1 {
				t.Fatal(n, err)
			}
			if err := db.SetCooldown(ctx, id, "rate_limited_7d", now.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "global":
				s, _ = db.GetIntelligenceCheckSettings(ctx)
				s.Enabled = false
				if _, err := db.SaveIntelligenceCheckSettings(ctx, s); err != nil {
					t.Fatal(err)
				}
			case "enrollment":
				if err := db.SetIntelligenceCheckEnabled(ctx, []int64{id}, false); err != nil {
					t.Fatal(err)
				}
			case "account":
				if err := db.SetAccountEnabled(ctx, id, false); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.ClearCooldown(ctx, id); err != nil {
				t.Fatal(err)
			}
			if run, err := db.ClaimIntelligenceCheck(ctx, "worker", now.Add(2*time.Hour), time.Minute); err != nil || run != nil {
				t.Fatalf("manual %s switch overridden: %+v %v", mode, run, err)
			}
			if n, err := db.EnqueueDueIntelligenceChecks(ctx, now.Add(2*time.Hour), 100); err != nil || n != 0 {
				t.Fatal(n, err)
			}
		})
	}
}

func TestIntelligenceCheckSQLiteCooldownFormats(t *testing.T) {
	// Existing SQLite rows include both Go String timestamps and SQL/RFC formats.
	// Dropping either reader silently prevents automatic recovery after expiry.
	now := time.Date(2026, 10, 28, 1, 2, 3, 123000000, time.UTC)
	for _, stamp := range []string{
		"2026-10-28 01:02:03.123 +0000 UTC",
		"2026-10-28 09:02:03.123 +0800 CST",
		"2026-10-27 21:02:03.123 -0400 EDT",
		"2026-10-28T01:02:03.123Z",
		"2026-10-28 01:02:03.123",
	} {
		t.Run(stamp, func(t *testing.T) {
			db := intelligenceDB(t)
			ctx := context.Background()
			s, _ := db.GetIntelligenceCheckSettings(ctx)
			s.Enabled = true
			if _, err := db.SaveIntelligenceCheckSettings(ctx, s); err != nil {
				t.Fatal(err)
			}
			id := intelligenceAccount(t, db)
			if _, err := db.conn.ExecContext(ctx, `UPDATE accounts SET cooldown_until=$1 WHERE id=$2`, stamp, id); err != nil {
				t.Fatal(err)
			}
			if n, err := db.EnqueueDueIntelligenceChecks(ctx, now.Add(-time.Millisecond), 100); err != nil || n != 0 {
				t.Fatal("cooldown ended early", n, err)
			}
			if n, err := db.EnqueueDueIntelligenceChecks(ctx, now, 100); err != nil || n != 1 {
				t.Fatal("cooldown did not expire", n, err)
			}
		})
	}
}
