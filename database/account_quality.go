package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/codex2api/accountops"
	"github.com/google/uuid"
	"time"
)

const qualityPlanColumns = `id,account_id,config,enabled,version,next_run,lease`

func scanAccountQualityPlan(row interface{ Scan(...any) error }) (accountops.Plan, error) {
	var p accountops.Plan
	var raw string
	var next any
	var id, account, version int64
	var enabled bool
	var lease string
	e := row.Scan(&id, &account, &raw, &enabled, &version, &next, &lease)
	if e != nil {
		return p, e
	}
	if e = json.Unmarshal([]byte(raw), &p); e != nil {
		return p, e
	}
	p.ID, p.AccountID, p.Enabled, p.Version, p.Lease = id, account, enabled, version, lease
	p.NextRun, e = parseDBTimeValue(next)
	return p, e
}
func (db *DB) SaveAccountQualityPlan(ctx context.Context, p accountops.Plan) (accountops.Plan, error) {
	if p.MaxResults == 0 {
		p.MaxResults = 100
	}
	next, e := p.Next(time.Now().UTC())
	if e != nil {
		return p, e
	}
	p.NextRun = next
	raw, e := json.Marshal(p)
	if e != nil {
		return p, e
	}
	e = db.withWriteTx(ctx, func(tx *sql.Tx) error {
		var exists bool
		if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM accounts WHERE id=$1 AND status<>'deleted')`, p.AccountID).Scan(&exists); e != nil {
			return e
		}
		if !exists {
			return fmt.Errorf("account unavailable")
		}
		groups := append([]int64{}, p.RemoveGroupIDs...)
		if p.Judge != nil {
			groups = append(groups, p.Judge.GroupID)
		}
		for _, id := range groups {
			if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM account_groups WHERE id=$1)`, id).Scan(&exists); e != nil {
				return e
			}
			if !exists {
				return fmt.Errorf("group %d unavailable", id)
			}
		}
		if p.ID == 0 {
			e := tx.QueryRowContext(ctx, `INSERT INTO account_quality_plans(account_id,config,enabled,next_run) VALUES($1,$2,$3,$4) RETURNING id,version`, p.AccountID, string(raw), p.Enabled, db.timeArg(next)).Scan(&p.ID, &p.Version)
			if e != nil {
				return e
			}
		} else {
			e := tx.QueryRowContext(ctx, `UPDATE account_quality_plans SET config=$1,enabled=$2,next_run=$3,version=version+1 WHERE id=$4 AND account_id=$5 AND version=$6 RETURNING version`, string(raw), p.Enabled, db.timeArg(next), p.ID, p.AccountID, p.Version).Scan(&p.Version)
			if e != nil {
				return e
			}
		}
		return nil
	})
	return p, e
}
func (db *DB) ListAccountQualityPlans(ctx context.Context) ([]accountops.Plan, error) {
	rows, e := db.conn.QueryContext(ctx, `SELECT `+qualityPlanColumns+` FROM account_quality_plans WHERE EXISTS(SELECT 1 FROM accounts WHERE accounts.id=account_quality_plans.account_id AND status<>'deleted' AND COALESCE(error_message,'')<>'deleted') ORDER BY id DESC`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []accountops.Plan{}
	for rows.Next() {
		p, e := scanAccountQualityPlan(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (db *DB) TriggerAccountQualityPlan(ctx context.Context, id int64) error {
	return db.withSQLiteWriteLock(ctx, func() error {
		r, e := db.conn.ExecContext(ctx, `UPDATE account_quality_plans SET next_run=$1 WHERE id=$2 AND enabled=TRUE AND (lease='' OR lease_until<$1)`, db.timeArg(time.Now().UTC()), id)
		if e != nil {
			return e
		}
		n, _ := r.RowsAffected()
		if n == 0 {
			return sql.ErrNoRows
		}
		return nil
	})
}
func (db *DB) DeleteAccountQualityPlan(ctx context.Context, id int64) error {
	return db.withSQLiteWriteLock(ctx, func() error {
		_, e := db.conn.ExecContext(ctx, `DELETE FROM account_quality_plans WHERE id=$1`, id)
		return e
	})
}
func (db *DB) ClaimAccountQualityPlan(ctx context.Context, now time.Time) (p *accountops.Plan, err error) {
	if err = db.ExpireQualityTests(ctx, now); err != nil {
		return nil, err
	}
	err = db.withWriteTx(ctx, func(tx *sql.Tx) error {
		q := `SELECT ` + qualityPlanColumns + ` FROM account_quality_plans WHERE enabled=TRUE AND next_run<=$1 AND (lease='' OR lease_until<$1) AND EXISTS(SELECT 1 FROM accounts WHERE accounts.id=account_quality_plans.account_id AND status<>'deleted' AND COALESCE(error_message,'')<>'deleted') AND NOT EXISTS(SELECT 1 FROM quality_test_jobs WHERE quality_test_jobs.account_id=account_quality_plans.account_id AND slot IS NOT NULL) ORDER BY next_run LIMIT 1`
		if !db.isSQLite() {
			q += ` FOR UPDATE SKIP LOCKED`
		}
		v, e := scanAccountQualityPlan(tx.QueryRowContext(ctx, q, db.timeArg(now)))
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		v.Lease = uuid.NewString()
		next, e := v.Next(now)
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `UPDATE account_quality_plans SET lease=$1,lease_until=$2,next_run=$3 WHERE id=$4`, v.Lease, db.timeArg(now.Add(15*time.Minute)), db.timeArg(next), v.ID)
		if e != nil {
			return e
		}
		p = &v
		return nil
	})
	return
}
func (db *DB) FinishAccountQualityRound(ctx context.Context, p accountops.Plan, r accountops.Round) error {
	return db.withWriteTx(ctx, func(tx *sql.Tx) error {
		raw, e := json.Marshal(r)
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO account_quality_rounds(plan_id,account_id,created_at,record) SELECT id,account_id,$1,$2 FROM account_quality_plans WHERE id=$3`, db.timeArg(r.StartedAt), string(raw), p.ID)
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `UPDATE account_quality_plans SET lease='',lease_until=NULL WHERE id=$1 AND lease=$2`, p.ID, p.Lease)
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `DELETE FROM account_quality_rounds WHERE created_at<$1 OR (plan_id=$2 AND id NOT IN (SELECT id FROM account_quality_rounds WHERE plan_id=$2 ORDER BY id DESC LIMIT 200))`, db.timeArg(time.Now().Add(-7*24*time.Hour)), p.ID)
		if e != nil {
			return e
		}
		return nil
	})
}
func (db *DB) ListAccountQualityHistory(ctx context.Context, before int64, detail bool) ([]accountops.Round, error) {
	q := `SELECT id,record FROM account_quality_rounds WHERE ($1=0 OR id<$1) ORDER BY id DESC LIMIT 101`
	rows, e := db.conn.QueryContext(ctx, q, before)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []accountops.Round{}
	for rows.Next() {
		var r accountops.Round
		var raw string
		var id int64
		if e = rows.Scan(&id, &raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(raw), &r); e != nil {
			return nil, e
		}
		r.ID = id
		if !detail {
			r.Results = nil
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (db *DB) GetAccountQualityRound(ctx context.Context, id int64) (accountops.Round, error) {
	var r accountops.Round
	var raw string
	e := db.conn.QueryRowContext(ctx, `SELECT record FROM account_quality_rounds WHERE id=$1`, id).Scan(&raw)
	if e == nil {
		e = json.Unmarshal([]byte(raw), &r)
		r.ID = id
	}
	return r, e
}

type qualityRecovery struct {
	Action           string    `json:"action"`
	GroupsBefore     []int64   `json:"groups_before"`
	GroupsAfter      []int64   `json:"groups_after"`
	Removed          []int64   `json:"removed"`
	EnabledBefore    bool      `json:"enabled_before"`
	AccountUpdatedAt time.Time `json:"account_updated_at"`
}

func qualityTxGroups(ctx context.Context, tx *sql.Tx, id int64) ([]int64, error) {
	rows, e := tx.QueryContext(ctx, `SELECT group_id FROM account_group_members WHERE account_id=$1 ORDER BY group_id`, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var v int64
		if e = rows.Scan(&v); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Both the recovery baseline and all account mutations commit together. Native
// account/group triggers emit scheduler outbox events in this same transaction.
func (db *DB) ApplyAccountQualityOutcome(ctx context.Context, p accountops.Plan, outcome string) (action string, err error) {
	action = "no_change"
	err = db.withWriteTx(ctx, func(tx *sql.Tx) error {
		q := `SELECT version,enabled,lease,lease_until FROM account_quality_plans WHERE id=$1`
		if !db.isSQLite() {
			q += ` FOR UPDATE`
		}
		var version int64
		var enabled bool
		var lease string
		var until any
		e := tx.QueryRowContext(ctx, q, p.ID).Scan(&version, &enabled, &lease, &until)
		if errors.Is(e, sql.ErrNoRows) {
			action = "stale_run"
			return nil
		}
		if e != nil {
			return e
		}
		expires, e := parseDBNullTimeValue(until)
		if e != nil {
			return e
		}
		if version != p.Version || !enabled || lease != p.Lease || lease == "" || !expires.Valid || time.Now().After(expires.Time) {
			action = "stale_run"
			return nil
		}
		if p.JobID > 0 {
			q := `SELECT status FROM quality_test_jobs WHERE id=$1`
			if !db.isSQLite() {
				q += ` FOR UPDATE`
			}
			var jobStatus string
			if e = tx.QueryRowContext(ctx, q, p.JobID).Scan(&jobStatus); e != nil {
				return e
			}
			if jobStatus != "running" {
				action = "cancelled"
				return nil
			}
		}
		q = `SELECT status,enabled,updated_at,credentials FROM accounts WHERE id=$1 AND status<>'deleted' AND COALESCE(error_message,'')<>'deleted'`
		if !db.isSQLite() {
			q += ` FOR UPDATE`
		}
		var status string
		var accountEnabled bool
		var updated, credentials any
		if e = tx.QueryRowContext(ctx, q, p.AccountID).Scan(&status, &accountEnabled, &updated, &credentials); e != nil {
			if errors.Is(e, sql.ErrNoRows) {
				action = "account_deleted"
				return nil
			}
			return e
		}
		if outcome == "inconclusive" {
			action = "inconclusive"
			return nil
		}
		_, e = parseDBTimeValue(updated)
		if e != nil {
			return e
		}
		groups, e := qualityTxGroups(ctx, tx, p.AccountID)
		if e != nil {
			return e
		}
		var raw string
		var baseline qualityRecovery
		e = tx.QueryRowContext(ctx, `SELECT snapshot FROM account_quality_recovery WHERE plan_id=$1`, p.ID).Scan(&raw)
		has := e == nil
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if has {
			if e = json.Unmarshal([]byte(raw), &baseline); e != nil {
				return e
			}
		}
		if outcome == "failed" {
			if has {
				action = "already_quarantined"
				return nil
			}

			baseline = qualityRecovery{Action: p.Action, GroupsBefore: groups, EnabledBefore: accountEnabled}
			switch p.Action {
			case "remove_groups":
				for _, g := range groups {
					remove := false
					for _, target := range p.RemoveGroupIDs {
						if target == g {
							remove = true
							break
						}
					}
					if remove {
						baseline.Removed = append(baseline.Removed, g)
						if _, e = tx.ExecContext(ctx, `DELETE FROM account_group_members WHERE account_id=$1 AND group_id=$2`, p.AccountID, g); e != nil {
							return e
						}
					}
				}
				if len(baseline.Removed) == 0 {
					action = "no_change"
					return nil
				}
				action = "groups_removed"
			case "disable_scheduling":
				if !accountEnabled {
					action = "no_change"
					return nil
				}
				if _, e = tx.ExecContext(ctx, `UPDATE accounts SET enabled=FALSE WHERE id=$1`, p.AccountID); e != nil {
					return e
				}
				action = "scheduling_disabled"
			}
			now := time.Now().UTC()
			if _, e = tx.ExecContext(ctx, `UPDATE accounts SET updated_at=$1 WHERE id=$2`, db.timeArg(now), p.AccountID); e != nil {
				return e
			}
			baseline.AccountUpdatedAt = now
			baseline.GroupsAfter, e = qualityTxGroups(ctx, tx, p.AccountID)
			if e != nil {
				return e
			}
			b, e := json.Marshal(baseline)
			if e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, `INSERT INTO account_quality_recovery(plan_id,snapshot) VALUES($1,$2)`, p.ID, string(b)); e != nil {
				return e
			}
		} else if outcome == "passed" && p.AutoRestore && has {
			if status != "active" {
				action = "restore_conflict"
				return nil
			}
			if baseline.Action == "remove_groups" {
				for _, g := range baseline.Removed {
					var exists bool
					if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM account_groups WHERE id=$1)`, g).Scan(&exists); e != nil {
						return e
					}
					if !exists {
						action = "restore_conflict"
						return nil
					}
				}
				for _, g := range baseline.Removed {
					if _, e = tx.ExecContext(ctx, `INSERT INTO account_group_members(account_id,group_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, p.AccountID, g); e != nil {
						return e
					}
				}
				action = "restored"
			} else {
				creds := decodeCredentials(credentials)
				for _, key := range []string{"subscription_expires_at"} {
					if value, ok := creds[key].(string); ok {
						if t, e := time.Parse(time.RFC3339, value); e == nil && t.Before(time.Now()) {
							action = "restore_conflict"
							return nil
						}
					}
				}
				if _, e = tx.ExecContext(ctx, `UPDATE accounts SET enabled=$1 WHERE id=$2`, baseline.EnabledBefore, p.AccountID); e != nil {
					return e
				}
				action = "restored"
			}
			if _, e = tx.ExecContext(ctx, `UPDATE accounts SET updated_at=$1 WHERE id=$2`, db.timeArg(time.Now().UTC()), p.AccountID); e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, `DELETE FROM account_quality_recovery WHERE plan_id=$1`, p.ID); e != nil {
				return e
			}
		}
		if outcome == "passed" && action == "no_change" {
			action = "passed"
		}
		return nil
	})
	return
}

// Cleanup counts samples, as source max_results does, rather than retaining
// max_results full parallel rounds. It also runs for paused rules.
func (db *DB) CleanupAccountQualityHistory(ctx context.Context) error {
	plans, e := db.ListAccountQualityPlans(ctx)
	if e != nil {
		return e
	}
	return db.withWriteTx(ctx, func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, `DELETE FROM account_quality_rounds WHERE created_at<$1`, db.timeArg(time.Now().Add(-7*24*time.Hour))); e != nil {
			return e
		}
		for _, p := range plans {
			remaining := p.MaxResults
			if remaining == 0 {
				remaining = 100
			}
			rows, e := tx.QueryContext(ctx, `SELECT id,record FROM account_quality_rounds WHERE plan_id=$1 ORDER BY id DESC`, p.ID)
			if e != nil {
				return e
			}
			type item struct {
				id    int64
				round accountops.Round
			}
			var items []item
			for rows.Next() {
				var id int64
				var raw string
				var r accountops.Round
				if e = rows.Scan(&id, &raw); e != nil {
					rows.Close()
					return e
				}
				if e = json.Unmarshal([]byte(raw), &r); e != nil {
					rows.Close()
					return e
				}
				items = append(items, item{id, r})
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return e
			}
			for _, item := range items {
				if remaining <= 0 {
					if _, e = tx.ExecContext(ctx, `DELETE FROM account_quality_rounds WHERE id=$1`, item.id); e != nil {
						return e
					}
					continue
				}
				count := len(item.round.Results)
				if count > remaining {
					item.round.Results = item.round.Results[count-remaining:]
					item.round.PassedCount = 0
					for _, r := range item.round.Results {
						if r.Error == "" && r.Verdict == "correct" {
							item.round.PassedCount++
						}
					}
					raw, e := json.Marshal(item.round)
					if e != nil {
						return e
					}
					if _, e = tx.ExecContext(ctx, `UPDATE account_quality_rounds SET record=$1 WHERE id=$2`, string(raw), item.id); e != nil {
						return e
					}
				}
				remaining -= count
			}
		}
		return nil
	})
}
func (db *DB) DeferAccountQualityPlan(ctx context.Context, p accountops.Plan) error {
	return db.withSQLiteWriteLock(ctx, func() error {
		_, e := db.conn.ExecContext(ctx, `UPDATE account_quality_plans SET lease='',lease_until=NULL,next_run=$1 WHERE id=$2 AND version=$3 AND lease=$4`, db.timeArg(time.Now().UTC()), p.ID, p.Version, p.Lease)
		return e
	})
}
