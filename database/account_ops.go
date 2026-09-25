// Native persistence adapter for Sub2API 2.8.11 account operations (LGPL-3.0).
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

func (db *DB) ensureAccountOpsSchema(ctx context.Context) error {
	id, ts := "BIGSERIAL PRIMARY KEY", "TIMESTAMPTZ"
	if db.isSQLite() {
		id, ts = "INTEGER PRIMARY KEY AUTOINCREMENT", "TIMESTAMP"
	}
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS account_ops_settings(key TEXT PRIMARY KEY,value TEXT NOT NULL)`,
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS account_quality_plans(id %s,account_id BIGINT NOT NULL UNIQUE REFERENCES accounts(id) ON DELETE CASCADE,config TEXT NOT NULL,enabled BOOLEAN NOT NULL,version BIGINT NOT NULL DEFAULT 1,next_run %s NOT NULL,lease TEXT NOT NULL DEFAULT '',lease_until %s)`, id, ts, ts),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS account_quality_rounds(id %s,plan_id BIGINT NOT NULL REFERENCES account_quality_plans(id) ON DELETE CASCADE,account_id BIGINT NOT NULL,created_at %s NOT NULL,record TEXT NOT NULL)`, id, ts),
		`CREATE INDEX IF NOT EXISTS idx_account_quality_rounds ON account_quality_rounds(plan_id,id DESC)`,
		`CREATE TABLE IF NOT EXISTS account_quality_recovery(plan_id BIGINT PRIMARY KEY REFERENCES account_quality_plans(id) ON DELETE CASCADE,snapshot TEXT NOT NULL)`,
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS account_ops_alerts(account_id BIGINT NOT NULL,kind TEXT NOT NULL,account_name TEXT NOT NULL,signal TEXT NOT NULL,http_status INTEGER NOT NULL,first_seen %s NOT NULL,last_seen %s NOT NULL,occurrences BIGINT NOT NULL DEFAULT 1,state TEXT NOT NULL DEFAULT 'pending',last_sent_at %s,next_send_at %s NOT NULL,attempts INTEGER NOT NULL DEFAULT 0,lease TEXT NOT NULL DEFAULT '',lease_until %s,PRIMARY KEY(account_id,kind))`, ts, ts, ts, ts, ts),
	} {
		if _, e := db.conn.ExecContext(ctx, q); e != nil {
			return e
		}
	}
	return nil
}

type AccountOpsSettings struct{ db *DB }

func NewAccountOpsSettings(db *DB) *AccountOpsSettings { return &AccountOpsSettings{db} }
func (s *AccountOpsSettings) GetValue(ctx context.Context, k string) (string, error) {
	var v string
	e := s.db.conn.QueryRowContext(ctx, `SELECT value FROM account_ops_settings WHERE key=$1`, k).Scan(&v)
	if errors.Is(e, sql.ErrNoRows) {
		return "", accountops.ErrSettingNotFound
	}
	return v, e
}
func (s *AccountOpsSettings) Set(ctx context.Context, k, v string) error {
	return s.db.withSQLiteWriteLock(ctx, func() error {
		_, e := s.db.conn.ExecContext(ctx, `INSERT INTO account_ops_settings(key,value) VALUES($1,$2) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value`, k, v)
		return e
	})
}

type AccountOpsRepository struct{ db *DB }

func NewAccountOpsRepository(db *DB) *AccountOpsRepository { return &AccountOpsRepository{db} }
func (r *AccountOpsRepository) Record(ctx context.Context, e accountops.AccountOpsEvent) error {
	return r.db.withSQLiteWriteLock(ctx, func() error {
		// Native runtime accounts carry email, not the administrator display name.
		// Resolve that snapshot off the proxy hot path, in the queue consumer.
		var name string
		nameErr := r.db.conn.QueryRowContext(ctx, `SELECT name FROM accounts WHERE id=$1`, e.AccountID).Scan(&name)
		if nameErr != nil && !errors.Is(nameErr, sql.ErrNoRows) {
			return nameErr
		}
		if name != "" {
			runes := []rune(name)
			if len(runes) > 120 {
				runes = runes[:120]
			}
			e.AccountName = string(runes)
		}
		_, err := r.db.conn.ExecContext(ctx, `INSERT INTO account_ops_alerts(account_id,kind,account_name,signal,http_status,first_seen,last_seen,next_send_at) VALUES($1,$2,$3,$4,$5,$6,$6,$6) ON CONFLICT(account_id,kind) DO UPDATE SET account_name=EXCLUDED.account_name,signal=EXCLUDED.signal,http_status=EXCLUDED.http_status,last_seen=EXCLUDED.last_seen,occurrences=account_ops_alerts.occurrences+1,state=CASE WHEN account_ops_alerts.state IN ('sent','suppressed','failed') AND account_ops_alerts.next_send_at<=EXCLUDED.last_seen AND (account_ops_alerts.state<>'failed' OR account_ops_alerts.attempts>=3) THEN 'pending' ELSE account_ops_alerts.state END,attempts=CASE WHEN account_ops_alerts.state IN ('sent','suppressed','failed') AND account_ops_alerts.next_send_at<=EXCLUDED.last_seen AND (account_ops_alerts.state<>'failed' OR account_ops_alerts.attempts>=3) THEN 0 ELSE account_ops_alerts.attempts END`, e.AccountID, e.Kind, e.AccountName, e.Signal, e.HTTPStatus, r.db.timeArg(time.Now().UTC()))
		return err
	})
}

const accountOpsColumns = `account_id,kind,account_name,signal,http_status,first_seen,last_seen,occurrences,state,last_sent_at,next_send_at,attempts,lease`

func scanAccountOps(row interface{ Scan(...any) error }) (*accountops.AccountOpsEvent, error) {
	var e accountops.AccountOpsEvent
	var first, last, sent, next any
	err := row.Scan(&e.AccountID, &e.Kind, &e.AccountName, &e.Signal, &e.HTTPStatus, &first, &last, &e.Occurrences, &e.State, &sent, &next, &e.Attempts, &e.Lease)
	if err != nil {
		return nil, err
	}
	if e.FirstSeen, err = parseDBTimeValue(first); err != nil {
		return nil, err
	}
	if e.LastSeen, err = parseDBTimeValue(last); err != nil {
		return nil, err
	}
	if e.NextSendAt, err = parseDBTimeValue(next); err != nil {
		return nil, err
	}
	st, err := parseDBNullTimeValue(sent)
	if st.Valid {
		e.LastSentAt = &st.Time
	}
	return &e, err
}
func (r *AccountOpsRepository) Claim(ctx context.Context) (event *accountops.AccountOpsEvent, err error) {
	err = r.db.withWriteTx(ctx, func(tx *sql.Tx) error {
		now := time.Now().UTC()
		q := `SELECT ` + accountOpsColumns + ` FROM account_ops_alerts WHERE ((state IN ('pending','failed') AND attempts<3 AND next_send_at<=$1) OR (state='sending' AND lease_until<$1)) ORDER BY next_send_at LIMIT 1`
		if !r.db.isSQLite() {
			q += ` FOR UPDATE SKIP LOCKED`
		}
		event, err = scanAccountOps(tx.QueryRowContext(ctx, q, r.db.timeArg(now)))
		if errors.Is(err, sql.ErrNoRows) {
			event = nil
			return nil
		}
		if err != nil {
			return err
		}
		event.Lease = uuid.NewString()
		event.Attempts++
		event.State = "sending"
		_, err = tx.ExecContext(ctx, `UPDATE account_ops_alerts SET state='sending',attempts=attempts+1,lease=$1,lease_until=$2 WHERE account_id=$3 AND kind=$4`, event.Lease, r.db.timeArg(now.Add(2*time.Minute)), event.AccountID, event.Kind)
		if err != nil {
			return err
		}
		return nil
	})
	return
}
func (r *AccountOpsRepository) Complete(ctx context.Context, e *accountops.AccountOpsEvent, state string, delay time.Duration) error {
	return r.db.withSQLiteWriteLock(ctx, func() error {
		now := time.Now().UTC()
		_, err := r.db.conn.ExecContext(ctx, `UPDATE account_ops_alerts SET state=$4,lease='',lease_until=NULL,next_send_at=$5,last_sent_at=CASE WHEN $4='sent' THEN $6 ELSE last_sent_at END WHERE account_id=$1 AND kind=$2 AND lease=$3`, e.AccountID, e.Kind, e.Lease, state, r.db.timeArg(now.Add(delay)), r.db.timeArg(now))
		return err
	})
}
func (r *AccountOpsRepository) SuppressDisabled(ctx context.Context, c accountops.AccountOpsConfig) error {
	return r.db.withSQLiteWriteLock(ctx, func() error {
		_, err := r.db.conn.ExecContext(ctx, `UPDATE account_ops_alerts SET state='suppressed',lease='',lease_until=NULL WHERE state IN ('pending','failed') AND (NOT $1 OR (kind='balance_low' AND NOT $2) OR (kind='weekly_quota' AND NOT $3))`, c.Enabled, c.BalanceLow, c.WeeklyQuota)
		return err
	})
}
func (r *AccountOpsRepository) List(ctx context.Context, offset, limit int) ([]accountops.AccountOpsEvent, error) {
	if limit < 1 || limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := r.db.conn.QueryContext(ctx, `SELECT `+accountOpsColumns+` FROM account_ops_alerts ORDER BY last_seen DESC,account_id,kind LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []accountops.AccountOpsEvent{}
	for rows.Next() {
		e, err := scanAccountOps(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

func (db *DB) GetAccountOpsSMTP(ctx context.Context) (accountops.SMTPConfig, error) {
	var c accountops.SMTPConfig
	c.Port = 587
	c.TLSMode = "starttls"
	raw, e := NewAccountOpsSettings(db).GetValue(ctx, "smtp")
	if errors.Is(e, accountops.ErrSettingNotFound) {
		return c, nil
	}
	if e != nil {
		return c, e
	}
	e = json.Unmarshal([]byte(raw), &c)
	c.Password = decryptCredentialValue("account_ops_smtp", c.Password)
	c.PasswordConfigured = c.Password != ""
	return c, e
}
func (db *DB) SaveAccountOpsSMTP(ctx context.Context, c accountops.SMTPConfig) error {
	if e := c.Validate(); e != nil {
		return e
	}
	if c.Password == "" {
		old, e := db.GetAccountOpsSMTP(ctx)
		if e != nil {
			return e
		}
		c.Password = old.Password
	}
	c.Password = encryptCredentialValue("account_ops_smtp", c.Password)
	raw, e := json.Marshal(c)
	if e != nil {
		return e
	}
	return NewAccountOpsSettings(db).Set(ctx, "smtp", string(raw))
}
