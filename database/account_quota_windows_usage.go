package database

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// GetAccountQuotaWindowUsage aggregates the current page's official quota
// windows from the source-compatible start onward. An admitted upstream result
// is not filtered by status, retry or credential generation. Spark is a distinct
// source shadow-account scope and never merged into the ordinary parent window.
// A successful empty window has explicit zero metrics; query errors return nil.
func (db *DB) GetAccountQuotaWindowUsage(ctx context.Context, windows map[int64]time.Time, asOf time.Time) (map[int64]*AccountTimeRangeUsage, error) {
	result := make(map[int64]*AccountTimeRangeUsage, len(windows))
	ids := make([]int64, 0, len(windows))
	for id, start := range windows {
		if id > 0 && !start.IsZero() {
			ids = append(ids, id)
			result[id] = &AccountTimeRangeUsage{AccountID: id}
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	const batchSize = 500
	for offset := 0; offset < len(ids); offset += batchSize {
		end := min(offset+batchSize, len(ids))
		values := make([]string, 0, end-offset)
		args := []interface{}{}
		for _, id := range ids[offset:end] {
			n := len(args) + 1
			if db.isSQLite() {
				values = append(values, fmt.Sprintf("($%d,$%d)", n, n+1))
			} else {
				values = append(values, fmt.Sprintf("($%d::BIGINT,$%d::TIMESTAMPTZ)", n, n+1))
			}
			args = append(args, id, db.timeArg(windows[id]))
		}
		// The compact account-cost ledger is independent of request-log mode.
		// Snapshot-bearing log rows are not unioned again: they already have a
		// durable event. Only pre-parity rows use the explicitly marked fallback.
		query := fmt.Sprintf(`WITH quota_windows(account_id,start_at) AS (VALUES %s),
   quota_events AS (
    SELECT e.account_id,e.created_at,e.model,e.quota_tokens AS tokens,e.account_cost AS cost,
     e.standard_cost,e.user_billed,0 AS legacy
    FROM account_usage_cost_events e
    JOIN quota_windows w ON e.account_id=w.account_id AND e.created_at>=w.start_at
    UNION ALL
    SELECT u.account_id,u.created_at,COALESCE(NULLIF(u.effective_model,''),u.model,''),COALESCE(u.total_tokens,0),
     %s,%s,u.user_billed,1 AS legacy
    FROM usage_logs u JOIN quota_windows w ON u.account_id=w.account_id AND u.created_at>=w.start_at
    WHERE u.quota_usage_admitted IS NULL
     AND TRIM(COALESCE(u.internal_reason,'')) IN ('','overflow_compact_summary','prompt_intelligence_cy_analysis')
     AND (u.status_code BETWEEN 200 AND 299 OR COALESCE(u.input_tokens,0)+COALESCE(u.output_tokens,0)>0 OR COALESCE(u.account_billed,0)>0)
   )
   SELECT account_id,COUNT(*),COALESCE(SUM(tokens),0),COALESCE(SUM(cost),0),
    COALESCE(SUM(user_billed),0),COALESCE(SUM(standard_cost),0),SUM(legacy)
   FROM quota_events WHERE LOWER(TRIM(model)) <> 'gpt-5.3-codex-spark'
   GROUP BY account_id`, strings.Join(values, ","), AccountCostSQL("u"), StandardCostSQL("u"))
		rows, err := db.conn.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			value := &AccountTimeRangeUsage{}
			if err := rows.Scan(&value.AccountID, &value.Requests, &value.Tokens, &value.AccountBilled, &value.UserBilled, &value.StandardCost, &value.LegacyRequests); err != nil {
				rows.Close()
				return nil, err
			}
			result[value.AccountID] = value
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}
