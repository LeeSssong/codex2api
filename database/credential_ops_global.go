package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

const credentialGlobalKey = "credential_ops_global_rules"

type CredentialOpsGlobalRules struct {
	RetrySeconds     int `json:"retry_seconds"`
	IntervalSeconds  int `json:"interval_seconds"`
	FailureThreshold int `json:"failure_threshold"`
	CooldownSeconds  int `json:"cooldown_seconds"`
}

func credentialGlobalRules(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (CredentialOpsGlobalRules, error) {
	c := CredentialOpsGlobalRules{RetrySeconds: 300, IntervalSeconds: 1800, FailureThreshold: 2, CooldownSeconds: 1800}
	var raw string
	err := q.QueryRowContext(ctx, `SELECT value FROM account_ops_settings WHERE key=$1`, credentialGlobalKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	err = json.Unmarshal([]byte(raw), &c)
	return c, err
}

func (db *DB) CredentialOpsGlobalRules(ctx context.Context) (CredentialOpsGlobalRules, error) {
	return credentialGlobalRules(ctx, db.conn)
}

// All native OpenAI OAuth imports enroll, including imports outside the 2FA page.
// Existing leases and results are preserved. Plugin enablement gates execution.
func enrollCredentialMonitorsTx(ctx context.Context, tx *sql.Tx, c CredentialOpsGlobalRules) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO credential_ops_monitors(account_id,enabled,auto_relogin_enabled) SELECT id,TRUE,TRUE FROM accounts WHERE status<>'deleted' AND platform='openai' AND type='oauth' ON CONFLICT(account_id) DO NOTHING`)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO credential_ops_rules(account_id,interval_seconds,failure_threshold,cooldown_seconds) SELECT account_id,$1,$2,$3 FROM credential_ops_monitors WHERE TRUE ON CONFLICT(account_id) DO NOTHING`, c.IntervalSeconds, c.FailureThreshold, c.CooldownSeconds)
	return err
}

func (db *DB) EnrollCredentialOpsMonitors(ctx context.Context) error {
	return db.withSQLiteWriteLock(ctx, func() error {
		tx, err := db.conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		c, err := credentialGlobalRules(ctx, tx)
		if err != nil {
			return err
		}
		if err = enrollCredentialMonitorsTx(ctx, tx, c); err != nil {
			return err
		}
		return tx.Commit()
	})
}

func (db *DB) SaveCredentialOpsGlobalRules(ctx context.Context, c CredentialOpsGlobalRules) error {
	if c.RetrySeconds < 30 || c.RetrySeconds > 86400 || c.IntervalSeconds < 60 || c.IntervalSeconds > 86400 || c.FailureThreshold < 1 || c.FailureThreshold > 10 || c.CooldownSeconds < 60 || c.CooldownSeconds > 604800 {
		return errors.New("invalid monitor rules")
	}
	return db.withSQLiteWriteLock(ctx, func() error {
		tx, err := db.conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		enabled, err := db.PluginEnabledTx(ctx, tx, "credential-ops")
		if err != nil {
			return err
		}
		if !enabled {
			return ErrCredentialOpsDisabled
		}
		raw, err := json.Marshal(c)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO account_ops_settings(key,value) VALUES($1,$2) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, credentialGlobalKey, string(raw)); err != nil {
			return err
		}
		if err = enrollCredentialMonitorsTx(ctx, tx, c); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE credential_ops_rules SET interval_seconds=$1,failure_threshold=$2,cooldown_seconds=$3`, c.IntervalSeconds, c.FailureThreshold, c.CooldownSeconds); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE credential_ops_monitors SET enabled=TRUE,auto_relogin_enabled=TRUE,lease_owner='',lease_until=NULL,next_probe_at=CURRENT_TIMESTAMP,cooldown_until=NULL,updated_at=CURRENT_TIMESTAMP`); err != nil {
			return err
		}
		return tx.Commit()
	})
}
