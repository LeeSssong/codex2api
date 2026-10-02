package database

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

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
