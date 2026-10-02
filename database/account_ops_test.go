package database

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/codex2api/accountops"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAccountOpsPersistenceLeasesAndRestore(t *testing.T) {
	db, e := newAccountOpsTestDB(t)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	_, e = db.conn.Exec(`INSERT INTO accounts(id,name,credentials,status,enabled) VALUES(100,'test','{}','active',TRUE)`)
	if e != nil {
		t.Fatal(e)
	}
	g, e := db.CreateAccountGroup(ctx, "group", "", "", 0, 0, sql.NullInt64{})
	if e != nil {
		t.Fatal(e)
	}
	if e = db.SetAccountGroups(ctx, 100, []int64{g}); e != nil {
		t.Fatal(e)
	}
	p := accountops.Plan{AccountID: 100, Enabled: true, Model: "test", Prompt: "question", Cron: "*/5 * * * *", Samples: 2, ExpectedAnswer: "21", Action: "remove_groups", RemoveGroupIDs: []int64{g}, AutoRestore: true}
	p, e = db.SaveAccountQualityPlan(ctx, p)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.SaveAccountQualityPlan(ctx, accountops.Plan{AccountID: 100}); e == nil {
		t.Fatal("duplicate accepted")
	}
	if e = db.TriggerAccountQualityPlan(ctx, p.ID); e != nil {
		t.Fatal(e)
	}
	claimed, e := db.ClaimAccountQualityPlan(ctx, time.Now())
	if e != nil || claimed == nil {
		t.Fatalf("claim: %v", e)
	}
	if other, e := db.ClaimAccountQualityPlan(ctx, time.Now()); e != nil || other != nil {
		t.Fatalf("double claim: %v", e)
	}
	action, e := db.ApplyAccountQualityOutcome(ctx, *claimed, "failed")
	if e != nil || action != "groups_removed" {
		t.Fatalf("action %s %v", action, e)
	}
	ids, _ := db.GetAccountGroupIDs(ctx, 100)
	if len(ids) != 0 {
		t.Fatal("group not removed")
	}
	action, e = db.ApplyAccountQualityOutcome(ctx, *claimed, "passed")
	if e != nil || action != "restored" {
		t.Fatalf("restore %s %v", action, e)
	}
	ids, _ = db.GetAccountGroupIDs(ctx, 100)
	if len(ids) != 1 || ids[0] != g {
		t.Fatal("group not restored")
	}
	action, e = db.ApplyAccountQualityOutcome(ctx, *claimed, "failed")
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.conn.Exec(`UPDATE accounts SET name='manual',updated_at=$1 WHERE id=100`, db.timeArg(time.Now().Add(time.Second)))
	if e != nil {
		t.Fatal(e)
	}
	action, e = db.ApplyAccountQualityOutcome(ctx, *claimed, "passed")
	if e != nil || action != "restore_conflict" {
		t.Fatalf("source restoration after account edit %s %v", action, e)
	}
	p.Enabled = false
	if _, e = db.SaveAccountQualityPlan(ctx, p); e != nil {
		t.Fatal(e)
	}
	action, e = db.ApplyAccountQualityOutcome(ctx, *claimed, "failed")
	if e != nil || action != "stale_run" {
		t.Fatalf("stale applied %s %v", action, e)
	}
}
func TestAccountOpsAlertStateAndLease(t *testing.T) {
	db, e := newAccountOpsTestDB(t)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	r := NewAccountOpsRepository(db)
	event := accountops.AccountOpsEvent{AccountID: 1, Kind: "balance_low", AccountName: "name", Signal: "balance_error_code", HTTPStatus: 402}
	if e = r.Record(ctx, event); e != nil {
		t.Fatal(e)
	}
	if e = r.Record(ctx, event); e != nil {
		t.Fatal(e)
	}
	got, e := r.Claim(ctx)
	if e != nil || got == nil || got.Occurrences != 2 {
		t.Fatalf("claim %+v %v", got, e)
	}
	other, e := r.Claim(ctx)
	if e != nil || other != nil {
		t.Fatal("claimed twice")
	}
	if e = r.Complete(ctx, got, "sent", time.Hour); e != nil {
		t.Fatal(e)
	}
	if e = r.Record(ctx, event); e != nil {
		t.Fatal(e)
	}
	other, e = r.Claim(ctx)
	if e != nil || other != nil {
		t.Fatal("cooldown ignored")
	}
	items, e := r.List(ctx, 0, 20)
	if e != nil || len(items) != 1 || items[0].State != "sent" || items[0].Occurrences != 3 {
		t.Fatalf("list %+v %v", items, e)
	}
}
func TestAccountQualitySoftDeletedAndBusyTriggers(t *testing.T) {
	db, e := newAccountOpsTestDB(t)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	_, e = db.conn.Exec(`INSERT INTO accounts(id,name,credentials,status,enabled) VALUES(101,'test','{}','active',TRUE)`)
	if e != nil {
		t.Fatal(e)
	}
	p, e := db.SaveAccountQualityPlan(ctx, accountops.Plan{AccountID: 101, Enabled: true, Model: "model", Prompt: "q", ExpectedAnswer: "a", Action: "disable_scheduling", Cron: "* * * * *", Samples: 1})
	if e != nil {
		t.Fatal(e)
	}
	if e = db.TriggerAccountQualityPlan(ctx, p.ID); e != nil {
		t.Fatal(e)
	}
	claimed, e := db.ClaimAccountQualityPlan(ctx, time.Now())
	if e != nil || claimed == nil {
		t.Fatal(e)
	}
	if e = db.TriggerAccountQualityPlan(ctx, p.ID); e == nil {
		t.Fatal("running plan triggered")
	}
	_, e = db.conn.Exec(`UPDATE accounts SET status='deleted' WHERE id=101`)
	if e != nil {
		t.Fatal(e)
	}
	action, e := db.ApplyAccountQualityOutcome(ctx, *claimed, "failed")
	if e != nil || action != "account_deleted" {
		t.Fatalf("deleted account changed %s %v", action, e)
	}
	var enabled bool
	db.conn.QueryRow(`SELECT enabled FROM accounts WHERE id=101`).Scan(&enabled)
	if !enabled {
		t.Fatal("deleted account disabled")
	}
}
func TestAccountQualityCancelledJobFencesActionAndRetentionCountsSamples(t *testing.T) {
	db, e := newAccountOpsTestDB(t)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	_, e = db.conn.Exec(`INSERT INTO accounts(id,name,credentials,status,enabled) VALUES(102,'test','{}','active',TRUE)`)
	if e != nil {
		t.Fatal(e)
	}
	p, e := db.SaveAccountQualityPlan(ctx, accountops.Plan{AccountID: 102, Enabled: true, Model: "model", Prompt: "q", ExpectedAnswer: "a", Action: "disable_scheduling", Cron: "* * * * *", Samples: 2, MaxResults: 3})
	if e != nil {
		t.Fatal(e)
	}
	if e = db.TriggerAccountQualityPlan(ctx, p.ID); e != nil {
		t.Fatal(e)
	}
	claimed, e := db.ClaimAccountQualityPlan(ctx, time.Now())
	if e != nil || claimed == nil {
		t.Fatal(e)
	}
	job, e := db.CreateQualityTestJob(ctx, QualityTestJob{AccountID: 102})
	if e != nil {
		t.Fatal(e)
	}
	claimed.JobID = job.ID
	if e = db.CancelQualityTest(ctx, job.ID); e != nil {
		t.Fatal(e)
	}
	action, e := db.ApplyAccountQualityOutcome(ctx, *claimed, "failed")
	if e != nil || action != "cancelled" {
		t.Fatalf("cancelled result applied: %s %v", action, e)
	}
	for i := 0; i < 3; i++ {
		r := accountops.Round{PlanID: p.ID, StartedAt: time.Now(), TotalCount: 2, Results: []accountops.Sample{{Judgment: accountops.Judgment{Verdict: "correct"}}, {Judgment: accountops.Judgment{Verdict: "incorrect"}}}}
		if e = db.FinishAccountQualityRound(ctx, p, r); e != nil {
			t.Fatal(e)
		}
	}
	if e = db.CleanupAccountQualityHistory(ctx); e != nil {
		t.Fatal(e)
	}
	rounds, e := db.ListAccountQualityHistory(ctx, 0, true)
	if e != nil || len(rounds) != 2 || len(rounds[0].Results)+len(rounds[1].Results) != 3 {
		t.Fatalf("sample retention %+v %v", rounds, e)
	}
}

func newAccountOpsTestDB(t *testing.T) (*DB, error) {
	t.Helper()
	dsn := os.Getenv("CODEX2API_TEST_POSTGRES_DSN")
	if dsn == "" {
		db, e := New("sqlite", filepath.Join(t.TempDir(), "ops.db"))
		if e == nil {
			t.Cleanup(func() { db.Close() })
		}
		return db, e
	}
	schema := fmt.Sprintf("account_ops_%d", time.Now().UnixNano())
	parsed, e := url.Parse(dsn)
	if e != nil {
		return nil, e
	}
	q := parsed.Query()
	q.Set("search_path", schema+",public")
	parsed.RawQuery = q.Encode()
	db, e := New("postgres", parsed.String(), schema)
	if e == nil {
		t.Cleanup(func() {
			db.DrainBackgroundTasks(5 * time.Second)
			db.conn.Exec(`DROP SCHEMA ` + quotePostgresIdent(schema) + ` CASCADE`)
			db.Close()
		})
	}
	return db, e
}
func TestAccountQualityClaimExpiresCrashedNativeJob(t *testing.T) {
	db, e := newAccountOpsTestDB(t)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	_, e = db.conn.Exec(`INSERT INTO accounts(id,name,credentials,status,enabled) VALUES(103,'test','{}','active',TRUE)`)
	if e != nil {
		t.Fatal(e)
	}
	p, e := db.SaveAccountQualityPlan(ctx, accountops.Plan{AccountID: 103, Enabled: true, Model: "model", Prompt: "q", ExpectedAnswer: "a", Action: "disable_scheduling", Cron: "* * * * *", Samples: 1})
	if e != nil {
		t.Fatal(e)
	}
	if e = db.TriggerAccountQualityPlan(ctx, p.ID); e != nil {
		t.Fatal(e)
	}
	job, e := db.CreateQualityTestJob(ctx, QualityTestJob{AccountID: 103})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.conn.Exec(`UPDATE quality_test_jobs SET deadline_at=$1 WHERE id=$2`, db.timeArg(time.Now().Add(-time.Hour)), job.ID); e != nil {
		t.Fatal(e)
	}
	claimed, e := db.ClaimAccountQualityPlan(ctx, time.Now())
	if e != nil || claimed == nil {
		t.Fatalf("expired native job blocks schedule: %v", e)
	}
	got, e := db.GetQualityTestJob(ctx, job.ID)
	if e != nil || got.Status != "interrupted" {
		t.Fatalf("crashed job not expired: %+v %v", got, e)
	}
}
