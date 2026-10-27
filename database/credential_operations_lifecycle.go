package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/codex2api/credentialops"
	"github.com/codex2api/internal/openaiidentity"
	"github.com/google/uuid"
)

var ErrCredentialOpsStale = errors.New("credential task lease or generation changed")
var ErrCredentialOpsDisabled = errors.New("credential operations is disabled")

func (db *DB) FailCredentialOpsTask(ctx context.Context, id int64, owner string, attempt int) error {
	res, err := db.conn.ExecContext(ctx, `UPDATE credential_operations_tasks SET status='failed',stage='failed',error_message='login protocol failed',lease_owner='',lease_until=NULL,finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=$1 AND lease_owner=$2 AND attempt=$3 AND status='running' AND lease_until>CURRENT_TIMESTAMP`, id, owner, attempt)
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
	return db.createCredentialOpsTaskWithConfig(ctx, accountID, generation, cfg, false, "")
}

func (db *DB) CreateCredentialOpsAutoTaskWithConfig(ctx context.Context, accountID, generation int64, cfg CredentialOpsLoginConfigRow, probeLease string) (*CredentialOpsTaskRow, error) {
	return db.createCredentialOpsTaskWithConfig(ctx, accountID, generation, cfg, true, probeLease)
}

func (db *DB) createCredentialOpsTaskWithConfig(ctx context.Context, accountID, generation int64, cfg CredentialOpsLoginConfigRow, automatic bool, probeLease string) (*CredentialOpsTaskRow, error) {
	cfg.Automatic = automatic
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
		if cfg.RetrySourceID > 0 {
			var locked int64
			if err = tx.QueryRowContext(ctx, db.credentialOpsLockQuery("SELECT id FROM credential_operations_tasks WHERE id=$1"), cfg.RetrySourceID).Scan(&locked); err != nil {
				return err
			}
			source, e := credentialOpsTaskTx(ctx, tx, cfg.RetrySourceID)
			if e != nil {
				return e
			}
			profile := cfg.OAuthProfile
			if profile == "" {
				profile = "codex"
			}
			if source.Kind != "login" || source.OAuthProfile != profile {
				return ErrCredentialOpsStale
			}
			if credentialOpsReusableRetry(source) {
				task = source
				return tx.Commit()
			}
			var childID int64
			e = tx.QueryRowContext(ctx, "SELECT child_task_id FROM credential_operations_retry_lineages WHERE source_task_id=$1", cfg.RetrySourceID).Scan(&childID)
			if e != nil && e != sql.ErrNoRows {
				return e
			}
			if e == nil {
				child, e := credentialOpsTaskTx(ctx, tx, childID)
				if e != nil {
					return e
				}
				if credentialOpsReusableRetry(child) {
					task = child
					return tx.Commit()
				}
			}
		}
		if accountID > 0 && !db.isSQLite() {
			if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", accountID); err != nil {
				return err
			}
		}
		if accountID == 0 && cfg.ImportKey != "" {
			if !db.isSQLite() {
				if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", cfg.ImportKey); err != nil {
					return err
				}
			}
			var existing int64
			err = tx.QueryRowContext(ctx, "SELECT id FROM credential_operations_tasks WHERE import_key=$1", cfg.ImportKey).Scan(&existing)
			if err == nil {
				task, err = credentialOpsTaskTx(ctx, tx, existing)
				if err != nil {
					return err
				}
				return tx.Commit()
			}
			if err != sql.ErrNoRows {
				return err
			}
		}
		if cfg.OAuthProfile == "excel" {
			var raw any
			var account AccountRow
			err = tx.QueryRowContext(ctx, "SELECT platform,type,status,credentials FROM accounts WHERE id=$1", accountID).Scan(&account.Platform, &account.Type, &account.Status, &raw)
			if err != nil {
				return errors.New("Excel authorization requires a native account")
			}
			account.Credentials = decodeCredentials(raw)
			if !CredentialOperationsExcelEligible(&account) {
				return errors.New("Excel authorization requires an eligible paid credential owner")
			}
			if accountID == 0 || cfg.CredentialMode != "password_totp" || (cfg.Engine != "local_worker" && cfg.Engine != "inherit") {
				return errors.New("Excel authorization requires an existing account and local password login")
			}
			var failed int
			cutoff := db.timeArg(time.Now().UTC().Add(-30 * time.Minute))
			if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM credential_operations_tasks WHERE account_id=$1 AND oauth_profile='excel' AND status='failed' AND finished_at>$2", accountID, cutoff).Scan(&failed); err != nil {
				return err
			}
			if failed > 0 {
				return errors.New("Excel authorization is cooling down")
			}
		}
		if automatic {
			if probeLease == "" {
				return ErrCredentialOpsStale
			}
			q := `SELECT m.enabled,m.auto_relogin_enabled,m.probe_state,m.fail_streak,r.failure_threshold FROM credential_operations_monitors m JOIN credential_operations_rules r ON r.account_id=m.account_id WHERE m.account_id=$1 AND m.lease_owner=$2 AND m.lease_until IS NULL`
			if !db.isSQLite() {
				q += ` FOR UPDATE OF m`
			}
			var monitorEnabled, autoEnabled bool
			var state string
			var streak, threshold int
			if err = tx.QueryRowContext(ctx, q, accountID, probeLease).Scan(&monitorEnabled, &autoEnabled, &state, &streak, &threshold); err != nil {
				return ErrCredentialOpsStale
			}
			if !monitorEnabled || !autoEnabled || state != "auth" || streak < threshold {
				return ErrCredentialOpsStale
			}
			if _, err = tx.ExecContext(ctx, `UPDATE credential_operations_monitors SET lease_owner='' WHERE account_id=$1 AND lease_owner=$2`, accountID, probeLease); err != nil {
				return err
			}
		}
		if accountID > 0 {
			var blocked int
			if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM credential_operations_totp_recoveries WHERE account_id=$1 AND state NOT IN ('adopted','old_verified')", accountID).Scan(&blocked); err != nil {
				return err
			}
			if blocked > 0 {
				return errors.New("2FA recovery must be resolved before login")
			}
			var count int
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM credential_operations_tasks WHERE account_id=$1 AND status IN ('queued','running')`, accountID).Scan(&count); err != nil {
				return err
			}
			if count > 0 {
				task, err = credentialOpsActiveTaskTx(ctx, tx, accountID)
				if err != nil {
					return err
				}
				return tx.Commit()
			}
			var current CredentialOpsLoginConfigRow
			current.AccountID = accountID
			e := tx.QueryRowContext(ctx, "SELECT revision,login_email,credential_mode,engine,proxy_source,COALESCE(proxy_id,0),password_ciphertext,totp_ciphertext,otp_url_ciphertext FROM credential_operations_login_configs WHERE account_id=$1", accountID).Scan(&current.Revision, &current.LoginEmail, &current.CredentialMode, &current.Engine, &current.ProxySource, &current.ProxyID, &current.PasswordCiphertext, &current.TOTPCiphertext, &current.OTPURLCiphertext)
			if e != nil && e != sql.ErrNoRows {
				return e
			}
			if cfg.Revision > 0 && cfg.Revision != current.Revision {
				return ErrCredentialOpsStale
			}
			if cfg.OAuthProfile == "excel" {
				if e != nil {
					return errors.New("Excel authorization requires saved login configuration")
				}
				cfg.Revision = current.Revision
				cfg.Engine = "local_worker"
			} else {
				if e == sql.ErrNoRows || current.LoginEmail != cfg.LoginEmail || current.CredentialMode != cfg.CredentialMode || current.Engine != cfg.Engine || current.ProxySource != cfg.ProxySource || current.ProxyID != cfg.ProxyID || current.PasswordCiphertext != cfg.PasswordCiphertext || current.TOTPCiphertext != cfg.TOTPCiphertext || current.OTPURLCiphertext != cfg.OTPURLCiphertext {
					if err = db.upsertCredentialOpsConfigTx(ctx, tx, cfg); err != nil {
						return err
					}
					cfg.Revision = current.Revision + 1
				} else {
					cfg.Revision = current.Revision
				}

			}

		}
		if cfg.OAuthProfile == "" {
			cfg.OAuthProfile = "codex"
		}
		if cfg.OAuthProfile != "codex" && cfg.OAuthProfile != "excel" {
			return errors.New("invalid OAuth profile")
		}
		cfg.AccountID = accountID
		if err = db.freezeCredentialOpsRouteTx(ctx, tx, &cfg); err != nil {
			return err
		}
		task = &CredentialOpsTaskRow{OAuthProfile: cfg.OAuthProfile, Kind: "login", ConfigRevision: cfg.Revision, AccountID: accountID, ExpectedGeneration: generation, Status: "queued", Stage: "queued"}
		if err = tx.QueryRowContext(ctx, `INSERT INTO credential_operations_tasks(account_id,status,stage,expected_generation,config_revision,oauth_profile,import_key) VALUES($1,'queued','queued',$2,$3,$4,$5) RETURNING id,created_at,updated_at`, accountID, generation, cfg.Revision, cfg.OAuthProfile, cfg.ImportKey).Scan(&task.ID, &task.CreatedAt, &task.UpdatedAt); err != nil {
			return err
		}
		payload, err := json.Marshal(cfg)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO credential_operations_task_inputs(task_id,config_json) VALUES($1,$2)`, task.ID, string(payload)); err != nil {
			return err
		}
		if cfg.RetrySourceID > 0 {
			if _, err = tx.ExecContext(ctx, "INSERT INTO credential_operations_retry_lineages(source_task_id,child_task_id) VALUES($1,$2) ON CONFLICT(source_task_id) DO UPDATE SET child_task_id=excluded.child_task_id", cfg.RetrySourceID, task.ID); err != nil {
				return err
			}
		}
		return tx.Commit()
	})
	return task, err
}

func (db *DB) CredentialOpsTaskConfig(ctx context.Context, id int64) (*CredentialOpsLoginConfigRow, error) {
	var raw string
	err := db.conn.QueryRowContext(ctx, `SELECT config_json FROM credential_operations_task_inputs WHERE task_id=$1`, id).Scan(&raw)
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
		enabled, err := db.credentialOperationsEnabledTx(ctx, tx)
		if err != nil {
			return err
		}
		if !enabled {
			return ErrCredentialOpsDisabled
		}
		cfg, e := credentialOpsConfigInTx(ctx, tx, id)
		if e != nil {
			return e
		}
		if cfg.AccountID > 0 {
			if _, _, e = db.credentialOpsFenceTx(ctx, tx, id, owner, attempt); e != nil {
				return e
			}
		}
		if cfg.FixedResource != "" {
			if _, e = tx.ExecContext(ctx, "UPDATE fixed_egress_leases SET expires_at=$1 WHERE id=$2", db.timeArg(time.Now().UTC().Add(90*time.Second)), credentialOpsLeaseID(id, attempt)); e != nil {
				return e
			}
		}
		until := time.Now().UTC().Add(2 * time.Minute)
		result, err := tx.ExecContext(ctx, `UPDATE credential_operations_tasks SET lease_until=$1,stage=$2,updated_at=CURRENT_TIMESTAMP WHERE id=$3 AND lease_owner=$4 AND attempt=$5 AND status='running' AND lease_until>CURRENT_TIMESTAMP`, db.timeArg(until), stage, id, owner, attempt)
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
		enabled, err := db.credentialOperationsEnabledTx(ctx, tx)
		if err != nil {
			return err
		}
		if !enabled {
			return ErrCredentialOpsDisabled
		}
		query := `SELECT account_id,expected_generation,config_revision,oauth_profile FROM credential_operations_tasks WHERE id=$1 AND status='running' AND lease_owner=$2 AND attempt=$3 AND lease_until>CURRENT_TIMESTAMP`
		if !db.isSQLite() {
			query += ` FOR UPDATE`
		}
		var expected, configRevision int64
		var profile string
		if err = tx.QueryRowContext(ctx, query, id, owner, attempt).Scan(&accountID, &expected, &configRevision, &profile); err != nil {
			return ErrCredentialOpsStale
		}
		firstImport := accountID == 0
		if !firstImport {
			var current int64
			if err = tx.QueryRowContext(ctx, db.credentialOpsLockQuery("SELECT revision FROM credential_operations_login_configs WHERE account_id=$1"), accountID).Scan(&current); err != nil || current != configRevision {
				return ErrCredentialOpsStale
			}
		}
		cfg, err := credentialOpsConfigInTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if err = db.validateCredentialOpsRouteTx(ctx, tx, cfg); err != nil {
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
			rows, e := tx.QueryContext(ctx, `SELECT id,credentials,credential_generation FROM accounts WHERE platform='openai' AND type='oauth' AND status<>'deleted' AND COALESCE(error_message,'')<>'deleted'`)
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
		if accountID > 0 {
			var pending int
			if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM credential_operations_totp_recoveries WHERE account_id=$1 AND state NOT IN ('adopted','old_verified')", accountID).Scan(&pending); err != nil {
				return err
			}
			if pending > 0 {
				return errors.New("2FA recovery must be resolved before credential commit")
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
			oldUser := credentialString(raw, "user_id")
			if oldUser == "" {
				oldUser = credentialString(raw, "chatgpt_user_id")
			}
			newUser, _ := credentials["user_id"].(string)
			if newUser == "" {
				newUser, _ = credentials["chatgpt_user_id"].(string)
			}
			if oldUser != "" && newUser != oldUser {
				return errors.New("login user differs from native account")
			}

			if (oldEmail != "" && !strings.EqualFold(oldEmail, email)) || (oldWorkspace != "" && oldWorkspace != workspace) {
				return errors.New("login identity differs from native account")
			}
			if profile == "excel" {
				if err = db.putCredentialOpsGrantTx(ctx, tx, accountID, "excel", credentials); err != nil {
					return err
				}
			}
			if profile != "excel" {
				merged := mergeCredentialMaps(old, credentials)
				if profile == "excel" {
					merged = old
				}
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
			proxyURL := ""
			if cfg.ProxySource == "proxy" {
				_, dec := credentialops.EnvCrypto()
				proxyURL, err = dec(cfg.ProxyURLCiphertext)
				if err != nil {
					return err
				}
			}
			if err = tx.QueryRowContext(ctx, "INSERT INTO accounts(name,credentials,proxy_url,credential_family_id) VALUES($1,$2,$3,$4) RETURNING id", name, encoded, proxyURL, family).Scan(&accountID); err != nil {
				return err
			}
		}
		cfg.AccountID = accountID
		if firstImport {
			cfg.Revision = 0
			if err = db.upsertCredentialOpsConfigTx(ctx, tx, cfg); err != nil {
				return err
			}
			for _, group := range cfg.GroupIDs {
				if _, err = tx.ExecContext(ctx, "INSERT INTO account_group_members(account_id,group_id) VALUES($1,$2) ON CONFLICT DO NOTHING", accountID, group); err != nil {
					return err
				}
			}
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO credential_operations_monitors(account_id,enabled,auto_relogin_enabled,last_reauth_at) VALUES($1,TRUE,TRUE,CURRENT_TIMESTAMP) ON CONFLICT(account_id) DO UPDATE SET fail_streak=0,probe_state='ok',last_reauth_at=CURRENT_TIMESTAMP", accountID); err != nil {
			return err
		}
		rules, e := credentialGlobalRules(ctx, tx)
		if e != nil {
			return e
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO credential_operations_rules(account_id,interval_seconds,failure_threshold,cooldown_seconds) VALUES($1,$2,$3,$4) ON CONFLICT(account_id) DO NOTHING", accountID, rules.IntervalSeconds, rules.FailureThreshold, rules.CooldownSeconds); err != nil {
			return err
		}

		result, err := tx.ExecContext(ctx, `UPDATE credential_operations_tasks SET account_id=$1,expected_generation=$5,status='succeeded',stage='succeeded',lease_owner='',lease_until=NULL,finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=$2 AND status='running' AND lease_owner=$3 AND attempt=$4 AND lease_until>CURRENT_TIMESTAMP`, accountID, id, owner, attempt, expected)
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
	err := tx.QueryRowContext(ctx, `SELECT config_json FROM credential_operations_task_inputs WHERE task_id=$1`, id).Scan(&raw)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &cfg)
	}
	return cfg, err
}
func (db *DB) upsertCredentialOpsConfigTx(ctx context.Context, tx *sql.Tx, cfg CredentialOpsLoginConfigRow) error {

	if !db.isSQLite() {
		if _, e := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", cfg.AccountID); e != nil {
			return e
		}
	}
	var current int64
	err := tx.QueryRowContext(ctx, db.credentialOpsLockQuery("SELECT revision FROM credential_operations_login_configs WHERE account_id=$1"), cfg.AccountID).Scan(&current)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if cfg.Revision > 0 && current != cfg.Revision {
		return ErrCredentialOpsStale
	}
	var blocked int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM credential_operations_totp_recoveries WHERE account_id=$1 AND state NOT IN ('adopted','old_verified')", cfg.AccountID).Scan(&blocked); err != nil {
		return err
	}
	if blocked > 0 {
		return errors.New("2FA recovery must be resolved before editing login configuration")
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO credential_operations_login_configs(account_id,login_email,credential_mode,engine,proxy_source,proxy_id,password_ciphertext,totp_ciphertext,otp_url_ciphertext) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(account_id) DO UPDATE SET login_email=excluded.login_email,credential_mode=excluded.credential_mode,engine=excluded.engine,proxy_source=excluded.proxy_source,proxy_id=excluded.proxy_id,password_ciphertext=excluded.password_ciphertext,totp_ciphertext=excluded.totp_ciphertext,otp_url_ciphertext=excluded.otp_url_ciphertext,revision=credential_operations_login_configs.revision+1,updated_at=CURRENT_TIMESTAMP", cfg.AccountID, cfg.LoginEmail, cfg.CredentialMode, cfg.Engine, cfg.ProxySource, cfg.ProxyID, cfg.PasswordCiphertext, cfg.TOTPCiphertext, cfg.OTPURLCiphertext)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE credential_operations_monitors SET lease_owner='',lease_until=NULL WHERE account_id=$1", cfg.AccountID)
	return err
}
func credentialOpsActiveTaskTx(ctx context.Context, tx *sql.Tx, id int64) (*CredentialOpsTaskRow, error) {
	r := &CredentialOpsTaskRow{}
	err := tx.QueryRowContext(ctx, "SELECT id,account_id,status,stage,attempt,created_at,updated_at,oauth_profile,kind FROM credential_operations_tasks WHERE account_id=$1 AND status IN ('queued','running') ORDER BY id LIMIT 1", id).Scan(&r.ID, &r.AccountID, &r.Status, &r.Stage, &r.Attempt, &r.CreatedAt, &r.UpdatedAt, &r.OAuthProfile, &r.Kind)
	return r, err
}
func (db *DB) putCredentialOpsGrantTx(ctx context.Context, tx *sql.Tx, id int64, profile string, credentials map[string]any) error {
	raw, e := json.Marshal(credentials)
	if e != nil {
		return e
	}
	enc, _ := credentialops.EnvCrypto()
	cipher, e := enc(string(raw))
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, "INSERT INTO credential_operations_profile_grants(account_id,profile,credentials_ciphertext) VALUES($1,$2,$3) ON CONFLICT(account_id,profile) DO UPDATE SET credentials_ciphertext=excluded.credentials_ciphertext,generation=credential_operations_profile_grants.generation+1,invalidated=FALSE,refresh_pending=FALSE,updated_at=CURRENT_TIMESTAMP", id, profile, cipher)
	return e
}

func credentialOpsTaskTx(ctx context.Context, tx *sql.Tx, id int64) (*CredentialOpsTaskRow, error) {
	r := &CredentialOpsTaskRow{}
	e := tx.QueryRowContext(ctx, "SELECT id,account_id,status,stage,attempt,created_at,updated_at,oauth_profile,kind FROM credential_operations_tasks WHERE id=$1", id).Scan(&r.ID, &r.AccountID, &r.Status, &r.Stage, &r.Attempt, &r.CreatedAt, &r.UpdatedAt, &r.OAuthProfile, &r.Kind)
	return r, e
}

func (db *DB) credentialOpsLockQuery(query string) string {
	if !db.isSQLite() {
		return query + " FOR UPDATE"
	}
	return query
}
