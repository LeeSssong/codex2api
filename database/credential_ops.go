package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// EnsureCredentialOpsSchema is intentionally explicit: the root bootstrap may call it
// after normal migrations, and the independent worker may call it against its own DB.
func (db *DB) EnsureCredentialOpsSchema(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS credential_ops_login_configs (account_id BIGINT PRIMARY KEY, login_email TEXT NOT NULL, credential_mode TEXT NOT NULL DEFAULT '', engine TEXT NOT NULL DEFAULT '', proxy_source TEXT NOT NULL DEFAULT '', proxy_id BIGINT NULL, password_ciphertext TEXT NOT NULL DEFAULT '', totp_ciphertext TEXT NOT NULL DEFAULT '', otp_url_ciphertext TEXT NOT NULL DEFAULT '', updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS credential_ops_tasks (id BIGSERIAL PRIMARY KEY, account_id BIGINT NOT NULL, status TEXT NOT NULL, stage TEXT NOT NULL DEFAULT 'queued', attempt INTEGER NOT NULL DEFAULT 0, expected_generation BIGINT NOT NULL DEFAULT 0, lease_owner TEXT NOT NULL DEFAULT '', lease_until TIMESTAMP NULL, error_message TEXT NOT NULL DEFAULT '', created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, finished_at TIMESTAMP NULL)`,
		`CREATE TABLE IF NOT EXISTS credential_ops_monitors (account_id BIGINT PRIMARY KEY, enabled BOOLEAN NOT NULL DEFAULT FALSE, auto_relogin_enabled BOOLEAN NOT NULL DEFAULT FALSE, probe_state TEXT NOT NULL DEFAULT 'pending', probe_detail TEXT NOT NULL DEFAULT '', fail_streak INTEGER NOT NULL DEFAULT 0, next_probe_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, cooldown_until TIMESTAMP NULL, lease_owner TEXT NOT NULL DEFAULT '', lease_until TIMESTAMP NULL, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
	}
	if db.isSQLite() {
		statements[1] = `CREATE TABLE IF NOT EXISTS credential_ops_tasks (id INTEGER PRIMARY KEY AUTOINCREMENT, account_id INTEGER NOT NULL, status TEXT NOT NULL, stage TEXT NOT NULL DEFAULT 'queued', attempt INTEGER NOT NULL DEFAULT 0, expected_generation INTEGER NOT NULL DEFAULT 0, lease_owner TEXT NOT NULL DEFAULT '', lease_until TIMESTAMP NULL, error_message TEXT NOT NULL DEFAULT '', created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, finished_at TIMESTAMP NULL)`
	}
	for _, stmt := range statements {
		if _, err := db.conn.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("credential ops schema: %w", err)
		}
	}
	if _, err := db.conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS credential_ops_task_inputs (task_id BIGINT PRIMARY KEY, config_json TEXT NOT NULL)`); err != nil {
		return err
	}
	if _, err := db.conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS credential_ops_rules (account_id BIGINT PRIMARY KEY, interval_seconds INTEGER NOT NULL DEFAULT 1800, failure_threshold INTEGER NOT NULL DEFAULT 2, cooldown_seconds INTEGER NOT NULL DEFAULT 3600)`); err != nil {
		return err
	}
	return nil
}

type CredentialOpsLoginConfigRow struct {
	Name                                                 string
	AccountID                                            int64
	LoginEmail, CredentialMode, Engine, ProxySource      string
	PasswordCiphertext, TOTPCiphertext, OTPURLCiphertext string
	UpdatedAt                                            time.Time
}

type CredentialOpsTaskRow struct {
	ID, AccountID, ExpectedGeneration          int64
	Status, Stage, WorkerID, LeaseOwner, Error string
	Attempt                                    int
	LeaseUntil, FinishedAt                     *time.Time
	CreatedAt, UpdatedAt                       time.Time
}

func (db *DB) CreateCredentialOpsTask(ctx context.Context, accountID, generation int64) (*CredentialOpsTaskRow, error) {
	q := `INSERT INTO credential_ops_tasks(account_id,status,stage,expected_generation) VALUES($1,'queued','queued',$2) RETURNING id,created_at,updated_at`
	if db.isSQLite() {
		q = `INSERT INTO credential_ops_tasks(account_id,status,stage,expected_generation) VALUES($1,'queued','queued',$2) RETURNING id,created_at,updated_at`
	}
	r := &CredentialOpsTaskRow{AccountID: accountID, ExpectedGeneration: generation, Status: "queued", Stage: "queued"}
	err := db.conn.QueryRowContext(ctx, q, accountID, generation).Scan(&r.ID, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}
func (db *DB) ClaimCredentialOpsTask(ctx context.Context, owner string, lease time.Duration) (*CredentialOpsTaskRow, error) {
	var task *CredentialOpsTaskRow
	err := db.withSQLiteWriteLock(ctx, func() error {
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
		q := `SELECT id FROM credential_ops_tasks WHERE status='queued' OR (status='running' AND lease_until<CURRENT_TIMESTAMP) ORDER BY id LIMIT 1`
		if !db.isSQLite() {
			q += ` FOR UPDATE SKIP LOCKED`
		}
		var id int64
		if err = tx.QueryRowContext(ctx, q).Scan(&id); err == sql.ErrNoRows {
			return tx.Commit()
		}
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE credential_ops_tasks SET status='running',stage='starting',lease_owner=$1,lease_until=$2,attempt=attempt+1,updated_at=CURRENT_TIMESTAMP WHERE id=$3`, owner, db.timeArg(time.Now().UTC().Add(lease)), id); err != nil {
			return err
		}
		task = &CredentialOpsTaskRow{}
		if err = tx.QueryRowContext(ctx, `SELECT id,account_id,expected_generation,status,stage,lease_owner,attempt,created_at,updated_at FROM credential_ops_tasks WHERE id=$1`, id).Scan(&task.ID, &task.AccountID, &task.ExpectedGeneration, &task.Status, &task.Stage, &task.LeaseOwner, &task.Attempt, &task.CreatedAt, &task.UpdatedAt); err != nil {
			return err
		}
		return tx.Commit()
	})
	return task, err
}
func (db *DB) CompleteCredentialOpsTask(ctx context.Context, id int64, owner, stage, status, reason string) error {
	if status != "failed" && status != "succeeded" {
		return errors.New("invalid terminal status")
	}
	q := `UPDATE credential_ops_tasks SET status=$3,stage=$4,error_message=$5,lease_owner='',lease_until=NULL,finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=$1 AND lease_owner=$2 AND status='running' AND lease_until>CURRENT_TIMESTAMP`
	res, err := db.conn.ExecContext(ctx, q, id, owner, status, stage, reason)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
func (db *DB) GetCredentialOpsTask(ctx context.Context, id int64) (*CredentialOpsTaskRow, error) {
	r := &CredentialOpsTaskRow{}
	var until, finished sql.NullTime
	err := db.conn.QueryRowContext(ctx, `SELECT id,account_id,expected_generation,status,stage,lease_owner,attempt,error_message,lease_until,finished_at,created_at,updated_at FROM credential_ops_tasks WHERE id=$1`, id).Scan(&r.ID, &r.AccountID, &r.ExpectedGeneration, &r.Status, &r.Stage, &r.LeaseOwner, &r.Attempt, &r.Error, &until, &finished, &r.CreatedAt, &r.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if until.Valid {
		r.LeaseUntil = &until.Time
	}
	if finished.Valid {
		r.FinishedAt = &finished.Time
	}
	return r, err
}
func (db *DB) CancelCredentialOpsTask(ctx context.Context, id int64) error {
	_, err := db.conn.ExecContext(ctx, `UPDATE credential_ops_tasks SET status='cancelled',stage='cancelled',lease_owner='',lease_until=NULL,finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=$1 AND status IN ('queued','running')`, id)
	return err
}

func (db *DB) UpsertCredentialOpsLoginConfig(ctx context.Context, row CredentialOpsLoginConfigRow) error {
	q := `INSERT INTO credential_ops_login_configs(account_id,login_email,credential_mode,engine,proxy_source,password_ciphertext,totp_ciphertext,otp_url_ciphertext,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,CURRENT_TIMESTAMP) ON CONFLICT(account_id) DO UPDATE SET login_email=excluded.login_email,credential_mode=excluded.credential_mode,engine=excluded.engine,proxy_source=excluded.proxy_source,password_ciphertext=excluded.password_ciphertext,totp_ciphertext=excluded.totp_ciphertext,otp_url_ciphertext=excluded.otp_url_ciphertext,updated_at=CURRENT_TIMESTAMP`
	if db.isSQLite() {
		q = `INSERT INTO credential_ops_login_configs(account_id,login_email,credential_mode,engine,proxy_source,password_ciphertext,totp_ciphertext,otp_url_ciphertext,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,CURRENT_TIMESTAMP) ON CONFLICT(account_id) DO UPDATE SET login_email=excluded.login_email,credential_mode=excluded.credential_mode,engine=excluded.engine,proxy_source=excluded.proxy_source,password_ciphertext=excluded.password_ciphertext,totp_ciphertext=excluded.totp_ciphertext,otp_url_ciphertext=excluded.otp_url_ciphertext,updated_at=CURRENT_TIMESTAMP`
	}
	_, err := db.conn.ExecContext(ctx, q, row.AccountID, row.LoginEmail, row.CredentialMode, row.Engine, row.ProxySource, row.PasswordCiphertext, row.TOTPCiphertext, row.OTPURLCiphertext)
	return err
}
func (db *DB) GetCredentialOpsLoginConfig(ctx context.Context, id int64) (*CredentialOpsLoginConfigRow, error) {
	var r CredentialOpsLoginConfigRow
	err := db.conn.QueryRowContext(ctx, `SELECT account_id,login_email,credential_mode,engine,proxy_source,password_ciphertext,totp_ciphertext,otp_url_ciphertext,updated_at FROM credential_ops_login_configs WHERE account_id=$1`, id).Scan(&r.AccountID, &r.LoginEmail, &r.CredentialMode, &r.Engine, &r.ProxySource, &r.PasswordCiphertext, &r.TOTPCiphertext, &r.OTPURLCiphertext, &r.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &r, err
}

func encodeCredentialOpsPayload(v map[string]any) (string, error) {
	b, e := json.Marshal(v)
	return string(b), e
}
