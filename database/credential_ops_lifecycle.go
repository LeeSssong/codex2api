package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/codex2api/internal/openaiidentity"
	"github.com/google/uuid"
)

var ErrCredentialOpsStale = errors.New("credential task lease or generation changed")
var ErrCredentialOpsDisabled = errors.New("credential operations plugin is disabled")

func (db *DB) FailCredentialOpsTask(ctx context.Context, id int64, owner string, attempt int) error {
	res, err := db.conn.ExecContext(ctx, `UPDATE credential_ops_tasks SET status='failed',stage='failed',error_message='login protocol failed',lease_owner='',lease_until=NULL,finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=$1 AND lease_owner=$2 AND attempt=$3 AND status='running' AND lease_until>CURRENT_TIMESTAMP`, id, owner, attempt)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrCredentialOpsStale
	}
	return nil
}

func (db *DB) CreateCredentialOpsTaskWithConfig(ctx context.Context, accountID, generation int64, cfg CredentialOpsLoginConfigRow) (*CredentialOpsTaskRow, error) {
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
		if accountID > 0 {
			var count int
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM credential_ops_tasks WHERE account_id=$1 AND status IN ('queued','running')`, accountID).Scan(&count); err != nil {
				return err
			}
			if count > 0 {
				return errors.New("login already queued or running")
			}
			if err = upsertCredentialOpsConfigTx(ctx, tx, cfg); err != nil {
				return err
			}
		}
		task = &CredentialOpsTaskRow{AccountID: accountID, ExpectedGeneration: generation, Status: "queued", Stage: "queued"}
		if err = tx.QueryRowContext(ctx, `INSERT INTO credential_ops_tasks(account_id,status,stage,expected_generation) VALUES($1,'queued','queued',$2) RETURNING id,created_at,updated_at`, accountID, generation).Scan(&task.ID, &task.CreatedAt, &task.UpdatedAt); err != nil {
			return err
		}
		payload, err := json.Marshal(cfg)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO credential_ops_task_inputs(task_id,config_json) VALUES($1,$2)`, task.ID, string(payload)); err != nil {
			return err
		}
		return tx.Commit()
	})
	return task, err
}

func (db *DB) CredentialOpsTaskConfig(ctx context.Context, id int64) (*CredentialOpsLoginConfigRow, error) {
	var raw string
	err := db.conn.QueryRowContext(ctx, `SELECT config_json FROM credential_ops_task_inputs WHERE task_id=$1`, id).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var cfg CredentialOpsLoginConfigRow
	err = json.Unmarshal([]byte(raw), &cfg)
	return &cfg, err
}

func (db *DB) RenewCredentialOpsTask(ctx context.Context, id int64, owner string, attempt int, stage string) error {
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
		until := time.Now().UTC().Add(2 * time.Minute)
		result, err := tx.ExecContext(ctx, `UPDATE credential_ops_tasks SET lease_until=$1,stage=$2,updated_at=CURRENT_TIMESTAMP WHERE id=$3 AND lease_owner=$4 AND attempt=$5 AND status='running' AND lease_until>CURRENT_TIMESTAMP`, db.timeArg(until), stage, id, owner, attempt)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrCredentialOpsStale
		}
		return tx.Commit()
	})
}

// CommitCredentialOpsLogin fences the task and native account in one transaction.
// A cancelled, expired, reclaimed or superseded task never writes credentials.
func (db *DB) CommitCredentialOpsLogin(ctx context.Context, id int64, owner string, attempt int, credentials map[string]any, creationDefaults ...map[string]any) (int64, error) {
	var accountID int64
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
		query := `SELECT account_id,expected_generation FROM credential_ops_tasks WHERE id=$1 AND status='running' AND lease_owner=$2 AND attempt=$3 AND lease_until>CURRENT_TIMESTAMP`
		if !db.isSQLite() {
			query += ` FOR UPDATE`
		}
		var expected int64
		if err = tx.QueryRowContext(ctx, query, id, owner, attempt).Scan(&accountID, &expected); err != nil {
			return ErrCredentialOpsStale
		}
		firstImport := accountID == 0
		cfg, err := credentialOpsConfigInTx(ctx, tx, id)
		if err != nil {
			return err
		}
		email, _ := credentials["email"].(string)
		workspace, _ := credentials["workspace_id"].(string)
		if email == "" || workspace == "" || !strings.EqualFold(email, cfg.LoginEmail) {
			return errors.New("verified login identity differs from configured identity")
		}
		// The native account table remains the sole routing and identity source.
		if accountID == 0 {
			if !db.isSQLite() {
				if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, strings.ToLower(email)+"/"+workspace); err != nil {
					return err
				}
			}
			rows, e := tx.QueryContext(ctx, `SELECT id,credentials,credential_generation FROM accounts WHERE status<>'deleted' AND COALESCE(error_message,'')<>'deleted'`)
			if e != nil {
				return e
			}
			for rows.Next() {
				var candidate, generation int64
				var raw any
				if e = rows.Scan(&candidate, &raw, &generation); e != nil {
					rows.Close()
					return e
				}
				if strings.EqualFold(credentialString(raw, "email"), email) && openaiidentity.EffectiveWorkspaceID(credentialString(raw, "workspace_id"), credentialStringMap(raw, "custom_headers")) == workspace {
					accountID = candidate
					expected = generation
					break
				}
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return e
			}
		}
		family := credentialFamilyCandidate(credentials)
		if family == "" {
			family = "cf_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		}
		if accountID > 0 {
			query = `SELECT credentials,credential_generation,credential_family_id FROM accounts WHERE id=$1 AND status<>'deleted'`
			if !db.isSQLite() {
				query += ` FOR UPDATE`
			}
			var raw any
			var generation int64
			var oldFamily string
			if err = tx.QueryRowContext(ctx, query, accountID).Scan(&raw, &generation, &oldFamily); err != nil {
				return err
			}
			if generation != expected {
				return ErrCredentialOpsStale
			}
			old := decodeCredentials(raw)
			oldEmail, _ := old["email"].(string)
			oldWorkspace := credentialString(raw, "workspace_id")
			if (oldEmail != "" && !strings.EqualFold(oldEmail, email)) || (oldWorkspace != "" && oldWorkspace != workspace) {
				return errors.New("login identity differs from native account")
			}
			merged := mergeCredentialMaps(old, credentials)
			if oldFamily != "" {
				family = oldFamily
			}
			merged["credential_family_id"] = family
			encoded, e := json.Marshal(encryptSensitiveCredentials(merged))
			if e != nil {
				return e
			}
			result, e := tx.ExecContext(ctx, `UPDATE accounts SET credentials=$1,credential_family_id=$2,credential_generation=credential_generation+1,updated_at=CURRENT_TIMESTAMP WHERE id=$3 AND credential_generation=$4 AND status<>'deleted'`, encoded, family, accountID, expected)
			if e != nil {
				return e
			}
			n, _ := result.RowsAffected()
			if n != 1 {
				return ErrCredentialOpsStale
			}
			if _, err = tx.ExecContext(ctx, `UPDATE accounts SET status=CASE WHEN status='error' THEN 'active' ELSE status END,error_message=CASE WHEN status='error' OR cooldown_reason='unauthorized' THEN '' ELSE error_message END,cooldown_until=CASE WHEN cooldown_reason='unauthorized' THEN NULL ELSE cooldown_until END,cooldown_reason=CASE WHEN cooldown_reason='unauthorized' THEN '' ELSE cooldown_reason END WHERE id=$1`, accountID); err != nil {
				return err
			}
		} else {
			insertCredentials := credentials
			if len(creationDefaults) > 0 {
				insertCredentials = mergeCredentialMaps(creationDefaults[0], credentials)
			}
			encoded, e := json.Marshal(encryptSensitiveCredentials(insertCredentials))
			if e != nil {
				return e
			}
			name := strings.TrimSpace(cfg.Name)
			if name == "" {
				name = email
			}
			if err = tx.QueryRowContext(ctx, `INSERT INTO accounts(name,credentials,proxy_url,credential_family_id) VALUES($1,$2,'',$3) RETURNING id`, name, encoded, family).Scan(&accountID); err != nil {
				return err
			}
			if err = db.ApplySmartOpsOAuthDefaultsTx(ctx, tx, accountID, "openai"); err != nil {
				return err
			}
		}
		cfg.AccountID = accountID
		if firstImport {
			if err = upsertCredentialOpsConfigTx(ctx, tx, cfg); err != nil {
				return err
			}
		}
		result, err := tx.ExecContext(ctx, `UPDATE credential_ops_tasks SET account_id=$1,status='succeeded',stage='succeeded',lease_owner='',lease_until=NULL,finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=$2 AND status='running' AND lease_owner=$3 AND attempt=$4 AND lease_until>CURRENT_TIMESTAMP`, accountID, id, owner, attempt)
		if err != nil {
			return err
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			return ErrCredentialOpsStale
		}
		return tx.Commit()
	})
	return accountID, err
}

func credentialOpsConfigInTx(ctx context.Context, tx *sql.Tx, id int64) (CredentialOpsLoginConfigRow, error) {
	var cfg CredentialOpsLoginConfigRow
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT config_json FROM credential_ops_task_inputs WHERE task_id=$1`, id).Scan(&raw)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &cfg)
	}
	return cfg, err
}
func upsertCredentialOpsConfigTx(ctx context.Context, tx *sql.Tx, cfg CredentialOpsLoginConfigRow) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO credential_ops_login_configs(account_id,login_email,credential_mode,engine,proxy_source,password_ciphertext,totp_ciphertext,otp_url_ciphertext) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(account_id) DO UPDATE SET login_email=excluded.login_email,credential_mode=excluded.credential_mode,engine=excluded.engine,proxy_source=excluded.proxy_source,password_ciphertext=excluded.password_ciphertext,totp_ciphertext=excluded.totp_ciphertext,otp_url_ciphertext=excluded.otp_url_ciphertext,updated_at=CURRENT_TIMESTAMP`, cfg.AccountID, cfg.LoginEmail, cfg.CredentialMode, cfg.Engine, cfg.ProxySource, cfg.PasswordCiphertext, cfg.TOTPCiphertext, cfg.OTPURLCiphertext)
	return err
}
