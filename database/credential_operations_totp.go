package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/codex2api/credentialops"
	"time"
)

type CredentialOpsTOTPRecovery struct {
	AccountID, TaskID, Revision                                                               int64
	State, PreviousCiphertext, CandidateCiphertext, JournalID, LoginEmail, PasswordCiphertext string
	UpdatedAt                                                                                 time.Time
}

func (db *DB) GetCredentialOpsTOTPRecovery(ctx context.Context, id int64) (*CredentialOpsTOTPRecovery, error) {
	r := &CredentialOpsTOTPRecovery{AccountID: id}
	e := db.conn.QueryRowContext(ctx, "SELECT task_id,state,previous_ciphertext,candidate_ciphertext,journal_id,revision,updated_at,login_email,password_ciphertext FROM credential_operations_totp_recoveries WHERE account_id=$1", id).Scan(&r.TaskID, &r.State, &r.PreviousCiphertext, &r.CandidateCiphertext, &r.JournalID, &r.Revision, &r.UpdatedAt, &r.LoginEmail, &r.PasswordCiphertext)
	if e == sql.ErrNoRows {
		return nil, nil
	}
	return r, e
}
func (db *DB) CreateCredentialOpsTOTPTask(ctx context.Context, id int64, action string, resolvedRoute ...string) (*CredentialOpsTaskRow, error) {
	if action != "rotate" && action != "verify_new" && action != "verify_old" {
		return nil, errors.New("invalid recovery action")
	}
	var result *CredentialOpsTaskRow
	e := db.withSQLiteWriteLock(ctx, func() error {
		tx, e := db.conn.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		var cfg CredentialOpsLoginConfigRow
		cfg.AccountID = id
		if len(resolvedRoute) > 0 {
			cfg.ResolvedProxyURL = resolvedRoute[0]
			cfg.RouteResolved = true
		}
		if e = tx.QueryRowContext(ctx, "SELECT login_email,credential_mode,engine,proxy_source,COALESCE(proxy_id,0),password_ciphertext,totp_ciphertext,otp_url_ciphertext,revision FROM credential_operations_login_configs WHERE account_id=$1", id).Scan(&cfg.LoginEmail, &cfg.CredentialMode, &cfg.Engine, &cfg.ProxySource, &cfg.ProxyID, &cfg.PasswordCiphertext, &cfg.TOTPCiphertext, &cfg.OTPURLCiphertext, &cfg.Revision); e != nil {
			return e
		}
		if cfg.CredentialMode != "password_totp" || cfg.PasswordCiphertext == "" || cfg.TOTPCiphertext == "" {
			return errors.New("saved password and current TOTP are required")
		}
		cfg.Engine = "local_worker"
		cfg.OAuthProfile = "codex"
		cfg.Action = action
		var generation int64
		if e = tx.QueryRowContext(ctx, "SELECT credential_generation FROM accounts WHERE id=$1 AND platform='openai' AND type='oauth' AND status<>'deleted'", id).Scan(&generation); e != nil {
			return e
		}
		if !db.isSQLite() {
			if _, e = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", id); e != nil {
				return e
			}
		}
		active, e := credentialOpsActiveTaskTx(ctx, tx, id)
		if e == nil {
			if active.Kind == "totp_"+action {
				result = active
				return tx.Commit()
			}
			return errors.New("another credential operation is active")
		}
		if e != sql.ErrNoRows {
			return e
		}
		var state, previous, candidate, recoveryEmail, recoveryPassword string
		e = tx.QueryRowContext(ctx, "SELECT state,previous_ciphertext,candidate_ciphertext,login_email,password_ciphertext FROM credential_operations_totp_recoveries WHERE account_id=$1", id).Scan(&state, &previous, &candidate, &recoveryEmail, &recoveryPassword)
		if e != nil && e != sql.ErrNoRows {
			return e
		}
		if action == "rotate" {
			if e == nil && state != "adopted" && state != "old_verified" {
				return errors.New("resolve pending recovery before rotating")
			}
		} else {
			if e == sql.ErrNoRows || (candidate == "" && action == "verify_new") {
				return errors.New("no saved recovery candidate")
			}
			cfg.LoginEmail = recoveryEmail
			cfg.PasswordCiphertext = recoveryPassword
			cfg.TOTPCiphertext = previous
			cfg.CandidateCiphertext = candidate
		}
		if e = db.freezeCredentialOpsRouteTx(ctx, tx, &cfg); e != nil {
			return e
		}
		result = &CredentialOpsTaskRow{AccountID: id, ExpectedGeneration: generation, ConfigRevision: cfg.Revision, OAuthProfile: "codex", Kind: "totp_" + action, Status: "queued", Stage: "queued"}
		if e = tx.QueryRowContext(ctx, "INSERT INTO credential_operations_tasks(account_id,status,stage,expected_generation,config_revision,kind) VALUES($1,'queued','queued',$2,$3,$4) RETURNING id,created_at,updated_at", id, generation, cfg.Revision, result.Kind).Scan(&result.ID, &result.CreatedAt, &result.UpdatedAt); e != nil {
			return e
		}
		raw, e := json.Marshal(cfg)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO credential_operations_task_inputs(task_id,config_json) VALUES($1,$2)", result.ID, string(raw)); e != nil {
			return e
		}
		if action == "rotate" {
			_, e = tx.ExecContext(ctx, "INSERT INTO credential_operations_totp_recoveries(account_id,task_id,state,previous_ciphertext,revision,login_email,password_ciphertext) VALUES($1,$2,'queued',$3,$4,$5,$6) ON CONFLICT(account_id) DO UPDATE SET task_id=excluded.task_id,state='queued',previous_ciphertext=excluded.previous_ciphertext,login_email=excluded.login_email,password_ciphertext=excluded.password_ciphertext,candidate_ciphertext='',revision=excluded.revision,updated_at=CURRENT_TIMESTAMP", id, result.ID, cfg.TOTPCiphertext, cfg.Revision, cfg.LoginEmail, cfg.PasswordCiphertext)
		} else {
			_, e = tx.ExecContext(ctx, "UPDATE credential_operations_totp_recoveries SET task_id=$1,state='queued_verification',updated_at=CURRENT_TIMESTAMP WHERE account_id=$2", result.ID, id)
		}
		if e != nil {
			return e
		}
		return tx.Commit()
	})
	return result, e
}
func (db *DB) credentialOpsFenceTx(ctx context.Context, tx *sql.Tx, id int64, owner string, attempt int) (int64, string, error) {
	var account int64
	var kind string
	var generation, revision, currentGeneration, currentRevision int64
	e := tx.QueryRowContext(ctx, db.credentialOpsLockQuery("SELECT account_id,kind,expected_generation,config_revision FROM credential_operations_tasks WHERE id=$1 AND lease_owner=$2 AND attempt=$3 AND status='running' AND lease_until>CURRENT_TIMESTAMP"), id, owner, attempt).Scan(&account, &kind, &generation, &revision)
	if e != nil {
		return 0, "", ErrCredentialOpsStale
	}
	e = tx.QueryRowContext(ctx, db.credentialOpsLockQuery("SELECT a.credential_generation,c.revision FROM accounts a JOIN credential_operations_login_configs c ON c.account_id=a.id WHERE a.id=$1 AND a.status<>'deleted'"), account).Scan(&currentGeneration, &currentRevision)
	if e != nil || generation != currentGeneration || revision != currentRevision {
		return 0, "", ErrCredentialOpsStale
	}
	cfg, e := credentialOpsConfigInTx(ctx, tx, id)
	if e != nil {
		return 0, "", e
	}
	if e = db.validateCredentialOpsRouteTx(ctx, tx, cfg); e != nil {
		return 0, "", e
	}
	return account, kind, nil
}
func (db *DB) CredentialOpsTOTPPhase(ctx context.Context, id int64, owner string, attempt int, phase, secret string) error {
	write := db.withWriteTx
	if phase == "prepared" {
		write = db.withCredentialOpsDurableTx
	}
	return write(ctx, func(tx *sql.Tx) error {
		account, kind, e := db.credentialOpsFenceTx(ctx, tx, id, owner, attempt)
		if e != nil {
			return e
		}
		if len(kind) < 5 || kind[:5] != "totp_" {
			return errors.New("not a rotation task")
		}
		var state, candidate string
		if e = tx.QueryRowContext(ctx, db.credentialOpsLockQuery("SELECT state,candidate_ciphertext FROM credential_operations_totp_recoveries WHERE account_id=$1 AND task_id=$2"), account, id).Scan(&state, &candidate); e != nil {
			return e
		}
		valid := false
		switch phase {
		case "enrolling":
			valid = state == "queued" && kind == "totp_rotate"
		case "prepared":
			valid = state == "enrolling" && secret != ""
			if valid {
				prepared, e := credentialops.PrepareConfig(credentialops.LoginConfigInput{Email: "validation@example.com", TOTPSecret: secret}, credentialops.LoginConfig{})
				if e != nil {
					return e
				}
				candidate = prepared.TOTPCiphertext
			}
		case "activating":
			valid = state == "prepared" && candidate != ""
		case "verifying":
			valid = state == "activating"
		case "verify_recovery":
			valid = state == "queued_verification" && kind != "totp_rotate"
		}
		if !valid {
			return errors.New("invalid recovery phase transition")
		}
		if _, e = tx.ExecContext(ctx, "UPDATE credential_operations_totp_recoveries SET state=$1,candidate_ciphertext=$2,updated_at=CURRENT_TIMESTAMP WHERE account_id=$3 AND task_id=$4", phase, candidate, account, id); e != nil {
			return e
		}
		result, e := tx.ExecContext(ctx, "UPDATE credential_operations_tasks SET stage=$1,updated_at=CURRENT_TIMESTAMP WHERE id=$2 AND lease_owner=$3 AND attempt=$4 AND status='running' AND lease_until>$5", phase, id, owner, attempt, db.timeArg(time.Now().UTC()))
		if e != nil {
			return e
		}
		if n, e := result.RowsAffected(); e != nil {
			return e
		} else if n != 1 {
			return ErrCredentialOpsStale
		}
		return nil
	})
}
func (db *DB) CredentialOpsTOTPFinish(ctx context.Context, id int64, owner string, attempt int, success bool) error {
	return db.withSQLiteWriteLock(ctx, func() error {
		tx, e := db.conn.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		account, kind, e := db.credentialOpsFenceTx(ctx, tx, id, owner, attempt)
		if e != nil {
			return e
		}
		var state, candidate, previous string
		if e = tx.QueryRowContext(ctx, db.credentialOpsLockQuery("SELECT state,candidate_ciphertext,previous_ciphertext FROM credential_operations_totp_recoveries WHERE account_id=$1 AND task_id=$2"), account, id).Scan(&state, &candidate, &previous); e != nil {
			return e
		}
		status, stage := "failed", "uncertain"
		if success {
			if state != "verifying" && state != "verify_recovery" {
				return errors.New("verification phase required")
			}
			status = "succeeded"
			stage = "adopted"
			chosen := candidate
			if kind == "totp_verify_old" {
				stage = "old_verified"
				chosen = previous
			}
			if chosen == "" {
				return errors.New("missing recovery secret")
			}
			if _, e = tx.ExecContext(ctx, "UPDATE credential_operations_login_configs SET totp_ciphertext=$1,revision=revision+1,updated_at=CURRENT_TIMESTAMP WHERE account_id=$2", chosen, account); e != nil {
				return e
			}
		}
		if _, e = tx.ExecContext(ctx, "UPDATE credential_operations_totp_recoveries SET state=$1,updated_at=CURRENT_TIMESTAMP WHERE account_id=$2", stage, account); e != nil {
			return e
		}
		result, e := tx.ExecContext(ctx, "UPDATE credential_operations_tasks SET status=$1,stage=$2,lease_owner='',lease_until=NULL,finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=$3 AND lease_owner=$4 AND attempt=$5 AND status='running' AND lease_until>$6", status, stage, id, owner, attempt, db.timeArg(time.Now().UTC()))
		if e != nil {
			return e
		}
		if n, e := result.RowsAffected(); e != nil {
			return e
		} else if n != 1 {
			return ErrCredentialOpsStale
		}
		return tx.Commit()
	})
}
func (db *DB) RecoverCredentialOpsTOTPCandidate(ctx context.Context, id int64, secret string) error {
	prepared, e := credentialops.PrepareConfig(credentialops.LoginConfigInput{Email: "validation@example.com", TOTPSecret: secret}, credentialops.LoginConfig{})
	if e != nil {
		return e
	}
	_, dec := credentialops.EnvCrypto()
	return db.withCredentialOpsDurableTx(ctx, func(tx *sql.Tx) error {
		var lockedTask int64
		if e := tx.QueryRowContext(ctx, db.credentialOpsLockQuery("SELECT id FROM credential_operations_tasks WHERE id=$1"), id).Scan(&lockedTask); e != nil {
			return e
		}
		var current, state string
		if e = tx.QueryRowContext(ctx, db.credentialOpsLockQuery("SELECT candidate_ciphertext,state FROM credential_operations_totp_recoveries WHERE task_id=$1"), id).Scan(&current, &state); e != nil {
			return e
		}
		if current != "" {
			a, e := dec(current)
			if e != nil || a != secret {
				return errors.New("candidate mismatch")
			}
		}
		if state != "uncertain" && state != "enrolling" && state != "prepared" {
			return errors.New("recovery state does not accept a candidate")
		}
		_, e := tx.ExecContext(ctx, "UPDATE credential_operations_totp_recoveries SET candidate_ciphertext=$1,state='uncertain',updated_at=CURRENT_TIMESTAMP WHERE task_id=$2", prepared.TOTPCiphertext, id)
		if e != nil {
			return e
		}
		return nil
	})
}
