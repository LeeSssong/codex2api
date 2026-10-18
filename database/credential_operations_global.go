package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

const credentialGlobalKey = "credential_operations_global_rules"

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
	err := q.QueryRowContext(ctx, `SELECT value FROM credential_operations_settings WHERE key=$1`, credentialGlobalKey).Scan(&raw)
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
		enabled, err := db.credentialOperationsEnabledTx(ctx, tx)
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
		if _, err = tx.ExecContext(ctx, `INSERT INTO credential_operations_settings(key,value) VALUES($1,$2) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, credentialGlobalKey, string(raw)); err != nil {
			return err
		}

		if _, err = tx.ExecContext(ctx, `UPDATE credential_operations_rules SET interval_seconds=$1,failure_threshold=$2,cooldown_seconds=$3`, c.IntervalSeconds, c.FailureThreshold, c.CooldownSeconds); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE credential_operations_monitors SET lease_owner='',lease_until=NULL,next_probe_at=CURRENT_TIMESTAMP,cooldown_until=NULL,updated_at=CURRENT_TIMESTAMP`); err != nil {
			return err
		}
		return tx.Commit()
	})
}
