package database

import (
	"context"
	"encoding/json"
	"github.com/codex2api/smartops"
	"time"
)

// Quality plans own these observations. Priority scheduling only reads their
// latest completed evidence and never writes or creates a second quality ledger.
func (db *DB) mergeSmartOpsQualitySignals(ctx context.Context, c smartops.PriorityConfig, signals map[int64]smartops.Signal) error {
	var exists bool
	query := `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='account_quality_rounds')`
	if !db.isSQLite() {
		query = `SELECT to_regclass('account_quality_rounds') IS NOT NULL`
	}
	if e := db.conn.QueryRowContext(ctx, query).Scan(&exists); e != nil {
		return e
	}
	if !exists {
		return nil
	}
	rows, e := db.conn.QueryContext(ctx, `SELECT q.account_id,q.record FROM account_quality_rounds q JOIN (SELECT account_id,MAX(id) AS id FROM account_quality_rounds WHERE created_at>=$1 GROUP BY account_id) latest ON latest.id=q.id`, db.timeArg(time.Now().Add(-time.Duration(c.QualityMaxAgeHours)*time.Hour)))
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var raw string
		if e = rows.Scan(&id, &raw); e != nil {
			return e
		}
		var round struct {
			PassedCount int       `json:"passed_count"`
			TotalCount  int       `json:"total_count"`
			CompletedAt time.Time `json:"completed_at"`
			Outcome     string    `json:"outcome"`
		}
		if e = json.Unmarshal([]byte(raw), &round); e != nil {
			return e
		}
		if round.TotalCount <= 0 || round.CompletedAt.IsZero() || round.CompletedAt.Before(time.Now().Add(-time.Duration(c.QualityMaxAgeHours)*time.Hour)) {
			continue
		}
		signal := signals[id]
		signal.QualityPercent = float64(round.PassedCount) * 100 / float64(round.TotalCount)
		signal.QualityObservedUnix = round.CompletedAt.Unix()
		signal.QualityKnown = true
		signals[id] = signal
	}
	return rows.Err()
}
