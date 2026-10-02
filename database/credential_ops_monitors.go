package database

import (
	"context"
	"errors"
	"time"
)

type CredentialOpsMonitorRow struct {
	AccountID        int64      `json:"account_id"`
	Name             string     `json:"name"`
	Enabled          bool       `json:"enabled"`
	AutoRelogin      bool       `json:"auto_relogin"`
	State            string     `json:"probe_state"`
	Detail           string     `json:"probe_detail"`
	FailStreak       int        `json:"fail_streak"`
	NextProbeAt      time.Time  `json:"next_probe_at"`
	CooldownUntil    *time.Time `json:"cooldown_until"`
	IntervalSeconds  int        `json:"interval_seconds"`
	FailureThreshold int        `json:"failure_threshold"`
	CooldownSeconds  int        `json:"cooldown_seconds"`
	LeaseOwner       string     `json:"-"`
}

func (db *DB) SaveCredentialOpsMonitor(ctx context.Context, m CredentialOpsMonitorRow) error {
	if m.IntervalSeconds < 60 || m.IntervalSeconds > 86400 || m.FailureThreshold < 1 || m.FailureThreshold > 20 || m.CooldownSeconds < 60 || m.CooldownSeconds > 86400 {
		return errors.New("invalid monitor rules")
	}
	return db.withSQLiteWriteLock(ctx, func() error {
		tx, err := db.conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(ctx, `INSERT INTO credential_ops_monitors(account_id,enabled,auto_relogin_enabled) VALUES($1,$2,$3) ON CONFLICT(account_id) DO UPDATE SET enabled=excluded.enabled,auto_relogin_enabled=excluded.auto_relogin_enabled,updated_at=CURRENT_TIMESTAMP`, m.AccountID, m.Enabled, m.AutoRelogin); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO credential_ops_rules(account_id,interval_seconds,failure_threshold,cooldown_seconds) VALUES($1,$2,$3,$4) ON CONFLICT(account_id) DO UPDATE SET interval_seconds=excluded.interval_seconds,failure_threshold=excluded.failure_threshold,cooldown_seconds=excluded.cooldown_seconds`, m.AccountID, m.IntervalSeconds, m.FailureThreshold, m.CooldownSeconds); err != nil {
			return err
		}
		return tx.Commit()
	})
}
func (db *DB) ListCredentialOpsMonitors(ctx context.Context) ([]CredentialOpsMonitorRow, error) {
	rows, err := db.conn.QueryContext(ctx, `SELECT a.id,a.name,COALESCE(m.enabled,FALSE),COALESCE(m.auto_relogin_enabled,FALSE),COALESCE(m.probe_state,'pending'),COALESCE(m.probe_detail,''),COALESCE(m.fail_streak,0),COALESCE(m.next_probe_at,CURRENT_TIMESTAMP),m.cooldown_until,COALESCE(r.interval_seconds,1800),COALESCE(r.failure_threshold,2),COALESCE(r.cooldown_seconds,3600) FROM accounts a LEFT JOIN credential_ops_monitors m ON m.account_id=a.id LEFT JOIN credential_ops_rules r ON r.account_id=a.id WHERE a.status<>'deleted' AND a.platform='openai' AND a.type='oauth' ORDER BY a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CredentialOpsMonitorRow{}
	for rows.Next() {
		var m CredentialOpsMonitorRow
		var next, cooldown any
		if err = rows.Scan(&m.AccountID, &m.Name, &m.Enabled, &m.AutoRelogin, &m.State, &m.Detail, &m.FailStreak, &next, &cooldown, &m.IntervalSeconds, &m.FailureThreshold, &m.CooldownSeconds); err != nil {
			return nil, err
		}
		if m.NextProbeAt, err = parseDBTimeValue(next); err != nil {
			return nil, err
		}
		parsed, err := parseDBNullTimeValue(cooldown)
		if err != nil {
			return nil, err
		}
		if parsed.Valid {
			m.CooldownUntil = &parsed.Time
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func (db *DB) ClaimCredentialOpsMonitor(ctx context.Context, id int64, owner string) (*CredentialOpsMonitorRow, error) {
	all, err := db.ListCredentialOpsMonitors(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	for _, m := range all {
		if id > 0 {
			if m.AccountID != id {
				continue
			}
		} else if !m.Enabled || m.NextProbeAt.After(now) || (m.CooldownUntil != nil && m.CooldownUntil.After(now)) {
			continue
		}
		var n int64
		err = db.withSQLiteWriteLock(ctx, func() error {
			tx, e := db.conn.BeginTx(ctx, nil)
			if e != nil {
				return e
			}
			defer tx.Rollback()
			enabled, e := db.PluginEnabledTx(ctx, tx, "credential-ops")
			if e != nil {
				return e
			}
			if !enabled {
				return ErrCredentialOpsDisabled
			}
			result, e := tx.ExecContext(ctx, `UPDATE credential_ops_monitors SET lease_owner=$1,lease_until=$2,updated_at=CURRENT_TIMESTAMP WHERE account_id=$3 AND (lease_until IS NULL OR lease_until<CURRENT_TIMESTAMP) AND ($4 OR (enabled=TRUE AND next_probe_at<=CURRENT_TIMESTAMP AND (cooldown_until IS NULL OR cooldown_until<=CURRENT_TIMESTAMP)))`, owner, db.timeArg(now.Add(time.Minute)), m.AccountID, id > 0)
			if e != nil {
				return e
			}
			n, e = result.RowsAffected()
			if e != nil {
				return e
			}
			return tx.Commit()
		})
		if err != nil {
			return nil, err
		}
		if n == 1 {
			m.LeaseOwner = owner
			return &m, nil
		}
		if id > 0 {
			return nil, nil
		}
	}
	return nil, nil
}
func (db *DB) CompleteCredentialOpsMonitor(ctx context.Context, m CredentialOpsMonitorRow, state string) (bool, error) {
	streak := 0
	detail := "available"
	if state == "auth" {
		streak = m.FailStreak + 1
		detail = "native probe confirmed unauthorized"
	}
	if state == "transient" {
		streak = m.FailStreak
		detail = "probe temporarily unavailable"
	}
	reauth := state == "auth" && m.AutoRelogin && streak >= m.FailureThreshold
	now := time.Now().UTC()
	var cooldown any
	if reauth {
		cooldown = db.timeArg(now.Add(time.Duration(m.CooldownSeconds) * time.Second))
	}
	err := db.withSQLiteWriteLock(ctx, func() error {
		tx, e := db.conn.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		enabled, e := db.PluginEnabledTx(ctx, tx, "credential-ops")
		if e != nil {
			return e
		}
		if !enabled {
			return ErrCredentialOpsDisabled
		}
		result, e := tx.ExecContext(ctx, `UPDATE credential_ops_monitors SET probe_state=$1,probe_detail=$2,fail_streak=$3,next_probe_at=$4,cooldown_until=$5,lease_owner='',lease_until=NULL,updated_at=CURRENT_TIMESTAMP WHERE account_id=$6 AND lease_owner=$7 AND lease_until>CURRENT_TIMESTAMP`, state, detail, streak, db.timeArg(now.Add(time.Duration(m.IntervalSeconds)*time.Second)), cooldown, m.AccountID, m.LeaseOwner)
		if e != nil {
			return e
		}
		n, e := result.RowsAffected()
		if e != nil {
			return e
		}
		if n != 1 {
			return ErrCredentialOpsStale
		}
		return tx.Commit()
	})
	return reauth && err == nil, err
}
func (db *DB) ListCredentialOpsTasks(ctx context.Context) ([]CredentialOpsTaskRow, error) {
	rows, err := db.conn.QueryContext(ctx, `SELECT id FROM credential_ops_tasks ORDER BY id DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []CredentialOpsTaskRow{}
	for _, id := range ids {
		row, err := db.GetCredentialOpsTask(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, *row)
	}
	return out, nil
}
