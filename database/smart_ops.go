package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/codex2api/smartops"
)

func (db *DB) GetSmartOpsConfig(ctx context.Context, key string, target any) error {
	if err := db.EnsureSmartOpsSchema(ctx); err != nil {
		return err
	}
	var raw string
	err := db.conn.QueryRowContext(ctx, `SELECT value FROM smart_ops_settings WHERE key=$1`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(raw), target)
}
func (db *DB) PutSmartOpsConfig(ctx context.Context, key string, value any) error {
	if err := db.EnsureSmartOpsSchema(ctx); err != nil {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = db.conn.ExecContext(ctx, `INSERT INTO smart_ops_settings(key,value,updated_at) VALUES($1,$2,CURRENT_TIMESTAMP) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=CURRENT_TIMESTAMP`, key, string(raw))
	return err
}
func (db *DB) LoadOAuthAutoConfig(ctx context.Context) (smartops.OAuthAutoConfig, error) {
	c := smartops.DefaultOAuthAutoConfig()
	return c, db.GetSmartOpsConfig(ctx, "oauth_auto_config", &c)
}
func (db *DB) SaveOAuthAutoConfig(ctx context.Context, c smartops.OAuthAutoConfig) error {
	return db.PutSmartOpsConfig(ctx, "oauth_auto_config", c)
}
func (db *DB) LoadPriorityScheduling(ctx context.Context) (smartops.PriorityConfig, error) {
	c := smartops.DefaultPriorityConfig()
	return c, db.GetSmartOpsConfig(ctx, "priority_scheduling", &c)
}
func (db *DB) SavePriorityScheduling(ctx context.Context, c smartops.PriorityConfig) error {
	return db.PutSmartOpsConfig(ctx, "priority_scheduling", c)
}

// SmartOpsJob is the durable lease boundary used by the Pelican adapter.
// Account lookup, eligibility, billing and probe execution remain native.
type SmartOpsJob struct {
	ID         int64
	AccountID  int64
	Model      string
	Samples    int
	Parallel   int
	Retries    int
	DueAt      time.Time
	LeaseOwner string
	LeaseUntil *time.Time
	Attempts   int
}

func (db *DB) CreateSmartOpsPelicanJob(ctx context.Context, j SmartOpsJob) (int64, error) {
	if err := db.EnsureSmartOpsSchema(ctx); err != nil {
		return 0, err
	}
	if j.Samples < 1 {
		j.Samples = 1
	}
	if j.Parallel < 1 {
		j.Parallel = 1
	}
	if j.DueAt.IsZero() {
		j.DueAt = time.Now().UTC()
	}
	if db.isSQLite() {
		r, e := db.conn.ExecContext(ctx, `INSERT INTO smart_ops_pelican_jobs(account_id,model,samples,parallel,retries,due_at) VALUES($1,$2,$3,$4,$5,$6)`, j.AccountID, j.Model, j.Samples, j.Parallel, j.Retries, j.DueAt)
		if e != nil {
			return 0, e
		}
		return r.LastInsertId()
	}
	var id int64
	err := db.conn.QueryRowContext(ctx, `INSERT INTO smart_ops_pelican_jobs(account_id,model,samples,parallel,retries,due_at) VALUES($1,$2,$3,$4,$5,$6) RETURNING id`, j.AccountID, j.Model, j.Samples, j.Parallel, j.Retries, j.DueAt).Scan(&id)
	return id, err
}
func (db *DB) ListSmartOpsPelicanJobs(ctx context.Context, limit int) ([]SmartOpsJob, error) {
	if err := db.EnsureSmartOpsSchema(ctx); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := db.conn.QueryContext(ctx, `SELECT id,account_id,model,samples,parallel,retries,due_at,lease_owner,lease_until,attempts FROM smart_ops_pelican_jobs ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SmartOpsJob
	for rows.Next() {
		var j SmartOpsJob
		if err = rows.Scan(&j.ID, &j.AccountID, &j.Model, &j.Samples, &j.Parallel, &j.Retries, &j.DueAt, &j.LeaseOwner, &j.LeaseUntil, &j.Attempts); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
func (db *DB) CancelSmartOpsPelicanJob(ctx context.Context, id int64) error {
	if err := db.EnsureSmartOpsSchema(ctx); err != nil {
		return err
	}
	r, err := db.conn.ExecContext(ctx, `DELETE FROM smart_ops_pelican_jobs WHERE id=$1 AND lease_until IS NULL`, id)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (db *DB) EnsureSmartOpsSchema(ctx context.Context) error {
	if db == nil || db.conn == nil {
		return errors.New("database is not initialized")
	}
	id := "INTEGER PRIMARY KEY AUTOINCREMENT"
	if db.driver == "postgres" || db.driver == "postgresql" {
		id = "BIGSERIAL PRIMARY KEY"
	}
	_, err := db.conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS smart_ops_pelican_jobs (id `+id+`, account_id BIGINT NOT NULL, model TEXT NOT NULL DEFAULT '', samples INTEGER NOT NULL DEFAULT 1, parallel INTEGER NOT NULL DEFAULT 1, retries INTEGER NOT NULL DEFAULT 1, due_at TIMESTAMP NOT NULL, lease_owner TEXT NOT NULL DEFAULT '', lease_until TIMESTAMP NULL, attempts INTEGER NOT NULL DEFAULT 0, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`)
	if err != nil {
		return err
	}
	if _, err = db.conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS smart_ops_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '{}', updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		return err
	}
	rid := "INTEGER PRIMARY KEY AUTOINCREMENT"
	if db.driver == "postgres" || db.driver == "postgresql" {
		rid = "BIGSERIAL PRIMARY KEY"
	}
	_, err = db.conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS smart_ops_pelican_results (id `+rid+`, job_id BIGINT NOT NULL, account_id BIGINT NOT NULL, sample INTEGER NOT NULL, status TEXT NOT NULL, output TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', latency_ms BIGINT NOT NULL DEFAULT 0, started_at TIMESTAMP NOT NULL, finished_at TIMESTAMP NOT NULL)`)
	return err
}

func (db *DB) ClaimSmartOpsPelicanJob(ctx context.Context, owner string, now, until time.Time) (*SmartOpsJob, error) {
	if err := db.EnsureSmartOpsSchema(ctx); err != nil {
		return nil, err
	}
	tx, err := db.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	row := tx.QueryRowContext(ctx, `SELECT id,account_id,model,samples,parallel,retries,due_at,lease_owner,lease_until,attempts FROM smart_ops_pelican_jobs WHERE due_at <= $1 AND (lease_until IS NULL OR lease_until < $1) ORDER BY due_at,id LIMIT 1`, now)
	var j SmartOpsJob
	if err = row.Scan(&j.ID, &j.AccountID, &j.Model, &j.Samples, &j.Parallel, &j.Retries, &j.DueAt, &j.LeaseOwner, &j.LeaseUntil, &j.Attempts); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE smart_ops_pelican_jobs SET lease_owner=$1,lease_until=$2,attempts=attempts+1,updated_at=CURRENT_TIMESTAMP WHERE id=$3 AND (lease_until IS NULL OR lease_until < $4)`, owner, until, j.ID, now)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return nil, nil
	}
	j.LeaseOwner = owner
	j.LeaseUntil = &until
	j.Attempts++
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &j, nil
}

func (db *DB) SaveSmartOpsPelicanResult(ctx context.Context, jobID, accountID int64, sample int, status, output, message string, latency time.Duration, started, finished time.Time) error {
	if err := db.EnsureSmartOpsSchema(ctx); err != nil {
		return err
	}
	_, err := db.conn.ExecContext(ctx, `INSERT INTO smart_ops_pelican_results(job_id,account_id,sample,status,output,error,latency_ms,started_at,finished_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, jobID, accountID, sample, status, output, message, latency.Milliseconds(), started, finished)
	return err
}

func (db *DB) ReleaseSmartOpsPelicanJob(ctx context.Context, jobID int64, owner string, next time.Time) error {
	_, err := db.conn.ExecContext(ctx, `UPDATE smart_ops_pelican_jobs SET lease_owner='',lease_until=NULL,due_at=$1,updated_at=CURRENT_TIMESTAMP WHERE id=$2 AND lease_owner=$3`, next, jobID, owner)
	return err
}
