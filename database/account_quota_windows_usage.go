package database

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// GetAccountQuotaWindowUsage aggregates the current page's official quota
// windows over [start, asOf). All four metrics share the same bounds and valid
// end-user log filters. Spark consumes a separate quota and is excluded.
// A successful empty window has explicit zero metrics; query errors return nil.
func (db *DB) GetAccountQuotaWindowUsage(ctx context.Context, windows map[int64]time.Time, asOf time.Time) (map[int64]*AccountTimeRangeUsage, error) {
	result := make(map[int64]*AccountTimeRangeUsage, len(windows))
	ids := make([]int64, 0, len(windows))
	for id, start := range windows {
		if id > 0 && !start.IsZero() && !start.After(asOf) {
			ids = append(ids, id)
			result[id] = &AccountTimeRangeUsage{AccountID: id}
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	const batchSize = 500
	for offset := 0; offset < len(ids); offset += batchSize {
		end := min(offset+batchSize, len(ids))
		values := make([]string, 0, end-offset)
		args := []interface{}{db.timeArg(asOf)}
		for _, id := range ids[offset:end] {
			n := len(args) + 1
			if db.isSQLite() {
				values = append(values, fmt.Sprintf("($%d,$%d)", n, n+1))
			} else {
				values = append(values, fmt.Sprintf("($%d::BIGINT,$%d::TIMESTAMPTZ)", n, n+1))
			}
			args = append(args, id, db.timeArg(windows[id]))
		}
		query := fmt.Sprintf(`WITH quota_windows(account_id,start_at) AS (VALUES %s)
   SELECT usage_logs.account_id, COUNT(*), COALESCE(SUM(total_tokens),0),
    COALESCE(SUM(account_billed),0), COALESCE(SUM(user_billed),0)
   FROM quota_windows JOIN usage_logs ON usage_logs.account_id=quota_windows.account_id
    AND usage_logs.created_at >= quota_windows.start_at AND usage_logs.created_at < $1
   WHERE status_code <> 499 AND %s AND %s AND %s
    AND LOWER(TRIM(COALESCE(NULLIF(effective_model,''),model,''))) <> 'gpt-5.3-codex-spark'
   GROUP BY usage_logs.account_id`, strings.Join(values, ","), db.nonRetryUsageLogPredicate(), db.currentAccountUsageGenerationPredicate(), db.endUserUsageLogPredicate())
		rows, err := db.conn.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			value := &AccountTimeRangeUsage{}
			if err := rows.Scan(&value.AccountID, &value.Requests, &value.Tokens, &value.AccountBilled, &value.UserBilled); err != nil {
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
