package database

import (
	"context"
	"database/sql"
	"fmt"
)

// This compact financial ledger is independent of diagnostic log retention. It
// stores no request IDs, clients, prompts, credentials, or response metadata.
// event_id is created once before buffering and deduplicates internal requeues.
func (db *DB) migrateAccountUsageCostEvents(ctx context.Context) error {
	timeType := "TIMESTAMPTZ"
	if db.isSQLite() {
		timeType = "TEXT"
	}
	_, err := db.conn.ExecContext(ctx, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS account_usage_cost_events (
 event_id TEXT PRIMARY KEY,
 account_id BIGINT NOT NULL,
 created_at %s NOT NULL,
 model TEXT NOT NULL DEFAULT '',
 quota_tokens BIGINT NOT NULL,
 account_cost DOUBLE PRECISION NOT NULL,
 account_stats_cost DOUBLE PRECISION,
 standard_cost DOUBLE PRECISION NOT NULL,
 account_rate_multiplier DOUBLE PRECISION,
 account_cost_source TEXT NOT NULL DEFAULT '',
 account_pricing_snapshot TEXT NOT NULL DEFAULT '',
 user_billed DOUBLE PRECISION NOT NULL
 ); CREATE INDEX IF NOT EXISTS idx_account_usage_cost_events_window ON account_usage_cost_events(account_id,created_at);`, timeType))
	return err
}
func (db *DB) insertAccountUsageCostEvents(ctx context.Context, tx *sql.Tx, batch []usageLogEntry) error {
	var stmt *sql.Stmt
	defer func() {
		if stmt != nil {
			_ = stmt.Close()
		}
	}()
	for _, e := range batch {
		if e.AccountUsageEventID == "" {
			continue
		}
		if stmt == nil {
			var err error
			stmt, err = tx.PrepareContext(ctx, `INSERT INTO account_usage_cost_events(event_id,account_id,created_at,model,quota_tokens,account_cost,account_stats_cost,standard_cost,account_rate_multiplier,account_cost_source,account_pricing_snapshot,user_billed)
   VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT(event_id) DO NOTHING`)
			if err != nil {
				return err
			}
		}
		var at any = e.AccountUsageCreatedAt
		if db.isSQLite() {
			at = sqliteTimeParam(e.AccountUsageCreatedAt)
		}
		if _, err := stmt.ExecContext(ctx, e.AccountUsageEventID, e.AccountID, at, e.AccountUsageModel, e.QuotaTokens, e.AccountCost, e.AccountStatsCost, e.StandardCost, e.AccountRateMultiplier, e.AccountCostSource, e.AccountPricingSnapshot, e.UserBilled); err != nil {
			return err
		}
	}
	return nil
}
