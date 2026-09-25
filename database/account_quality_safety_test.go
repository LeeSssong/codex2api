package database

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/codex2api/accountops"
	"github.com/google/uuid"
)

func newAccountQualitySafetyDB(t *testing.T, driver string) *DB {
	t.Helper()
	if driver == "sqlite" {
		return guardTestDB(t, driver)
	}
	if os.Getenv("CODEX2API_TEST_POSTGRES_DSN") == "" {
		t.Skip("requires isolated CODEX2API_TEST_POSTGRES_DSN")
	}
	db, err := newAccountOpsTestDB(t)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func newQualitySafetyPlan(t *testing.T, db *DB, action string) (accountops.Plan, int64) {
	t.Helper()
	ctx := context.Background()
	id, err := db.InsertAccountWithCredentials(ctx, "quality-"+uuid.NewString(), map[string]any{"refresh_token": "test-" + uuid.NewString()}, "")
	if err != nil {
		t.Fatal(err)
	}
	group, err := db.CreateAccountGroup(ctx, "quality-"+uuid.NewString(), "", "", 0, 0, sql.NullInt64{})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.SetAccountGroups(ctx, id, []int64{group}); err != nil {
		t.Fatal(err)
	}
	p, err := db.SaveAccountQualityPlan(ctx, accountops.Plan{AccountID: id, Enabled: true, Model: "model", Prompt: "q", ExpectedAnswer: "a", Cron: "* * * * *", Samples: 2, Action: action, RemoveGroupIDs: []int64{group}, AutoRestore: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.TriggerAccountQualityPlan(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	claimed, err := db.ClaimAccountQualityPlan(ctx, time.Now())
	if err != nil || claimed == nil || claimed.ID != p.ID {
		t.Fatalf("claim %+v %v", claimed, err)
	}
	return *claimed, group
}

func TestAccountQualityRestoreRequiresOwnedControlRevision(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			for _, change := range []string{"unchanged", "manual_same_value", "manual_aba", "manual_account_edit", "group_aba", "other_guard_aba"} {
				t.Run(change, func(t *testing.T) {
					db := newAccountQualitySafetyDB(t, driver)
					ctx := context.Background()
					p, g := newQualitySafetyPlan(t, db, "disable_scheduling")
					apply := func(outcome string) string {
						t.Helper()
						got, err := db.ApplyAccountQualityOutcome(ctx, p, outcome)
						if err != nil {
							t.Fatal(err)
						}
						return got
					}
					if got := apply("failed"); got != "scheduling_disabled" {
						t.Fatal(got)
					}
					run := func(q string, args ...any) {
						t.Helper()
						if _, err := db.conn.ExecContext(ctx, q, args...); err != nil {
							t.Fatal(err)
						}
					}
					switch change {
					case "manual_same_value":
						run("UPDATE accounts SET enabled=FALSE WHERE id=$1", p.AccountID)
					case "manual_aba":
						run("UPDATE accounts SET enabled=TRUE WHERE id=$1", p.AccountID)
						run("UPDATE accounts SET enabled=FALSE WHERE id=$1", p.AccountID)
					case "manual_account_edit":
						run("UPDATE accounts SET name='manual' WHERE id=$1", p.AccountID)
					case "group_aba":
						if err := db.SetAccountGroups(ctx, p.AccountID, nil); err != nil {
							t.Fatal(err)
						}
						if err := db.SetAccountGroups(ctx, p.AccountID, []int64{g}); err != nil {
							t.Fatal(err)
						}
					case "other_guard_aba":
						run("UPDATE accounts SET status='error',error_message='token_guard:verified_auth_failure' WHERE id=$1", p.AccountID)
						run("UPDATE accounts SET status='active',error_message='' WHERE id=$1", p.AccountID)
					}
					want := "restore_conflict"
					if change == "unchanged" {
						want = "restored"
					}
					if got := apply("passed"); got != want {
						t.Fatalf("%s: restore=%s want %s", change, got, want)
					}
					row, err := db.GetAccountByID(ctx, p.AccountID)
					if err != nil {
						t.Fatal(err)
					}
					if row.Enabled != (change == "unchanged") {
						t.Fatalf("manual/guard state overwritten: enabled=%v", row.Enabled)
					}
					// Release this plan's lease so isolated PG runs do not claim an older test fixture.
					if err := db.FinishAccountQualityRound(ctx, p, accountops.Round{PlanID: p.ID, AccountID: p.AccountID, StartedAt: time.Now(), CompletedAt: time.Now()}); err != nil {
						t.Fatal(err)
					}
				})
			}
		})
	}
}

func TestAccountQualityUnknownRoundCannotRestoreOrQuarantine(t *testing.T) {
	db := newAccountQualitySafetyDB(t, "sqlite")
	ctx := context.Background()
	p, _ := newQualitySafetyPlan(t, db, "disable_scheduling")
	for _, stage := range []string{"before", "after"} {
		if stage == "after" {
			if _, err := db.ApplyAccountQualityOutcome(ctx, p, "failed"); err != nil {
				t.Fatal(err)
			}
		}
		before, _ := db.AccountControlRevision(ctx, p.AccountID)
		for _, samples := range [][]accountops.Sample{{{Judgment: accountops.Judgment{Verdict: "unknown"}}}, {{Judgment: accountops.Judgment{Verdict: "correct"}}, {Error: "timeout"}}} {
			outcome := accountops.Outcome(samples)
			got, err := db.ApplyAccountQualityOutcome(ctx, p, outcome)
			if err != nil || got != "inconclusive" {
				t.Fatalf("%s: %s %v", stage, got, err)
			}
		}
		after, _ := db.AccountControlRevision(ctx, p.AccountID)
		if after != before {
			t.Fatal("unknown round mutated account")
		}
	}
	allCorrect := []accountops.Sample{{Judgment: accountops.Judgment{Verdict: "correct"}}, {Judgment: accountops.Judgment{Verdict: "correct"}}}
	if got, err := db.ApplyAccountQualityOutcome(ctx, p, accountops.Outcome(allCorrect)); err != nil || got != "restored" {
		t.Fatal(fmt.Sprint(got, err))
	}
}

func TestAccountQualityRemovedGroupEditRevokesRecovery(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			db := newAccountQualitySafetyDB(t, driver)
			ctx := context.Background()
			p, g := newQualitySafetyPlan(t, db, "remove_groups")
			if got, err := db.ApplyAccountQualityOutcome(ctx, p, "failed"); err != nil || got != "groups_removed" {
				t.Fatalf("isolate %s %v", got, err)
			}
			// Even a same-value update is a manual group edit after quality removed membership.
			if _, err := db.conn.ExecContext(ctx, "UPDATE account_groups SET color=color WHERE id=$1", g); err != nil {
				t.Fatal(err)
			}
			if got, err := db.ApplyAccountQualityOutcome(ctx, p, "passed"); err != nil || got != "restore_conflict" {
				t.Fatalf("removed group changed: %s %v", got, err)
			}
			ids, err := db.GetAccountGroupIDs(ctx, p.AccountID)
			if err != nil || len(ids) != 0 {
				t.Fatalf("stale membership restored: %v %v", ids, err)
			}
			if err := db.FinishAccountQualityRound(ctx, p, accountops.Round{PlanID: p.ID, AccountID: p.AccountID, StartedAt: time.Now(), CompletedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAccountQualityAccountEditDuringRoundPreventsIsolation(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			db := newAccountQualitySafetyDB(t, driver)
			ctx := context.Background()
			p, _ := newQualitySafetyPlan(t, db, "disable_scheduling")
			p.ControlRevision, _ = db.AccountControlRevision(ctx, p.AccountID)
			if _, err := db.conn.ExecContext(ctx, "UPDATE accounts SET enabled=TRUE WHERE id=$1", p.AccountID); err != nil {
				t.Fatal(err)
			}
			if got, err := db.ApplyAccountQualityOutcome(ctx, p, "failed"); err != nil || got != "account_changed" {
				t.Fatalf("stale quality result applied: %s %v", got, err)
			}
			row, err := db.GetAccountByID(ctx, p.AccountID)
			if err != nil || !row.Enabled {
				t.Fatal("manual change was overwritten")
			}

		})
	}
}

func TestAccountQualityCredentialChangeDuringRoundPreventsIsolation(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			db := newAccountQualitySafetyDB(t, driver)
			ctx := context.Background()
			p, _ := newQualitySafetyPlan(t, db, "disable_scheduling")
			row, err := db.GetAccountByID(ctx, p.AccountID)
			if err != nil {
				t.Fatal(err)
			}
			p.CredentialGeneration = row.CredentialGeneration
			if _, err := db.conn.ExecContext(ctx, "UPDATE accounts SET credential_generation=credential_generation+1 WHERE id=$1", p.AccountID); err != nil {
				t.Fatal(err)
			}
			if got, err := db.ApplyAccountQualityOutcome(ctx, p, "failed"); err != nil || got != "account_changed" {
				t.Fatalf("stale credential result applied: %s %v", got, err)
			}
			row, err = db.GetAccountByID(ctx, p.AccountID)
			if err != nil || !row.Enabled {
				t.Fatal("new credential isolated by previous generation result")
			}

		})
	}
}

// A failed replacement must leave the old fence installed, including when a
// second application instance is already serving writes against this schema.
func TestAccountQualityPostgresTriggerReplacementIsAtomic(t *testing.T) {
	db := newAccountQualitySafetyDB(t, "postgres")
	ctx := context.Background()
	p, group := newQualitySafetyPlan(t, db, "remove_groups")
	if action, err := db.ApplyAccountQualityOutcome(ctx, p, "failed"); err != nil || action != "groups_removed" {
		t.Fatalf("isolate: %s %v", action, err)
	}
	var schema string
	if err := db.conn.QueryRowContext(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	eventName := "quality_replace_fail_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	function := quotePostgresIdent(schema) + "." + quotePostgresIdent(eventName)
	createFunction := fmt.Sprintf("CREATE FUNCTION %s() RETURNS event_trigger LANGUAGE plpgsql AS $$ BEGIN IF current_schema() = '%s' AND position('CREATE TRIGGER account_quality_removed_group_ownership ' in current_query()) > 0 THEN RAISE EXCEPTION 'quality_replacement_failpoint'; END IF; END $$", function, strings.ReplaceAll(schema, "'", "''"))
	if _, err := db.conn.ExecContext(ctx, createFunction); err != nil {
		t.Fatal(err)
	}
	// Event-trigger names are database-wide; use a unique name and filter on
	// this fixture's schema so concurrent root/guard fixtures are unaffected.
	dropEvent := "DROP EVENT TRIGGER IF EXISTS " + quotePostgresIdent(eventName)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := db.conn.ExecContext(cleanup, dropEvent); err != nil {
			t.Errorf("remove test failpoint: %v", err)
		}
	})
	if _, err := db.conn.ExecContext(ctx, "CREATE EVENT TRIGGER "+quotePostgresIdent(eventName)+" ON ddl_command_start WHEN TAG IN ('CREATE TRIGGER') EXECUTE FUNCTION "+function+"()"); err != nil {
		t.Fatal(err)
	}
	if err := db.ensureAccountQualityOwnershipTriggers(ctx); err == nil || !strings.Contains(err.Error(), "quality_replacement_failpoint") {
		t.Fatalf("expected injected replacement failure, got %v", err)
	}
	var fences int
	if err := db.conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM pg_trigger WHERE tgrelid='account_groups'::regclass AND tgname='account_quality_removed_group_ownership'").Scan(&fences); err != nil {
		t.Fatal(err)
	}
	if fences != 1 {
		t.Fatalf("failed replacement removed the existing ownership fence: count=%d", fences)
	}
	if _, err := db.conn.ExecContext(ctx, "UPDATE account_groups SET name=name WHERE id=$1", group); err != nil {
		t.Fatal(err)
	}
	if action, err := db.ApplyAccountQualityOutcome(ctx, p, "passed"); err != nil || action != "restore_conflict" {
		t.Fatalf("old fence did not protect an intervening group edit: %s %v", action, err)
	}
	if _, err := db.conn.ExecContext(ctx, dropEvent); err != nil {
		t.Fatal(err)
	}
	if err := db.ensureAccountQualityOwnershipTriggers(ctx); err != nil {
		t.Fatalf("replacement after failpoint removal: %v", err)
	}
}
