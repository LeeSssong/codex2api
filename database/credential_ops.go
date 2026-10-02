package database

import (
	"context"
	"database/sql"
	"encoding/json"
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
	return nil
}

type CredentialOpsLoginConfigRow struct {
	AccountID                                            int64
	LoginEmail, CredentialMode, Engine, ProxySource      string
	PasswordCiphertext, TOTPCiphertext, OTPURLCiphertext string
	UpdatedAt                                            time.Time
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
