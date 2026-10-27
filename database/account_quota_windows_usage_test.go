package database

import (
	"context"
	"testing"
	"time"
)

func TestAccountQuotaWindowsBoundariesAndFilters(t *testing.T) {
	db := newGrokStateTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	start := now.Add(-4 * time.Hour)
	for _, r := range []struct {
		id            int64
		at            time.Time
		status        int
		retry         bool
		reason, model string
		generation    int64
	}{
		{1, start, 200, false, "", "gpt-5.4", 0},
		{1, now.Add(-time.Hour), 500, false, "", "gpt-5.4", 0},
		{1, start.Add(-time.Second), 200, false, "", "gpt-5.4", 0},
		{1, now, 200, false, "", "gpt-5.4", 0},
		{1, now.Add(time.Second), 200, false, "", "gpt-5.4", 0},
		{1, start, 499, false, "", "gpt-5.4", 0},
		{1, start, 200, true, "", "gpt-5.4", 0},
		{1, start, 200, false, "probe", "gpt-5.4", 0},
		{1, start, 200, false, "", " GPT-5.3-CODEX-SPARK ", 0},
		{2, now.Add(-8 * 24 * time.Hour), 200, false, "", "gpt-5.4", 0},
		{2, now.Add(-31 * 24 * time.Hour), 200, false, "", "gpt-5.4", 0},
		{4, start, 200, false, "", "gpt-5.4", 0},
	} {
		if _, err := db.conn.ExecContext(ctx, `INSERT INTO usage_logs(account_id,created_at,status_code,is_retry_attempt,internal_reason,model,credential_generation,account_billed,user_billed,total_tokens) VALUES($1,$2,$3,$4,$5,$6,$7,6,9,20)`, r.id, db.timeArg(r.at), r.status, r.retry, r.reason, r.model, r.generation); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.GetAccountQuotaWindowUsage(ctx, map[int64]time.Time{1: start, 2: now.Add(-30 * 24 * time.Hour), 3: start}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[1] == nil || got[1].Requests != 6 || got[1].Tokens != 120 || got[1].AccountBilled != 36 || got[1].UserBilled != 54 {
		t.Fatalf("bounded account 1=%+v, len=%d", got[1], len(got))
	}
	if got[2] == nil || got[2].Requests != 1 || got[2].AccountBilled != 6 {
		t.Fatalf("30d account 2=%+v", got[2])
	}
	if got[3] == nil || got[3].Requests != 0 || got[3].AccountBilled != 0 {
		t.Fatalf("empty window must be explicit zero: %+v", got[3])
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := db.GetAccountQuotaWindowUsage(canceled, map[int64]time.Time{1: start}, now); err == nil {
		t.Fatal("query error must not become zero usage")
	}
}

func TestAccountQuotaWindowJustOpenedIsExplicitZero(t *testing.T) {
	db := newGrokStateTestDB(t)
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	got, err := db.GetAccountQuotaWindowUsage(context.Background(), map[int64]time.Time{1: now, 2: now.Add(time.Second), 0: now.Add(-time.Hour)}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1] == nil || got[1].Requests != 0 || got[1].AccountBilled != 0 {
		t.Fatalf("just-opened empty window=%+v", got)
	}
}

func TestAccountQuotaWindowRespectsEffectiveModelAndCredentialGeneration(t *testing.T) {
	db := newGrokStateTestDB(t)
	ctx := context.Background()
	id, err := db.InsertAccountWithCredentials(ctx, "quota-generation", map[string]interface{}{"refresh_token": "quota-generation-rt"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.ExecContext(ctx, `UPDATE accounts SET credential_generation=2 WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	for _, r := range []struct {
		model, effective string
		generation       int64
	}{
		{"gpt-5.4", "gpt-5.3-codex-spark", 2},
		{"gpt-5.3-codex-spark", "gpt-5.4", 2},
		{"gpt-5.4", "", 1},
		{"gpt-5.4", "", 0},
	} {
		if _, err := db.conn.ExecContext(ctx, `INSERT INTO usage_logs(account_id,status_code,model,effective_model,credential_generation,account_billed,total_tokens,created_at) VALUES($1,200,$2,$3,$4,6,20,$5)`, id, r.model, r.effective, r.generation, db.timeArg(now.Add(-time.Minute))); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.GetAccountQuotaWindowUsage(ctx, map[int64]time.Time{id: now.Add(-time.Hour)}, now)
	if err != nil {
		t.Fatal(err)
	}
	if got[id] == nil || got[id].Requests != 3 || got[id].AccountBilled != 18 || got[id].Tokens != 60 {
		t.Fatalf("effective model/generation aggregation=%+v", got[id])
	}
}

func TestAccountQuotaSourceAdmittedUsageIsStatusAndGenerationIndependent(t *testing.T) {
	db := newGrokStateTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	// Source ledger includes partial/cancelled upstream results, not diagnostic rows.
	rows := []struct {
		status   int
		retry    bool
		reason   string
		admitted bool
		cost     float64
	}{
		{200, false, "", true, 1}, {499, false, "", true, 2}, {502, true, "", true, 3},
		{200, false, "overflow_compact_summary", true, 4}, {200, false, "connection_test", false, 8},
		{503, true, "", false, 16}, {200, false, "", true, 0},
	}
	for _, r := range rows {
		_, err := db.conn.ExecContext(ctx, `INSERT INTO usage_logs(account_id,created_at,status_code,is_retry_attempt,internal_reason,credential_generation,model,account_billed,user_billed,total_tokens,account_cost,standard_cost,quota_usage_admitted,quota_tokens) VALUES(1,$1,$2,$3,$4,99,'gpt-5.4',99,7,99,$5,6,$6,10)`, db.timeArg(now.Add(-time.Minute)), r.status, r.retry, r.reason, r.cost, r.admitted)
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.GetAccountQuotaWindowUsage(ctx, map[int64]time.Time{1: now.Add(-time.Hour)}, now)
	if err != nil {
		t.Fatal(err)
	}
	if got[1].Requests != 5 || got[1].AccountBilled != 10 || got[1].Tokens != 50 {
		t.Fatalf("source admitted rows=%+v", got[1])
	}
}
