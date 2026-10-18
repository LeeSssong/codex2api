package database

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// SupplyActivity keeps diagnostic attempts separate from delivered business traffic.
// Times and counters cover only the trailing hour, including older credentials.
type SupplyActivity struct {
	AccountID        int64      `json:"account_id"`
	Attempts         int64      `json:"business_attempts_60m"`
	Successes        int64      `json:"business_successes_60m"`
	Retries          int64      `json:"retry_attempts_60m"`
	RetriedFailures  int64      `json:"failures_followed_by_retry_60m"`
	Unauthorized     int64      `json:"unauthorized_attempts_60m"`
	RateLimited      int64      `json:"rate_limited_attempts_60m"`
	InternalAttempts int64      `json:"internal_attempts_60m"`
	Billed60m        float64    `json:"successful_billed_usd_60m"`
	Billed15m        float64    `json:"successful_billed_usd_15m"`
	LastAttemptAt    *time.Time `json:"last_business_attempt_at"`
	LastSuccessAt    *time.Time `json:"last_business_success_at"`
	Last401At        *time.Time `json:"last_business_401_at"`
}

// GetSupplyActivity scans one bounded time range. It includes removed accounts in
// demand, so rotating credentials or deleting an account cannot erase demand.
func (db *DB) GetSupplyActivity(ctx context.Context, now time.Time, models []string) (result []SupplyActivity, resultErr error) {
	result = []SupplyActivity{}
	if len(models) == 0 {
		return result, nil
	}
	args := []interface{}{db.timeArg(now.Add(-time.Hour)), db.timeArg(now), db.timeArg(now.Add(-15 * time.Minute))}
	places := make([]string, 0, len(models))
	for _, model := range models {
		args = append(args, model)
		places = append(places, fmt.Sprintf("$%d", len(args)))
	}
	business := db.endUserUsageLogPredicate()
	success := "(" + business + " AND status_code >= 200 AND status_code < 300 AND " + db.nonRetryUsageLogPredicate() + ")"
	query := fmt.Sprintf(`SELECT account_id,
	 SUM(CASE WHEN %[1]s THEN 1 ELSE 0 END),
	 SUM(CASE WHEN %[2]s THEN 1 ELSE 0 END),
	 SUM(CASE WHEN %[1]s AND attempt_index > 1 THEN 1 ELSE 0 END),
	 SUM(CASE WHEN %[1]s AND NOT (%[3]s) THEN 1 ELSE 0 END),
	 SUM(CASE WHEN %[1]s AND status_code = 401 THEN 1 ELSE 0 END),
	 SUM(CASE WHEN %[1]s AND status_code = 429 THEN 1 ELSE 0 END),
	 SUM(CASE WHEN NOT (%[1]s) THEN 1 ELSE 0 END),
	 COALESCE(SUM(CASE WHEN %[2]s THEN account_billed ELSE 0 END), 0),
	 COALESCE(SUM(CASE WHEN %[2]s AND created_at >= $3 THEN account_billed ELSE 0 END), 0),
	 MAX(CASE WHEN %[1]s THEN created_at END),
	 MAX(CASE WHEN %[2]s THEN created_at END),
	 MAX(CASE WHEN %[1]s AND status_code = 401 THEN created_at END)
	 FROM usage_logs WHERE created_at >= $1 AND created_at < $2
	 AND (channel = 'codex' OR COALESCE(channel, '') = '')
	 AND COALESCE(NULLIF(effective_model, ''), model) IN (%[4]s)
	 GROUP BY account_id ORDER BY account_id`, business, success, db.nonRetryUsageLogPredicate(), strings.Join(places, ","))
	rows, err := db.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query supply activity: %w", err)
	}
	defer func() {
		if errClose := rows.Close(); errClose != nil && resultErr == nil {
			resultErr = fmt.Errorf("close supply activity rows: %w", errClose)
		}
	}()
	for rows.Next() {
		var row SupplyActivity
		var last, succeeded, unauthorized interface{}
		if errScan := rows.Scan(&row.AccountID, &row.Attempts, &row.Successes, &row.Retries, &row.RetriedFailures,
			&row.Unauthorized, &row.RateLimited, &row.InternalAttempts, &row.Billed60m, &row.Billed15m,
			&last, &succeeded, &unauthorized); errScan != nil {
			return nil, fmt.Errorf("scan supply activity: %w", errScan)
		}
		for _, field := range []struct {
			raw interface{}
			out **time.Time
		}{{last, &row.LastAttemptAt}, {succeeded, &row.LastSuccessAt}, {unauthorized, &row.Last401At}} {
			if field.raw == nil {
				continue
			}
			parsed, errTime := parseDBTimeValue(field.raw)
			if errTime != nil {
				return nil, fmt.Errorf("parse supply activity time: %w", errTime)
			}
			*field.out = &parsed
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
