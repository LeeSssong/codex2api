package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type FixedEgressEnrollment struct {
	AccountID int64  `json:"account_id"`
	PoolID    string `json:"pool_id"`
}
type FixedEgressEvent struct {
	ID        int64     `json:"id"`
	AccountID int64     `json:"account_id"`
	Kind      string    `json:"kind"`
	Detail    string    `json:"detail"`
	CreatedAt time.Time `json:"created_at"`
}

func (db *DB) HasFixedEgressEnrollment(ctx context.Context, id int64) (bool, error) {
	var n int
	e := db.conn.QueryRowContext(ctx, `SELECT count(*) FROM fixed_egress_enrollments WHERE account_id=$1`, id).Scan(&n)
	return n > 0, e
}
func (db *DB) GetFixedEgressPool(ctx context.Context, id string) (FixedEgressPool, error) {
	var p FixedEgressPool
	e := db.conn.QueryRowContext(ctx, `SELECT id,name,enabled,strategy,cooldown_seconds,max_attempts,rotate_429,retry_5xx,updated_at FROM fixed_egress_pools WHERE id=$1`, id).Scan(&p.ID, &p.Name, &p.Enabled, &p.Strategy, &p.CooldownSeconds, &p.MaxAttempts, &p.Rotate429, &p.Retry5xx, &p.UpdatedAt)
	return p, e
}
func (db *DB) ListFixedEgressPools(ctx context.Context) ([]FixedEgressPool, error) {
	rows, e := db.conn.QueryContext(ctx, `SELECT id,name,enabled,strategy,cooldown_seconds,max_attempts,rotate_429,retry_5xx,updated_at FROM fixed_egress_pools ORDER BY id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []FixedEgressPool{}
	for rows.Next() {
		var p FixedEgressPool
		if e = rows.Scan(&p.ID, &p.Name, &p.Enabled, &p.Strategy, &p.CooldownSeconds, &p.MaxAttempts, &p.Rotate429, &p.Retry5xx, &p.UpdatedAt); e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (db *DB) ListFixedEgressEnrollments(ctx context.Context) ([]FixedEgressEnrollment, error) {
	rows, e := db.conn.QueryContext(ctx, `SELECT account_id,pool_id FROM fixed_egress_enrollments ORDER BY account_id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []FixedEgressEnrollment{}
	for rows.Next() {
		var a FixedEgressEnrollment
		if e = rows.Scan(&a.AccountID, &a.PoolID); e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (db *DB) ListFixedEgressProviders(ctx context.Context) ([]FixedEgressProvider, error) {
	rows, e := db.conn.QueryContext(ctx, `SELECT id FROM fixed_egress_providers ORDER BY id`)
	if e != nil {
		return nil, e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return nil, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	out := []FixedEgressProvider{}
	for _, id := range ids {
		p, e := db.GetFixedEgressProvider(ctx, id)
		if e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, nil
}
func (db *DB) GetFixedEgressResource(ctx context.Context, id string) (FixedEgressResource, error) {
	rs, e := db.ListFixedEgress(ctx)
	if e != nil {
		return FixedEgressResource{}, e
	}
	for _, r := range rs {
		if r.ID == id {
			return r, nil
		}
	}
	return FixedEgressResource{}, sql.ErrNoRows
}
func (db *DB) ListFixedEgressTasks(ctx context.Context) ([]FixedEgressTask, error) {
	rows, e := db.conn.QueryContext(ctx, `SELECT id,provider_id,port,state,result,resource_id,created_at,updated_at FROM fixed_egress_tasks ORDER BY created_at DESC LIMIT 500`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []FixedEgressTask{}
	for rows.Next() {
		var t FixedEgressTask
		if e = rows.Scan(&t.ID, &t.ProviderID, &t.Port, &t.State, &t.Result, &t.ResourceID, &t.CreatedAt, &t.UpdatedAt); e != nil {
			return nil, e
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
func (db *DB) ListFixedEgressEvents(ctx context.Context) ([]FixedEgressEvent, error) {
	rows, e := db.conn.QueryContext(ctx, `SELECT id,account_id,kind,detail,created_at FROM fixed_egress_events ORDER BY created_at DESC,id DESC LIMIT 200`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []FixedEgressEvent{}
	for rows.Next() {
		var t FixedEgressEvent
		if e = rows.Scan(&t.ID, &t.AccountID, &t.Kind, &t.Detail, &t.CreatedAt); e != nil {
			return nil, e
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
func (db *DB) AppendFixedEgressEvent(ctx context.Context, account int64, kind string, detail any) error {
	raw, e := json.Marshal(detail)
	if e != nil {
		return e
	}
	return db.withWriteTx(ctx, func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, `UPDATE fixed_egress_config SET revision=revision WHERE id=1`); e != nil {
			return e
		}
		_, e := tx.ExecContext(ctx, `INSERT INTO fixed_egress_events(id,account_id,kind,detail) SELECT COALESCE(MAX(id),0)+1,$1,$2,$3 FROM fixed_egress_events`, account, kind, string(raw))
		return e
	})
}
func (db *DB) SetFixedEgressResourceHealth(ctx context.Context, id, expectedURL, exit, state string) error {
	return db.withWriteTx(ctx, func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, `UPDATE fixed_egress_config SET revision=revision WHERE id=1`); e != nil {
			return e
		}
		var enc, oldIP string
		if e := tx.QueryRowContext(ctx, `SELECT proxy_ciphertext,observed_exit_ip FROM fixed_egress_resources WHERE id=$1`, id).Scan(&enc, &oldIP); e != nil {
			return e
		}
		current, e := feOpen(enc)
		if e != nil {
			return e
		}
		if current != expectedURL {
			return ErrFixedEgressStale
		}
		var n int
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM fixed_egress_bindings WHERE resource_id=$1`, id).Scan(&n); e != nil {
			return e
		}
		if n > 0 && oldIP != "" && exit != "" && oldIP != exit {
			state = "quarantined"
			exit = oldIP
		}
		if exit == "" {
			exit = oldIP
		}
		_, e = tx.ExecContext(ctx, `UPDATE fixed_egress_resources SET state=$1,observed_exit_ip=$2,cooldown_until=NULL,changed_at=CURRENT_TIMESTAMP WHERE id=$3`, state, exit, id)
		if e == nil && state == "healthy" {
			_, e = tx.ExecContext(ctx, `UPDATE fixed_egress_bindings SET state='ready' WHERE resource_id=$1`, id)
		}
		return e
	})
}
func (db *DB) FixedEgressEnrolledPool(ctx context.Context, id int64) (string, error) {
	var p string
	e := db.conn.QueryRowContext(ctx, `SELECT pool_id FROM fixed_egress_enrollments WHERE account_id=$1`, id).Scan(&p)
	return p, e
}
func (db *DB) CountFixedEgressBindings(ctx context.Context) (int, error) {
	var n int
	e := db.conn.QueryRowContext(ctx, `SELECT count(*) FROM fixed_egress_enrollments`).Scan(&n)
	return n, e
}

var _ = errors.Is
