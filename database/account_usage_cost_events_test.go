package database

import (
	"context"
	"math"
	"testing"
)

func TestAccountCostLedgerIndependentOfDiagnosticLogMode(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			db := guardTestDB(t, driver)
			if driver == "postgres" {
				defer db.Close()
			}
			ctx := context.Background()
			for _, mode := range []string{UsageLogModeFull, UsageLogModeErrors, UsageLogModeOff} {
				t.Run(mode, func(t *testing.T) {
					db.SetUsageLogConfig(mode, 100, 60)
					var beforeLogs, beforeEvents int
					if e := db.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_logs`).Scan(&beforeLogs); e != nil {
						t.Fatal(e)
					}
					if e := db.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_usage_cost_events`).Scan(&beforeEvents); e != nil {
						t.Fatal(e)
					}
					admitted := true
					tokens := int64(1100)
					input := &UsageLogInput{AccountID: 91, Model: "gpt-5.4", InputTokens: 1000, CachedTokens: 200, OutputTokens: 100, StatusCode: 200, ClientIP: "must-not-persist", ClientUserAgent: "must-not-persist"}
					input.QuotaUsageAdmitted = &admitted
					input.QuotaTokens = &tokens
					if e := db.InsertUsageLog(ctx, input); e != nil {
						t.Fatal(e)
					}
					// A diagnostic-only row has no independent account cost event.
					diagnostic := *input
					denied := false
					diagnostic.QuotaUsageAdmitted = &denied
					if e := db.InsertUsageLog(ctx, &diagnostic); e != nil {
						t.Fatal(e)
					}
					db.FlushUsageLogs()
					var logs, events int
					var cost float64
					var gotTokens int64
					if e := db.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_logs`).Scan(&logs); e != nil {
						t.Fatal(e)
					}
					if e := db.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_usage_cost_events`).Scan(&events); e != nil {
						t.Fatal(e)
					}
					if events-beforeEvents != 1 {
						t.Fatalf("events delta=%d", events-beforeEvents)
					}
					if mode == UsageLogModeFull && logs-beforeLogs != 2 {
						t.Fatalf("all logs delta=%d", logs-beforeLogs)
					}
					if mode != UsageLogModeFull && logs != beforeLogs {
						t.Fatal("diagnostic log retention overridden")
					}
					if e := db.conn.QueryRowContext(ctx, `SELECT account_cost,quota_tokens FROM account_usage_cost_events ORDER BY created_at DESC,event_id DESC LIMIT 1`).Scan(&cost, &gotTokens); e != nil {
						t.Fatal(e)
					}
					if math.Abs(cost-.00355) > 1e-10 || gotTokens != 1100 {
						t.Fatalf("ledger fee%v tokens%d", cost, gotTokens)
					}
				})
			}
		})
	}
}

func TestAccountCostLedgerRequeueIsIdempotent(t *testing.T) {
	db := guardTestDB(t, "sqlite")
	ctx := context.Background()
	db.SetUsageLogConfig(UsageLogModeOff, 100, 60)
	admitted := true
	input := &UsageLogInput{AccountID: 92, Model: "gpt-5.4", InputTokens: 1000, StatusCode: 200}
	input.QuotaUsageAdmitted = &admitted
	if e := db.InsertUsageLog(ctx, input); e != nil {
		t.Fatal(e)
	}
	db.logMu.Lock()
	batch := append([]usageLogEntry(nil), db.logBuf...)
	db.logMu.Unlock()
	if len(batch) != 1 {
		t.Fatalf("buffered %d", len(batch))
	}
	if e := db.insertSQLiteUsageLogBatch(ctx, batch); e != nil {
		t.Fatal(e)
	}
	if e := db.insertSQLiteUsageLogBatch(ctx, batch); e != nil {
		t.Fatal(e)
	}
	var events int
	if e := db.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_usage_cost_events`).Scan(&events); e != nil {
		t.Fatal(e)
	}
	if events != 1 {
		t.Fatalf("retried event duplicated %d", events)
	}
}
