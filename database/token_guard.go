package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"strings"
	"time"
)

var (
	ErrTokenGuardBusy     = errors.New("另一凭证守护任务正在运行")
	ErrTokenGuardStale    = errors.New("任务、配置或账号已变更，已拒绝旧结果")
	ErrTokenGuardDisabled = errors.New("请先启用智能运维模块")
)

const tokenGuardLeaseSeconds int64 = 90
const TokenGuardOwnedError = "token_guard:verified_auth_failure"

type TokenGuardJob struct {
	ID            string "json:\"job_id\""
	Kind          string "json:\"kind\""
	AccountID     int64  "json:\"account_id\""
	State         string "json:\"state\""
	Owner         string "json:\"-\""
	Fence         int64  "json:\"-\""
	ConfigVersion int64  "json:\"-\""
	LeaseUntil    int64  "json:\"-\""
	Cancellation  bool   "json:\"cancellation\""
	CreatedAt     int64  "json:\"created_at\""
	UpdatedAt     int64  "json:\"updated_at\""
	Stats         string "json:\"-\""
	Message       string "json:\"message\""
}

func (db *DB) ensureTokenGuardSchema(ctx context.Context) error {
	id := "BIGSERIAL PRIMARY KEY"
	if db.isSQLite() {
		id = "INTEGER PRIMARY KEY AUTOINCREMENT"
	}
	statements := []string{
		"CREATE TABLE IF NOT EXISTS account_ops_settings(key TEXT PRIMARY KEY,value TEXT NOT NULL)",
		"CREATE TABLE IF NOT EXISTS account_token_guard_config(id INTEGER PRIMARY KEY,value TEXT NOT NULL,revision BIGINT NOT NULL DEFAULT 1)",
		"INSERT INTO account_token_guard_config(id,value,revision) VALUES(1,'{}',1) ON CONFLICT(id) DO NOTHING",
		"CREATE TABLE IF NOT EXISTS account_token_guard_jobs(id TEXT PRIMARY KEY,kind TEXT NOT NULL,account_id BIGINT NOT NULL DEFAULT 0,state TEXT NOT NULL,owner TEXT NOT NULL DEFAULT '',fence BIGINT NOT NULL DEFAULT 0,config_version BIGINT NOT NULL,lease_until BIGINT NOT NULL DEFAULT 0,cancellation BOOLEAN NOT NULL DEFAULT FALSE,created_at BIGINT NOT NULL,updated_at BIGINT NOT NULL,stats TEXT NOT NULL DEFAULT '{}',message TEXT NOT NULL DEFAULT '')",
		"CREATE UNIQUE INDEX IF NOT EXISTS account_token_guard_one_active ON account_token_guard_jobs((1)) WHERE state IN ('queued','running','cancelling')",
		"CREATE INDEX IF NOT EXISTS account_token_guard_jobs_latest ON account_token_guard_jobs(created_at DESC)",
		"CREATE TABLE IF NOT EXISTS account_token_guard_states(account_id BIGINT PRIMARY KEY,state TEXT NOT NULL,last_probe_at BIGINT NOT NULL DEFAULT 0)",
		"CREATE TABLE IF NOT EXISTS account_token_guard_ownership(account_id BIGINT PRIMARY KEY,control_revision BIGINT NOT NULL,previous_status TEXT NOT NULL,previous_error TEXT NOT NULL,created_at BIGINT NOT NULL)",
		fmt.Sprintf("CREATE TABLE IF NOT EXISTS account_token_guard_events(id %s,account_id BIGINT NOT NULL,kind TEXT NOT NULL,detail TEXT NOT NULL,latency_ms INTEGER NOT NULL DEFAULT 0,created_at BIGINT NOT NULL)", id),
	}
	for _, q := range statements {
		if _, err := db.conn.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	if db.isSQLite() {
		_, err := db.conn.ExecContext(ctx, "CREATE TRIGGER IF NOT EXISTS token_guard_module_fence AFTER UPDATE ON account_ops_settings WHEN NEW.key='module_enabled' AND NEW.value IS NOT OLD.value BEGIN UPDATE account_token_guard_config SET revision=revision+1 WHERE id=1; UPDATE account_token_guard_jobs SET state='cancelled',cancellation=TRUE,fence=fence+1,message='模块配置已变更' WHERE state IN ('queued','running','cancelling'); END")
		return err
	}
	return db.execControlDDLTransaction(ctx, []string{
		"CREATE OR REPLACE FUNCTION token_guard_module_fence() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.key='module_enabled' AND NEW.value IS DISTINCT FROM OLD.value THEN UPDATE account_token_guard_config SET revision=revision+1 WHERE id=1; UPDATE account_token_guard_jobs SET state='cancelled',cancellation=TRUE,fence=fence+1,message='模块配置已变更' WHERE state IN ('queued','running','cancelling'); END IF; RETURN NULL; END $$",
		"DROP TRIGGER IF EXISTS token_guard_module_fence ON account_ops_settings",
		"CREATE TRIGGER token_guard_module_fence AFTER UPDATE ON account_ops_settings FOR EACH ROW EXECUTE FUNCTION token_guard_module_fence()",
	})
}
func (db *DB) TokenGuardModuleEnabled(ctx context.Context) (bool, error) {
	var value string
	err := db.conn.QueryRowContext(ctx, "SELECT value FROM account_ops_settings WHERE key='module_enabled'").Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return value == "true", err
}
func (db *DB) TokenGuardConfig(ctx context.Context) (string, int64, error) {
	var raw string
	var v int64
	err := db.conn.QueryRowContext(ctx, "SELECT value,revision FROM account_token_guard_config WHERE id=1").Scan(&raw, &v)
	if err != nil {
		return "", 0, err
	}
	decoded := decryptCredentialValue("token_guard_config", raw)
	if strings.HasPrefix(decoded, credEncPrefix) {
		return "", 0, errors.New("凭证守护配置无法解密")
	}
	return decoded, v, nil
}
func (db *DB) SaveTokenGuardConfig(ctx context.Context, raw string, expected int64) (int64, error) {
	var next int64
	err := db.withWriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE account_token_guard_config SET value=$1,revision=revision+1 WHERE id=1 AND revision=$2", encryptCredentialValue("token_guard_config", raw), expected)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrTokenGuardStale
		}
		if _, err = tx.ExecContext(ctx, "UPDATE account_token_guard_jobs SET state='cancelled',cancellation=TRUE,fence=fence+1,updated_at=$1,message='守护配置已变更' WHERE state IN ('queued','running','cancelling')", time.Now().Unix()); err != nil {
			return err
		}
		next = expected + 1
		return nil
	})
	return next, err
}
func (db *DB) guardConfigLock(ctx context.Context, tx *sql.Tx) (int64, error) {
	// A real write reservation comes before reads on SQLite. On PG this row
	// serializes config edits/admission and yields a consistent lock order.
	if _, err := tx.ExecContext(ctx, "UPDATE account_token_guard_config SET revision=revision WHERE id=1"); err != nil {
		return 0, err
	}
	var v int64
	if err := tx.QueryRowContext(ctx, "SELECT revision FROM account_token_guard_config WHERE id=1").Scan(&v); err != nil {
		return 0, err
	}
	var enabled string
	err := tx.QueryRowContext(ctx, "SELECT value FROM account_ops_settings WHERE key='module_enabled'").Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) || err == nil && enabled != "true" {
		return 0, ErrTokenGuardDisabled
	}
	return v, err
}

const tokenGuardJobColumns = "id,kind,account_id,state,owner,fence,config_version,lease_until,cancellation,created_at,updated_at,stats,message"

func scanTokenGuardJob(row interface{ Scan(...any) error }) (*TokenGuardJob, error) {
	var j TokenGuardJob
	err := row.Scan(&j.ID, &j.Kind, &j.AccountID, &j.State, &j.Owner, &j.Fence, &j.ConfigVersion, &j.LeaseUntil, &j.Cancellation, &j.CreatedAt, &j.UpdatedAt, &j.Stats, &j.Message)
	return &j, err
}
func (db *DB) CreateTokenGuardJob(ctx context.Context, kind string, accountID, version int64) (*TokenGuardJob, error) {
	if kind != "cycle" && kind != "relogin" || kind == "relogin" && accountID <= 0 {
		return nil, errors.New("无效的守护任务")
	}
	now := time.Now().Unix()
	j := &TokenGuardJob{ID: uuid.NewString(), Kind: kind, AccountID: accountID, State: "queued", ConfigVersion: version, CreatedAt: now, UpdatedAt: now, Stats: "{}"}
	err := db.withWriteTx(ctx, func(tx *sql.Tx) error {
		v, err := db.guardConfigLock(ctx, tx)
		if err != nil {
			return err
		}
		if v != version {
			return ErrTokenGuardStale
		}
		if _, err = tx.ExecContext(ctx, "UPDATE account_token_guard_jobs SET state='failed',fence=fence+1,message='任务租约过期，旧结果已失效',updated_at=$1 WHERE state IN ('running','cancelling') AND lease_until<$1", now); err != nil {
			return err
		}
		var n int
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM account_token_guard_jobs WHERE state IN ('queued','running','cancelling')").Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrTokenGuardBusy
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO account_token_guard_jobs(id,kind,account_id,state,config_version,created_at,updated_at) VALUES($1,$2,$3,'queued',$4,$5,$5)", j.ID, kind, accountID, version, now)
		return err
	})
	if err != nil {
		return nil, err
	}
	return j, nil
}
func (db *DB) ClaimTokenGuardJob(ctx context.Context, owner string, now time.Time) (*TokenGuardJob, error) {
	var claimed *TokenGuardJob
	err := db.withWriteTx(ctx, func(tx *sql.Tx) error {
		v, err := db.guardConfigLock(ctx, tx)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE account_token_guard_jobs SET state='failed',fence=fence+1,message='任务租约过期，旧结果已失效',updated_at=$1 WHERE state IN ('running','cancelling') AND lease_until<$1", now.Unix()); err != nil {
			return err
		}
		j, err := scanTokenGuardJob(tx.QueryRowContext(ctx, "SELECT "+tokenGuardJobColumns+" FROM account_token_guard_jobs WHERE state='queued' ORDER BY created_at,id LIMIT 1"))
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if j.ConfigVersion != v {
			_, err = tx.ExecContext(ctx, "UPDATE account_token_guard_jobs SET state='cancelled',cancellation=TRUE,fence=fence+1 WHERE id=$1", j.ID)
			return err
		}
		j.State = "running"
		j.Owner = owner
		j.Fence++
		j.LeaseUntil = now.Unix() + tokenGuardLeaseSeconds
		j.UpdatedAt = now.Unix()
		_, err = tx.ExecContext(ctx, "UPDATE account_token_guard_jobs SET state='running',owner=$1,fence=$2,lease_until=$3,updated_at=$4 WHERE id=$5 AND state='queued'", j.Owner, j.Fence, j.LeaseUntil, j.UpdatedAt, j.ID)
		if err == nil {
			claimed = j
		}
		return err
	})
	return claimed, err
}
func (db *DB) guardJobTx(ctx context.Context, tx *sql.Tx, j TokenGuardJob) error {
	v, err := db.guardConfigLock(ctx, tx)
	if err != nil {
		return err
	}
	if v != j.ConfigVersion {
		return ErrTokenGuardStale
	}
	var n int
	err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM account_token_guard_jobs WHERE id=$1 AND owner=$2 AND fence=$3 AND config_version=$4 AND state='running' AND cancellation=FALSE AND lease_until>=$5", j.ID, j.Owner, j.Fence, j.ConfigVersion, time.Now().Unix()).Scan(&n)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrTokenGuardStale
	}
	return nil
}
func (db *DB) RenewTokenGuardJob(ctx context.Context, j TokenGuardJob, now time.Time) error {
	return db.withWriteTx(ctx, func(tx *sql.Tx) error {
		if err := db.guardJobTx(ctx, tx, j); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE account_token_guard_jobs SET lease_until=$1,updated_at=$2 WHERE id=$3 AND owner=$4 AND fence=$5", now.Unix()+tokenGuardLeaseSeconds, now.Unix(), j.ID, j.Owner, j.Fence)
		return err
	})
}
func (db *DB) CancelTokenGuardJob(ctx context.Context, id string) (bool, error) {
	var changed bool
	err := db.withWriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE account_token_guard_config SET revision=revision WHERE id=1"); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, "UPDATE account_token_guard_jobs SET state='cancelled',cancellation=TRUE,fence=fence+1,updated_at=$1,message='任务已取消' WHERE id=$2 AND state IN ('queued','running','cancelling')", time.Now().Unix(), id)
		if err == nil {
			n, e := res.RowsAffected()
			changed = n > 0
			return e
		}
		return err
	})
	return changed, err
}
func (db *DB) FinishTokenGuardJob(ctx context.Context, j TokenGuardJob, state, stats, message string) error {
	if state != "completed" && state != "failed" && state != "cancelled" {
		return errors.New("无效任务终态")
	}
	return db.withWriteTx(ctx, func(tx *sql.Tx) error {
		if err := db.guardJobTx(ctx, tx, j); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE account_token_guard_jobs SET state=$1,stats=$2,message=$3,lease_until=0,updated_at=$4 WHERE id=$5 AND owner=$6 AND fence=$7", state, stats, message, time.Now().Unix(), j.ID, j.Owner, j.Fence)
		return err
	})
}
func (db *DB) TokenGuardJob(ctx context.Context, id string) (*TokenGuardJob, error) {
	return scanTokenGuardJob(db.conn.QueryRowContext(ctx, "SELECT "+tokenGuardJobColumns+" FROM account_token_guard_jobs WHERE id=$1", id))
}
func (db *DB) LatestTokenGuardJob(ctx context.Context) (*TokenGuardJob, error) {
	order := "created_at DESC,updated_at DESC,id DESC"
	if db.isSQLite() {
		order = "created_at DESC,rowid DESC"
	}
	j, err := scanTokenGuardJob(db.conn.QueryRowContext(ctx, "SELECT "+tokenGuardJobColumns+" FROM account_token_guard_jobs ORDER BY "+order+" LIMIT 1"))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return j, err
}
