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
func (db *DB) EnsureCredentialOperationsSchema(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS credential_operations_login_configs (account_id BIGINT PRIMARY KEY, login_email TEXT NOT NULL, credential_mode TEXT NOT NULL DEFAULT '', engine TEXT NOT NULL DEFAULT '', proxy_source TEXT NOT NULL DEFAULT '', proxy_id BIGINT NULL, revision BIGINT NOT NULL DEFAULT 1, password_ciphertext TEXT NOT NULL DEFAULT '', totp_ciphertext TEXT NOT NULL DEFAULT '', otp_url_ciphertext TEXT NOT NULL DEFAULT '', updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS credential_operations_tasks (id BIGSERIAL PRIMARY KEY, account_id BIGINT NOT NULL, status TEXT NOT NULL, stage TEXT NOT NULL DEFAULT 'queued', attempt INTEGER NOT NULL DEFAULT 0, expected_generation BIGINT NOT NULL DEFAULT 0, config_revision BIGINT NOT NULL DEFAULT 0, oauth_profile TEXT NOT NULL DEFAULT 'codex', kind TEXT NOT NULL DEFAULT 'login', import_key TEXT NOT NULL DEFAULT '', lease_owner TEXT NOT NULL DEFAULT '', lease_until TIMESTAMP NULL, error_message TEXT NOT NULL DEFAULT '', created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, finished_at TIMESTAMP NULL)`,
		`CREATE TABLE IF NOT EXISTS credential_operations_monitors (account_id BIGINT PRIMARY KEY, enabled BOOLEAN NOT NULL DEFAULT FALSE, auto_relogin_enabled BOOLEAN NOT NULL DEFAULT FALSE, probe_state TEXT NOT NULL DEFAULT 'pending', probe_detail TEXT NOT NULL DEFAULT '', last_probe_at TIMESTAMP NULL,last_reauth_at TIMESTAMP NULL, fail_streak INTEGER NOT NULL DEFAULT 0, next_probe_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, cooldown_until TIMESTAMP NULL, lease_owner TEXT NOT NULL DEFAULT '', lease_until TIMESTAMP NULL, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
	}
	if db.isSQLite() {
		statements[1] = `CREATE TABLE IF NOT EXISTS credential_operations_tasks (id INTEGER PRIMARY KEY AUTOINCREMENT, account_id INTEGER NOT NULL, status TEXT NOT NULL, stage TEXT NOT NULL DEFAULT 'queued', attempt INTEGER NOT NULL DEFAULT 0, expected_generation INTEGER NOT NULL DEFAULT 0, config_revision BIGINT NOT NULL DEFAULT 0, oauth_profile TEXT NOT NULL DEFAULT 'codex', kind TEXT NOT NULL DEFAULT 'login', import_key TEXT NOT NULL DEFAULT '', lease_owner TEXT NOT NULL DEFAULT '', lease_until TIMESTAMP NULL, error_message TEXT NOT NULL DEFAULT '', created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, finished_at TIMESTAMP NULL)`
	}
	statements = append(statements, "CREATE TABLE IF NOT EXISTS credential_operations_retry_lineages(source_task_id BIGINT PRIMARY KEY,child_task_id BIGINT NOT NULL)", "CREATE TABLE IF NOT EXISTS credential_operations_attempts(task_id BIGINT NOT NULL,attempt INTEGER NOT NULL,owner TEXT NOT NULL,proxy_ciphertext TEXT NOT NULL DEFAULT '',PRIMARY KEY(task_id,attempt))", "CREATE UNIQUE INDEX IF NOT EXISTS credential_operations_import_key ON credential_operations_tasks(import_key) WHERE import_key<>''", "CREATE TABLE IF NOT EXISTS credential_operations_totp_recoveries(account_id BIGINT PRIMARY KEY,task_id BIGINT NOT NULL,state TEXT NOT NULL,previous_ciphertext TEXT NOT NULL,login_email TEXT NOT NULL DEFAULT '',password_ciphertext TEXT NOT NULL DEFAULT '',candidate_ciphertext TEXT NOT NULL DEFAULT '',journal_id TEXT NOT NULL DEFAULT '',revision BIGINT NOT NULL,updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)", "CREATE TABLE IF NOT EXISTS credential_operations_settings (key TEXT PRIMARY KEY,value TEXT NOT NULL)", "CREATE UNIQUE INDEX IF NOT EXISTS credential_operations_active_account ON credential_operations_tasks(account_id) WHERE account_id>0 AND status IN ('queued','running')", "CREATE TABLE IF NOT EXISTS credential_operations_profile_grants (account_id BIGINT NOT NULL,profile TEXT NOT NULL,generation BIGINT NOT NULL DEFAULT 1,credentials_ciphertext TEXT NOT NULL,invalidated BOOLEAN NOT NULL DEFAULT FALSE,refresh_pending BOOLEAN NOT NULL DEFAULT FALSE,updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,PRIMARY KEY(account_id,profile))")
	for _, stmt := range statements {
		if _, err := db.conn.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("credential ops schema: %w", err)
		}
	}
	if _, err := db.conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS credential_operations_task_inputs (task_id BIGINT PRIMARY KEY, config_json TEXT NOT NULL)`); err != nil {
		return err
	}
	if _, err := db.conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS credential_operations_rules (account_id BIGINT PRIMARY KEY, interval_seconds INTEGER NOT NULL DEFAULT 1800, failure_threshold INTEGER NOT NULL DEFAULT 2, cooldown_seconds INTEGER NOT NULL DEFAULT 3600)`); err != nil {
		return err
	}
	if db.isSQLite() {
		return db.ensureSQLiteColumn(ctx, "credential_operations_profile_grants", "refresh_pending", "BOOLEAN NOT NULL DEFAULT FALSE")
	}
	_, err := db.conn.ExecContext(ctx, "ALTER TABLE credential_operations_profile_grants ADD COLUMN IF NOT EXISTS refresh_pending BOOLEAN NOT NULL DEFAULT FALSE")
	return err
}

type CredentialOpsLoginConfigRow struct {
	RetrySourceID                                        int64
	ImportKey                                            string
	ResolvedProxyURL                                     string `json:"-"`
	RouteResolved                                        bool
	ProxyURLCiphertext, FixedResource                    string
	RouteRevision, FixedGeneration, FixedConfigRevision  int64
	Action, CandidateCiphertext                          string
	Revision                                             int64
	ProxyID                                              int64
	GroupIDs                                             []int64
	OAuthProfile                                         string
	Automatic                                            bool `json:"automatic,omitempty"`
	Name                                                 string
	AccountID                                            int64
	LoginEmail, CredentialMode, Engine, ProxySource      string
	PasswordCiphertext, TOTPCiphertext, OTPURLCiphertext string
	UpdatedAt                                            time.Time
}

type CredentialOpsTaskRow struct {
	ID, AccountID, ExpectedGeneration, ConfigRevision int64
	OAuthProfile, Kind                                string
	Status, Stage, WorkerID, LeaseOwner, Error        string
	Attempt                                           int
	LeaseUntil, FinishedAt                            *time.Time
	CreatedAt, UpdatedAt                              time.Time
}

func (db *DB) CreateCredentialOpsTask(ctx context.Context, accountID, generation int64) (*CredentialOpsTaskRow, error) {
	q := `INSERT INTO credential_operations_tasks(account_id,status,stage,expected_generation) VALUES($1,'queued','queued',$2) RETURNING id,created_at,updated_at`
	if db.isSQLite() {
		q = `INSERT INTO credential_operations_tasks(account_id,status,stage,expected_generation) VALUES($1,'queued','queued',$2) RETURNING id,created_at,updated_at`
	}
	r := &CredentialOpsTaskRow{AccountID: accountID, ExpectedGeneration: generation, Status: "queued", Stage: "queued"}
	err := db.conn.QueryRowContext(ctx, q, accountID, generation).Scan(&r.ID, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}
func (db *DB) ClaimCredentialOpsTask(ctx context.Context, owner string, lease time.Duration) (*CredentialOpsTaskRow, error) {
	return db.ClaimCredentialOpsTaskWithCapabilities(ctx, owner, lease, []string{"login_codex_v1"})
}
func (db *DB) ClaimCredentialOpsTaskWithCapabilities(ctx context.Context, owner string, lease time.Duration, capabilities []string) (*CredentialOpsTaskRow, error) {
	var task *CredentialOpsTaskRow
	err := db.withSQLiteWriteLock(ctx, func() error {
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
		// Task rows precede recovery rows in every cancellation/callback path.
		if _, err = tx.ExecContext(ctx, "UPDATE credential_operations_tasks SET status='failed',stage='uncertain',lease_owner='',lease_until=NULL,finished_at=CURRENT_TIMESTAMP WHERE kind LIKE 'totp_%' AND status='running' AND lease_until<CURRENT_TIMESTAMP"); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE credential_operations_tasks SET status='failed',stage='stale',error_message='configuration or account changed',lease_owner='',lease_until=NULL,finished_at=CURRENT_TIMESTAMP WHERE account_id>0 AND status IN ('queued','running') AND (expected_generation<>(SELECT credential_generation FROM accounts WHERE id=account_id) OR config_revision<>COALESCE((SELECT revision FROM credential_operations_login_configs WHERE account_id=credential_operations_tasks.account_id),-1) OR NOT EXISTS(SELECT 1 FROM accounts WHERE id=credential_operations_tasks.account_id AND status<>'deleted' AND platform='openai' AND type='oauth'))"); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE credential_operations_totp_recoveries SET state='uncertain',updated_at=CURRENT_TIMESTAMP WHERE task_id IN(SELECT id FROM credential_operations_tasks WHERE kind LIKE 'totp_%' AND status IN ('failed','cancelled')) AND state NOT IN ('adopted','old_verified')"); err != nil {
			return err
		}
		modeExpr := "json_extract(config_json,'$.CredentialMode')"
		if !db.isSQLite() {
			modeExpr = "config_json::jsonb->>'CredentialMode'"
		}
		passwordMode := "id IN(SELECT task_id FROM credential_operations_task_inputs WHERE " + modeExpr + "='password_totp')"
		emailMode := "id IN(SELECT task_id FROM credential_operations_task_inputs WHERE " + modeExpr + "='email_otp_url')"
		hasCodex, hasEmail := false, false
		for _, cap := range capabilities {
			hasCodex = hasCodex || cap == "login_codex_v1"
			hasEmail = hasEmail || cap == "email_otp_v1"
		}
		allowed := "FALSE"
		for _, cap := range capabilities {
			if cap == "login_codex_v1" {
				allowed += " OR (kind='login' AND oauth_profile='codex' AND " + passwordMode + ")"
			}
			if cap == "excel_oauth_v1" {
				allowed += " OR (kind='login' AND oauth_profile='excel')"
			}
			if cap == "totp_rotation_v1" {
				allowed += " OR kind LIKE 'totp_%'"
			}
		}
		if hasCodex && hasEmail {
			allowed += " OR (kind='login' AND oauth_profile='codex' AND " + emailMode + ")"
		}
		q := "SELECT id FROM credential_operations_tasks WHERE (" + allowed + ") AND (status='queued' OR (status='running' AND lease_until<CURRENT_TIMESTAMP)) ORDER BY id LIMIT 1"

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
		if _, err = tx.ExecContext(ctx, `UPDATE credential_operations_tasks SET status='running',stage='starting',lease_owner=$1,lease_until=$2,attempt=attempt+1,updated_at=CURRENT_TIMESTAMP WHERE id=$3`, owner, db.timeArg(time.Now().UTC().Add(lease)), id); err != nil {
			return err
		}
		task = &CredentialOpsTaskRow{}
		if err = tx.QueryRowContext(ctx, `SELECT id,account_id,expected_generation,status,stage,lease_owner,attempt,created_at,updated_at,config_revision,oauth_profile,kind FROM credential_operations_tasks WHERE id=$1`, id).Scan(&task.ID, &task.AccountID, &task.ExpectedGeneration, &task.Status, &task.Stage, &task.LeaseOwner, &task.Attempt, &task.CreatedAt, &task.UpdatedAt, &task.ConfigRevision, &task.OAuthProfile, &task.Kind); err != nil {
			return err
		}
		cfg, err := credentialOpsConfigInTx(ctx, tx, task.ID)
		if err != nil {
			return err
		}
		if err = db.validateCredentialOpsRouteTx(ctx, tx, cfg); err != nil {
			if _, err = tx.ExecContext(ctx, "UPDATE credential_operations_tasks SET status='failed',stage='stale',error_message='configured egress changed',lease_owner='',lease_until=NULL,finished_at=CURRENT_TIMESTAMP WHERE id=$1", task.ID); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "UPDATE credential_operations_totp_recoveries SET state='uncertain',updated_at=CURRENT_TIMESTAMP WHERE task_id=$1 AND state NOT IN ('adopted','old_verified')", task.ID); err != nil {
				return err
			}
			task = nil
			return tx.Commit()
		}
		if cfg.FixedResource != "" {
			var state string
			if err = tx.QueryRowContext(ctx, "SELECT state FROM fixed_egress_resources WHERE id=$1", cfg.FixedResource).Scan(&state); err != nil {
				return err
			}
			if state != "bound" && state != "healthy" {
				return ErrFixedEgressUnavailable
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO fixed_egress_leases(id,resource_id,expires_at) VALUES($1,$2,$3)", credentialOpsLeaseID(task.ID, task.Attempt), cfg.FixedResource, db.timeArg(time.Now().UTC().Add(90*time.Second))); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO credential_operations_attempts(task_id,attempt,owner) VALUES($1,$2,$3)", task.ID, task.Attempt, owner); err != nil {
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
	q := `UPDATE credential_operations_tasks SET status=$3,stage=$4,error_message=$5,lease_owner='',lease_until=NULL,finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=$1 AND lease_owner=$2 AND status='running' AND lease_until>CURRENT_TIMESTAMP`
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
	err := db.conn.QueryRowContext(ctx, `SELECT id,account_id,expected_generation,status,stage,lease_owner,attempt,error_message,lease_until,finished_at,created_at,updated_at,config_revision,oauth_profile,kind FROM credential_operations_tasks WHERE id=$1`, id).Scan(&r.ID, &r.AccountID, &r.ExpectedGeneration, &r.Status, &r.Stage, &r.LeaseOwner, &r.Attempt, &r.Error, &until, &finished, &r.CreatedAt, &r.UpdatedAt, &r.ConfigRevision, &r.OAuthProfile, &r.Kind)
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
	return db.withSQLiteWriteLock(ctx, func() error {
		tx, e := db.conn.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(ctx, "UPDATE credential_operations_tasks SET status='cancelled',stage='cancelled',lease_owner='',lease_until=NULL,finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=$1 AND status IN ('queued','running')", id); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "UPDATE credential_operations_totp_recoveries SET state='uncertain',updated_at=CURRENT_TIMESTAMP WHERE task_id=$1 AND state NOT IN ('adopted','old_verified')", id); e != nil {
			return e
		}
		return tx.Commit()
	})
}

func (db *DB) UpsertCredentialOpsLoginConfig(ctx context.Context, row CredentialOpsLoginConfigRow) error {
	return db.withSQLiteWriteLock(ctx, func() error {
		tx, e := db.conn.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		if e = db.upsertCredentialOpsConfigTx(ctx, tx, row); e != nil {
			return e
		}
		return tx.Commit()
	})
}
func (db *DB) GetCredentialOpsLoginConfig(ctx context.Context, id int64) (*CredentialOpsLoginConfigRow, error) {
	var r CredentialOpsLoginConfigRow
	err := db.conn.QueryRowContext(ctx, `SELECT account_id,login_email,credential_mode,engine,proxy_source,password_ciphertext,totp_ciphertext,otp_url_ciphertext,updated_at,revision,COALESCE(proxy_id,0) FROM credential_operations_login_configs WHERE account_id=$1`, id).Scan(&r.AccountID, &r.LoginEmail, &r.CredentialMode, &r.Engine, &r.ProxySource, &r.PasswordCiphertext, &r.TOTPCiphertext, &r.OTPURLCiphertext, &r.UpdatedAt, &r.Revision, &r.ProxyID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &r, err
}

func encodeCredentialOpsPayload(v map[string]any) (string, error) {
	b, e := json.Marshal(v)
	return string(b), e
}
