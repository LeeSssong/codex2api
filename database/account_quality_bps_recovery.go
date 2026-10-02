package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/codex2api/accountops"
)

type QualityBPSRecoveryCandidate struct {
	AccountID  int64
	Generation int64
	Revision   int64
	DisabledAt int64
	Policy     accountops.QualityBPSPolicy
}

func bpsRecoveryInterval(p accountops.QualityBPSPolicy) time.Duration {
	if p.RecoveryIntervalMinutes != nil {
		return time.Duration(*p.RecoveryIntervalMinutes) * time.Minute
	}
	return time.Hour
}

func (db *DB) DisableQualityBPSOn403(ctx context.Context, id, generation int64) (changed bool, err error) {
	err = db.withWriteTx(ctx, func(tx *sql.Tx) error {
		enabled, e := db.PluginEnabledTx(ctx, tx, "quality-ops")
		if e != nil || !enabled {
			return e
		}
		revision, e := db.LockAccountControlTx(ctx, tx, id)
		if e != nil {
			return e
		}
		var currentGen int64
		if e := tx.QueryRowContext(ctx, `SELECT credential_generation FROM accounts WHERE id=$1`, id).Scan(&currentGen); e != nil {
			return e
		}
		if currentGen != generation {
			return nil
		}
		var raw string
		var allowed int
		e = tx.QueryRowContext(ctx, `SELECT quality_bps,allowed FROM account_codex_paths WHERE account_id=$1 AND upstream='basispoints'`, id).Scan(&raw, &allowed)
		if errors.Is(e, sql.ErrNoRows) || e == nil && (raw == "" || allowed == 0) {
			return nil
		}
		if e != nil {
			return e
		}
		var policy accountops.QualityBPSPolicy
		if e := json.Unmarshal([]byte(raw), &policy); e != nil {
			return e
		}
		if !policy.AutoDisableOn403 && !policy.AutoMoveOn403 {
			return nil
		}
		if policy.AutoMoveOn403 {
			if policy.TargetGroupID > 0 {
				var exists bool
				if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM account_groups WHERE id=$1)`, policy.TargetGroupID).Scan(&exists); e != nil {
					return e
				}
				if !exists {
					return nil
				}
			}
			if _, e := tx.ExecContext(ctx, `DELETE FROM account_group_members WHERE account_id=$1`, id); e != nil {
				return e
			}
			if policy.TargetGroupID > 0 {
				if _, e := tx.ExecContext(ctx, `INSERT INTO account_group_members(account_id,group_id) VALUES($1,$2)`, id, policy.TargetGroupID); e != nil {
					return e
				}
			}
		}
		if policy.AutoDisableOn403 {
			now := time.Now()
			if _, e := tx.ExecContext(ctx, `UPDATE account_codex_paths SET allowed=0,quality_bps_disabled_at=$1,quality_bps_recovery_at=$2 WHERE account_id=$3 AND upstream='basispoints'`, now.UnixNano(), now.Add(bpsRecoveryInterval(policy)).Unix(), id); e != nil {
				return e
			}
		}
		// Keep ownership current only for the exact rule snapshot this action owns.
		nextRevision, e := db.LockAccountControlTx(ctx, tx, id)
		if e != nil {
			return e
		}
		if policy.AutoDisableOn403 {
			if _, e := tx.ExecContext(ctx, `UPDATE account_codex_paths SET quality_bps_owner_revision=$1 WHERE account_id=$2 AND upstream='basispoints'`, nextRevision, id); e != nil {
				return e
			}
		}
		if e := db.refreshQualityBPSOwnershipTx(ctx, tx, id, raw, revision, nextRevision); e != nil {
			return e
		}
		changed = true
		return db.AccountControlOutboxTx(ctx, tx, id)
	})
	return
}

func (db *DB) refreshQualityBPSOwnershipTx(ctx context.Context, tx *sql.Tx, id int64, policy string, oldRevision, newRevision int64) error {
	rows, e := tx.QueryContext(ctx, `SELECT r.plan_id,r.snapshot FROM account_quality_recovery r JOIN account_quality_plans p ON p.id=r.plan_id WHERE p.account_id=$1`, id)
	if e != nil {
		return e
	}
	type item struct {
		id       int64
		baseline qualityRecovery
	}
	var updates []item
	for rows.Next() {
		var id int64
		var raw string
		if e := rows.Scan(&id, &raw); e != nil {
			rows.Close()
			return e
		}
		var b qualityRecovery
		if e := json.Unmarshal([]byte(raw), &b); e != nil {
			rows.Close()
			return e
		}
		if b.Action == "enable_bps" && b.BPSApplied == policy && b.ControlRevision == oldRevision {
			b.ControlRevision = newRevision
			updates = append(updates, item{id, b})
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, item := range updates {
		raw, e := json.Marshal(item.baseline)
		if e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, `UPDATE account_quality_recovery SET snapshot=$1 WHERE plan_id=$2`, string(raw), item.id); e != nil {
			return e
		}
	}
	return nil
}

func (db *DB) ClaimQualityBPSRecovery(ctx context.Context, now time.Time) (candidate *QualityBPSRecoveryCandidate, err error) {
	err = db.withWriteTx(ctx, func(tx *sql.Tx) error {
		enabled, e := db.PluginEnabledTx(ctx, tx, "quality-ops")
		if e != nil || !enabled {
			return e
		}
		q := `SELECT a.id,a.credential_generation,a.control_revision,p.quality_bps_disabled_at,p.quality_bps FROM accounts a JOIN account_codex_paths p ON p.account_id=a.id WHERE a.status='active' AND a.enabled=TRUE AND a.control_revision=p.quality_bps_owner_revision AND p.upstream='basispoints' AND p.allowed=0 AND p.quality_bps_disabled_at>0 AND p.quality_bps_recovery_at<=$1 ORDER BY p.quality_bps_recovery_at LIMIT 1`
		if !db.isSQLite() {
			q += ` FOR UPDATE SKIP LOCKED`
		}
		var c QualityBPSRecoveryCandidate
		var raw string
		e = tx.QueryRowContext(ctx, q, now.Unix()).Scan(&c.AccountID, &c.Generation, &c.Revision, &c.DisabledAt, &raw)
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		if e := json.Unmarshal([]byte(raw), &c.Policy); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, `UPDATE account_codex_paths SET quality_bps_recovery_at=$1 WHERE account_id=$2 AND upstream='basispoints'`, now.Add(bpsRecoveryInterval(c.Policy)).Unix(), c.AccountID); e != nil {
			return e
		}
		if c.Policy.AutoDisableOn403 && c.Policy.AutoRecoverOn403 {
			candidate = &c
		}
		return nil
	})
	return
}

func (db *DB) CompleteQualityBPSRecovery(ctx context.Context, c QualityBPSRecoveryCandidate) (changed bool, err error) {
	err = db.withWriteTx(ctx, func(tx *sql.Tx) error {
		enabled, e := db.PluginEnabledTx(ctx, tx, "quality-ops")
		if e != nil || !enabled {
			return e
		}
		rev, e := db.LockAccountControlTx(ctx, tx, c.AccountID)
		if e != nil {
			return e
		}
		if rev != c.Revision {
			return nil
		}
		var generation int64
		if e := tx.QueryRowContext(ctx, `SELECT credential_generation FROM accounts WHERE id=$1 AND status='active' AND enabled=TRUE`, c.AccountID).Scan(&generation); errors.Is(e, sql.ErrNoRows) {
			return nil
		} else if e != nil {
			return e
		}
		if generation != c.Generation {
			return nil
		}
		result, e := tx.ExecContext(ctx, `UPDATE account_codex_paths SET allowed=1,quality_bps_disabled_at=0,quality_bps_recovery_at=0 WHERE account_id=$1 AND upstream='basispoints' AND allowed=0 AND quality_bps_disabled_at=$2`, c.AccountID, c.DisabledAt)
		if e != nil {
			return e
		}
		n, e := result.RowsAffected()
		if e != nil || n == 0 {
			return e
		}
		var raw string
		if e := tx.QueryRowContext(ctx, `SELECT quality_bps FROM account_codex_paths WHERE account_id=$1 AND upstream='basispoints'`, c.AccountID).Scan(&raw); e != nil {
			return e
		}
		next, e := db.LockAccountControlTx(ctx, tx, c.AccountID)
		if e != nil {
			return e
		}
		if e := db.refreshQualityBPSOwnershipTx(ctx, tx, c.AccountID, raw, rev, next); e != nil {
			return e
		}
		changed = true
		return db.AccountControlOutboxTx(ctx, tx, c.AccountID)
	})
	return
}
