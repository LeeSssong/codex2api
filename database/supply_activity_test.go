package database

import (
	"context"
	"testing"
	"time"
)

func TestSupplyActivitySeparatesRetriesProbesAndDeliveredBilling(t *testing.T) {
	db := newPromptPolicySQLiteTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 20, 8, 0, 0, 0, time.UTC)
	inputs := []*UsageLogInput{
		{AccountID: 1, RequestID: "failed", StatusCode: 401, AttemptIndex: 1, IsRetryAttempt: true},
		{AccountID: 2, RequestID: "success", StatusCode: 200, AttemptIndex: 2},
		{AccountID: 1, RequestID: "probe", StatusCode: 200, InternalReason: "test_probe"},
		{AccountID: 1, RequestID: "throttle", StatusCode: 429, AttemptIndex: 2, IsRetryAttempt: true},
		{AccountID: 1, RequestID: "old", StatusCode: 200},
		{AccountID: 1, RequestID: "outside", StatusCode: 200},
		{AccountID: 1, RequestID: "future", StatusCode: 200},
		{AccountID: 3, RequestID: "other-model", StatusCode: 200},
	}
	for _, input := range inputs {
		input.Channel, input.Model, input.Endpoint = "codex", "gpt-6-astra", "/v1/responses"
		if input.RequestID == "other-model" {
			input.Model = "gpt-5.6-sol"
		}
		if err := db.InsertUsageLog(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	db.FlushUsageLogs()
	for _, input := range inputs {
		at := now.Add(-time.Minute)
		switch input.RequestID {
		case "old":
			at = now.Add(-30 * time.Minute)
		case "outside":
			at = now.Add(-61 * time.Minute)
		case "future":
			at = now
		}
		if _, err := db.conn.ExecContext(ctx, "UPDATE usage_logs SET created_at=$1, account_billed=$2 WHERE request_id=$3", db.timeArg(at), map[string]int{"failed": 99, "success": 2, "probe": 77, "old": 3, "outside": 100, "future": 100, "other-model": 100}[input.RequestID], input.RequestID); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.GetSupplyActivity(ctx, now, []string{"gpt-6-astra"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows=%+v", rows)
	}
	a, b := rows[0], rows[1]
	if a.Attempts != 3 || a.Successes != 1 || a.Unauthorized != 1 || a.RateLimited != 1 || a.Retries != 1 || a.RetriedFailures != 2 || a.InternalAttempts != 1 || a.Billed60m != 3 || a.Billed15m != 0 {
		t.Fatalf("attempt/probe accounting: %+v", a)
	}
	if b.Successes != 1 || b.Retries != 1 || b.Billed15m != 2 || b.Billed60m != 2 {
		t.Fatalf("rotated success: %+v", b)
	}
	if a.Last401At == nil || !a.Last401At.Equal(now.Add(-time.Minute)) || a.LastSuccessAt == nil || !a.LastSuccessAt.Equal(now.Add(-30*time.Minute)) {
		t.Fatalf("timestamps: %+v", a)
	}
}
