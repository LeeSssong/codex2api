package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Data contains public metadata; Secret is never included in API list responses.
type StatePoolRow struct {
	ID        string
	AccountID int64
	Model     string
	Effort    string
	Status    string
	Data      string
	Secret    string
	Owner     string
	CreatedAt int64
	UpdatedAt int64
	GroupID   string
	Ordinal   int
	Serial    bool
}

func (db *DB) ensureStatePoolSchema(ctx context.Context) error {
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS codex_state_entries (id TEXT PRIMARY KEY, account_id BIGINT NOT NULL,
		 model TEXT NOT NULL, effort TEXT NOT NULL, data TEXT NOT NULL, secret TEXT NOT NULL,
		 UNIQUE(account_id,model,effort))`,
		`CREATE TABLE IF NOT EXISTS codex_state_jobs (id TEXT PRIMARY KEY, account_id BIGINT NOT NULL,
		 model TEXT NOT NULL, effort TEXT NOT NULL, status TEXT NOT NULL, data TEXT NOT NULL,
		 secret TEXT NOT NULL DEFAULT '', owner TEXT NOT NULL DEFAULT '', lease_until BIGINT NOT NULL DEFAULT 0,
		 created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS idx_codex_state_queue ON codex_state_jobs(status,created_at)`,
		`CREATE TABLE IF NOT EXISTS codex_state_config (id INTEGER PRIMARY KEY, concurrency INTEGER NOT NULL,
		 per_account INTEGER NOT NULL)`,
		`INSERT INTO codex_state_config(id,concurrency,per_account) VALUES(1,4,2) ON CONFLICT(id) DO NOTHING`,
	} {
		if _, err := db.conn.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize state pool: %w", err)
		}
	}
	for _, column := range []struct{ table, name, definition string }{
		{"codex_state_jobs", "group_id", "TEXT NOT NULL DEFAULT ''"},
		{"codex_state_jobs", "ordinal", "INTEGER NOT NULL DEFAULT 1"},
		{"codex_state_jobs", "serial", "INTEGER NOT NULL DEFAULT 0"},
		{"codex_state_jobs", "route_key", "TEXT NOT NULL DEFAULT ''"},
		{"codex_state_jobs", "forward_route_key", "TEXT NOT NULL DEFAULT ''"},
		{"codex_state_config", "per_proxy", "INTEGER NOT NULL DEFAULT 2"},
	} {
		optional := "IF NOT EXISTS "
		if db.isSQLite() {
			columns, err := db.sqliteTableColumns(ctx, column.table)
			if err != nil {
				return err
			}
			if _, exists := columns[column.name]; exists {
				continue
			}
			optional = ""
		}
		if _, err := db.conn.ExecContext(ctx, "ALTER TABLE "+column.table+" ADD COLUMN "+optional+column.name+" "+column.definition); err != nil {
			return err
		}
	}
	for _, statement := range []string{
		`UPDATE codex_state_jobs SET group_id=id WHERE group_id=''`,
		`DROP INDEX IF EXISTS idx_codex_state_active`,
		`CREATE INDEX IF NOT EXISTS idx_codex_state_group ON codex_state_jobs(group_id,status)`,
	} {
		if _, err := db.conn.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (db *DB) StatePoolLimits(ctx context.Context) (int, int, error) {
	var global, account int
	err := db.conn.QueryRowContext(ctx, `SELECT concurrency,per_account FROM codex_state_config WHERE id=1`).Scan(&global, &account)
	return global, account, err
}

func (db *DB) SetStatePoolLimits(ctx context.Context, global, account int, proxyLimit ...int) error {
	perProxy := 2
	if len(proxyLimit) > 0 {
		perProxy = proxyLimit[0]
	}
	if global < 1 || global > 32 || account < 1 || account > 4 || account > global || perProxy < 1 || perProxy > 8 {
		return errors.New("invalid state pool concurrency limits")
	}
	_, err := db.conn.ExecContext(ctx, `UPDATE codex_state_config SET concurrency=$1,per_account=$2,per_proxy=$3 WHERE id=1`, global, account, perProxy)
	return err
}

// Serializing admission and claims also enforces limits across server replicas.
func (db *DB) lockStatePool(ctx context.Context, tx *sql.Tx) (int, int, error) {
	query := `SELECT concurrency,per_account FROM codex_state_config WHERE id=1`
	if !db.isSQLite() {
		query += ` FOR UPDATE`
	}
	var global, account int
	err := tx.QueryRowContext(ctx, query).Scan(&global, &account)
	return global, account, err
}

func (db *DB) CreateStatePoolJobs(ctx context.Context, jobs []StatePoolRow) ([]string, error) {
	if len(jobs) == 0 || len(jobs) > 1536 {
		return nil, errors.New("a state batch must contain 1 to 1536 candidates")
	}
	ids := []string{}
	err := db.withWriteTx(ctx, func(tx *sql.Tx) error {
		if _, _, err := db.lockStatePool(ctx, tx); err != nil {
			return err
		}
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM codex_state_jobs WHERE status IN ('queued','running','cancelling')`).Scan(&active); err != nil {
			return err
		}
		if active+len(jobs) > 2048 {
			return errors.New("state task queue is full (2048)")
		}
		accepted := map[string]bool{}
		for _, job := range jobs {
			if job.GroupID == "" {
				job.GroupID = job.ID
			}
			allow, checked := accepted[job.GroupID]
			if !checked {
				var existing int
				if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM codex_state_jobs WHERE account_id=$1 AND model=$2 AND effort=$3 AND status IN ('queued','running','cancelling')`, job.AccountID, job.Model, job.Effort).Scan(&existing); err != nil {
					return err
				}
				allow = existing == 0
				accepted[job.GroupID] = allow
			}
			if !allow {
				continue
			}
			serial := 0
			if job.Serial {
				serial = 1
			}
			result, err := tx.ExecContext(ctx, `INSERT INTO codex_state_jobs
			 (id,account_id,model,effort,status,data,secret,created_at,updated_at,group_id,ordinal,serial)
			 VALUES($1,$2,$3,$4,'queued',$5,$6,$7,$7,$8,$9,$10) ON CONFLICT DO NOTHING`,
				job.ID, job.AccountID, job.Model, job.Effort, job.Data, job.Secret, job.CreatedAt, job.GroupID, job.Ordinal, serial)
			if err != nil {
				return err
			}
			if count, _ := result.RowsAffected(); count == 1 {
				ids = append(ids, job.ID)
			}
		}
		return nil
	})
	return ids, err
}

const statePoolJobColumns = `id,account_id,model,effort,status,data,secret,owner,created_at,updated_at,group_id,ordinal,serial`

func scanStatePoolJob(scanner interface{ Scan(...any) error }) (StatePoolRow, error) {
	var r StatePoolRow
	var serial int
	err := scanner.Scan(&r.ID, &r.AccountID, &r.Model, &r.Effort, &r.Status, &r.Data, &r.Secret, &r.Owner, &r.CreatedAt, &r.UpdatedAt, &r.GroupID, &r.Ordinal, &serial)
	r.Serial = serial != 0
	return r, err
}

func (db *DB) ClaimStatePoolJob(ctx context.Context, owner string, now time.Time) (*StatePoolRow, error) {
	var claimed *StatePoolRow
	err := db.withWriteTx(ctx, func(tx *sql.Tx) error {
		global, perAccount, err := db.lockStatePool(ctx, tx)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE codex_state_jobs SET status='interrupted',secret='',updated_at=$1
		 WHERE status IN ('running','cancelling') AND lease_until<$1`, now.Unix()); err != nil {
			return err
		}
		var running int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM codex_state_jobs WHERE status IN ('running','cancelling')`).Scan(&running); err != nil {
			return err
		}
		if running >= global {
			return nil
		}
		row, err := scanStatePoolJob(tx.QueryRowContext(ctx, `SELECT `+statePoolJobColumns+` FROM codex_state_jobs q
		 WHERE status='queued' AND (SELECT COUNT(*) FROM codex_state_jobs a WHERE a.account_id=q.account_id
		 AND a.status IN ('running','cancelling'))<$1
		 AND (q.serial=0 OR NOT EXISTS (SELECT 1 FROM codex_state_jobs s WHERE s.group_id=q.group_id AND s.status IN ('running','cancelling')))
		 ORDER BY created_at,ordinal,id LIMIT 1`, perAccount))
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE codex_state_jobs SET status='running',owner=$1,lease_until=$2,updated_at=$3 WHERE id=$4`,
			owner, now.Add(90*time.Second).Unix(), now.Unix(), row.ID); err != nil {
			return err
		}
		row.Status, row.Owner, row.UpdatedAt = "running", owner, now.Unix()
		claimed = &row
		return nil
	})
	return claimed, err
}

func (db *DB) ListStatePoolJobs(ctx context.Context) ([]StatePoolRow, error) {
	rows, err := db.conn.QueryContext(ctx, `SELECT `+statePoolJobColumns+` FROM codex_state_jobs
	 ORDER BY CASE WHEN status IN ('queued','running','cancelling') THEN 0 ELSE 1 END,created_at DESC,group_id,ordinal LIMIT 4096`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []StatePoolRow{}
	for rows.Next() {
		row, err := scanStatePoolJob(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (db *DB) UpdateStatePoolJob(ctx context.Context, row StatePoolRow, now time.Time) (bool, error) {
	result, err := db.conn.ExecContext(ctx, `UPDATE codex_state_jobs SET data=$1,status=$2,updated_at=$3,
	 secret=CASE WHEN $2='running' THEN secret ELSE '' END WHERE id=$4 AND owner=$5 AND status='running'`,
		row.Data, row.Status, now.Unix(), row.ID, row.Owner)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (db *DB) HeartbeatStatePoolJob(ctx context.Context, id, owner string, now time.Time) (bool, error) {
	result, err := db.conn.ExecContext(ctx, `UPDATE codex_state_jobs SET lease_until=$1 WHERE id=$2 AND owner=$3 AND status='running'`,
		now.Add(90*time.Second).Unix(), id, owner)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (db *DB) CancelStatePoolJob(ctx context.Context, id string) error {
	_, err := db.conn.ExecContext(ctx, `UPDATE codex_state_jobs SET
	 status=CASE WHEN status='queued' THEN 'cancelled' ELSE 'cancelling' END,secret='',updated_at=$1
	 WHERE id=$2 AND status IN ('queued','running')`, time.Now().Unix(), id)
	return err
}

func (db *DB) FinishCancelledStatePoolJob(ctx context.Context, id, owner string) error {
	_, err := db.conn.ExecContext(ctx, `UPDATE codex_state_jobs SET status='cancelled',secret='',updated_at=$1
	 WHERE id=$2 AND owner=$3 AND status='cancelling'`, time.Now().Unix(), id, owner)
	return err
}

func (db *DB) ListStatePoolEntries(ctx context.Context) ([]StatePoolRow, error) {
	rows, err := db.conn.QueryContext(ctx, `SELECT id,account_id,model,effort,data,secret FROM codex_state_entries ORDER BY account_id,model,effort`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []StatePoolRow{}
	for rows.Next() {
		var row StatePoolRow
		if err := rows.Scan(&row.ID, &row.AccountID, &row.Model, &row.Effort, &row.Data, &row.Secret); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// Publishing and finishing are one transaction, fenced by job ownership and credentials.
func (db *DB) PublishStatePoolEntry(ctx context.Context, entry StatePoolRow, generation int64, job StatePoolRow, now time.Time) error {
	return db.withWriteTx(ctx, func(tx *sql.Tx) error {
		if _, _, err := db.lockStatePool(ctx, tx); err != nil {
			return err
		}
		query := `SELECT credential_generation FROM accounts WHERE id=$1 AND deleted_at IS NULL`
		if !db.isSQLite() {
			query += ` FOR UPDATE`
		}
		var current int64
		if err := tx.QueryRowContext(ctx, query, entry.AccountID).Scan(&current); err != nil {
			return err
		}
		if current != generation {
			return errors.New("account credentials changed during validation")
		}
		result, err := tx.ExecContext(ctx, `UPDATE codex_state_jobs SET status='completed',data=$1,secret='',updated_at=$2
		 WHERE id=$3 AND owner=$4 AND status='running'`, job.Data, now.Unix(), job.ID, job.Owner)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return errors.New("state job cancelled or ownership lost")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE codex_state_jobs SET status=CASE WHEN status='queued' THEN 'superseded' ELSE 'cancelling' END,secret='',updated_at=$1
		 WHERE group_id=$2 AND id<>$3 AND status IN ('queued','running')`, now.Unix(), job.GroupID, job.ID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO codex_state_entries(id,account_id,model,effort,data,secret)
		 VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(account_id,model,effort) DO UPDATE SET
		 id=excluded.id,data=excluded.data,secret=excluded.secret`, entry.ID, entry.AccountID, entry.Model, entry.Effort, entry.Data, entry.Secret)
		return err
	})
}

func (db *DB) StatePoolProxyLimit(ctx context.Context) (int, error) {
	var value int
	err := db.conn.QueryRowContext(ctx, `SELECT per_proxy FROM codex_state_config WHERE id=1`).Scan(&value)
	return value, err
}

// A request reserves its current egress only while its response body is open.
func (db *DB) AcquireStatePoolRoute(ctx context.Context, id, owner, route string, forwards ...string) (bool, error) {
	forward := ""
	if len(forwards) > 0 {
		forward = forwards[0]
	}
	acquired := false
	err := db.withWriteTx(ctx, func(tx *sql.Tx) error {
		if _, _, err := db.lockStatePool(ctx, tx); err != nil {
			return err
		}
		var limit, count int
		if err := tx.QueryRowContext(ctx, `SELECT per_proxy FROM codex_state_config WHERE id=1`).Scan(&limit); err != nil {
			return err
		}
		for _, candidate := range []string{route, forward} {
			if candidate == "" {
				continue
			}
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM codex_state_jobs WHERE id<>$1 AND (route_key=$2 OR forward_route_key=$2) AND status IN ('running','cancelling')`, id, candidate).Scan(&count); err != nil {
				return err
			}
			if count >= limit {
				return nil
			}
		}
		result, err := tx.ExecContext(ctx, `UPDATE codex_state_jobs SET route_key=$1,forward_route_key=$4 WHERE id=$2 AND owner=$3 AND status='running'`, route, id, owner, forward)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		acquired = n == 1
		return err
	})
	return acquired, err
}

func (db *DB) ReleaseStatePoolRoute(ctx context.Context, id, owner string) error {
	_, err := db.conn.ExecContext(ctx, `UPDATE codex_state_jobs SET route_key='',forward_route_key='' WHERE id=$1 AND owner=$2`, id, owner)
	return err
}

func (db *DB) CancelStatePoolGroup(ctx context.Context, group string) error {
	_, err := db.conn.ExecContext(ctx, `UPDATE codex_state_jobs SET status=CASE WHEN status='queued' THEN 'cancelled' ELSE 'cancelling' END,secret='',updated_at=$1 WHERE group_id=$2 AND status IN ('queued','running')`, time.Now().Unix(), group)
	return err
}

func (db *DB) HaltStatePoolAccount(ctx context.Context, accountID int64, except string) error {
	_, err := db.conn.ExecContext(ctx, `UPDATE codex_state_jobs SET status=CASE WHEN status='queued' THEN 'blocked' ELSE 'cancelling' END,secret='',updated_at=$1 WHERE account_id=$2 AND id<>$3 AND status IN ('queued','running')`, time.Now().Unix(), accountID, except)
	return err
}

func (db *DB) UpdateStatePoolEntry(ctx context.Context, id, data string) error {
	result, err := db.conn.ExecContext(ctx, `UPDATE codex_state_entries SET data=$1 WHERE id=$2`, data, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (db *DB) DeleteStatePoolEntry(ctx context.Context, id string) error {
	_, err := db.conn.ExecContext(ctx, `DELETE FROM codex_state_entries WHERE id=$1`, id)
	return err
}
