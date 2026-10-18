package database

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func (db *DB) ReserveFixedEgressTask(ctx context.Context, t FixedEgressTask) error {
	return db.withWriteTx(ctx, func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, `UPDATE fixed_egress_config SET revision=revision WHERE id=1`); e != nil {
			return e
		}
		var start, end, cap, interval int
		var enabled bool
		var base string
		if e := tx.QueryRowContext(ctx, `SELECT port_start,port_end,capacity,refill_interval_seconds,enabled,base_url FROM fixed_egress_providers WHERE id=$1`, t.ProviderID).Scan(&start, &end, &cap, &interval, &enabled, &base); e != nil {
			return e
		}
		if !enabled || t.Port < start || t.Port > end {
			return ErrFixedEgressUnavailable
		}
		var n int
		if e := tx.QueryRowContext(ctx, `SELECT count(*) FROM fixed_egress_tasks t JOIN fixed_egress_providers p ON p.id=t.provider_id WHERE p.base_url=$1 AND t.port=$2 AND t.state IN ('unknown','finalized','stop_unknown')`, base, t.Port).Scan(&n); e != nil {
			return e
		}
		if n > 0 {
			return ErrFixedEgressUnavailable
		}
		if e := tx.QueryRowContext(ctx, `SELECT count(*) FROM fixed_egress_resources WHERE provider_id=$1 AND state<>'retired'`, t.ProviderID).Scan(&n); e != nil {
			return e
		}
		if n >= cap {
			return ErrFixedEgressUnavailable
		}
		if e := tx.QueryRowContext(ctx, `SELECT count(*) FROM fixed_egress_tasks WHERE provider_id=$1 AND created_at>$2 AND state<>'stopped'`, t.ProviderID, time.Now().UTC().Add(-time.Duration(interval)*time.Second)).Scan(&n); e != nil {
			return e
		}
		if n > 0 {
			return ErrFixedEgressUnavailable
		}
		_, e := tx.ExecContext(ctx, `INSERT INTO fixed_egress_tasks(id,provider_id,port,state,result,resource_id) VALUES($1,$2,$3,'unknown','',$4)`, t.ID, t.ProviderID, t.Port, t.ResourceID)
		return e
	})
}
func (db *DB) FinalizeFixedEgressTask(ctx context.Context, id, resource string, port int, result string) error {
	return db.withWriteTx(ctx, func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, `UPDATE fixed_egress_config SET revision=revision WHERE id=1`); e != nil {
			return e
		}
		var provider, state, rprovider string
		var reserved, rport int
		if e := tx.QueryRowContext(ctx, `SELECT provider_id,port,state FROM fixed_egress_tasks WHERE id=$1`, id).Scan(&provider, &reserved, &state); e != nil {
			return e
		}
		if reserved != port || state != "unknown" {
			return ErrFixedEgressStale
		}
		if e := tx.QueryRowContext(ctx, `SELECT COALESCE(provider_id,''),provider_port FROM fixed_egress_resources WHERE id=$1`, resource).Scan(&rprovider, &rport); e != nil {
			return e
		}
		if provider != rprovider || port != rport {
			return ErrFixedEgressStale
		}
		if _, e := tx.ExecContext(ctx, `UPDATE fixed_egress_tasks SET state='finalized',result=$1,resource_id=$2,updated_at=CURRENT_TIMESTAMP WHERE id=$3`, result, resource, id); e != nil {
			return e
		}
		_, e := tx.ExecContext(ctx, `UPDATE fixed_egress_resources SET owned=TRUE,provider_task_id=$1 WHERE id=$2`, id, resource)
		return e
	})
}
func (db *DB) RetireFixedEgressResource(ctx context.Context, id string, remote bool) error {
	return db.withWriteTx(ctx, func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, `UPDATE fixed_egress_config SET revision=revision WHERE id=1`); e != nil {
			return e
		}
		var n int
		if e := tx.QueryRowContext(ctx, `SELECT count(*) FROM fixed_egress_bindings WHERE resource_id=$1`, id).Scan(&n); e != nil {
			return e
		}
		if n > 0 {
			return ErrFixedEgressUnavailable
		}
		if e := tx.QueryRowContext(ctx, `SELECT count(*) FROM fixed_egress_leases WHERE resource_id=$1`, id).Scan(&n); e != nil {
			return e
		}
		if n > 0 {
			return ErrFixedEgressUnavailable
		}
		if remote {
			var owned bool
			var task string
			if e := tx.QueryRowContext(ctx, `SELECT owned,provider_task_id FROM fixed_egress_resources WHERE id=$1`, id).Scan(&owned, &task); e != nil {
				return e
			}
			if !owned || task == "" {
				return ErrFixedEgressUnavailable
			}
			var state string
			if e := tx.QueryRowContext(ctx, `SELECT state FROM fixed_egress_tasks WHERE id=$1`, task).Scan(&state); e != nil {
				return e
			}
			if state != "finalized" && state != "stop_unknown" {
				return ErrFixedEgressUnavailable
			}
			if _, e := tx.ExecContext(ctx, `UPDATE fixed_egress_tasks SET state='stop_unknown',updated_at=CURRENT_TIMESTAMP WHERE id=$1`, task); e != nil {
				return e
			}
		}
		r, e := tx.ExecContext(ctx, `UPDATE fixed_egress_resources SET state='retired',changed_at=CURRENT_TIMESTAMP WHERE id=$1`, id)
		if e != nil {
			return e
		}
		n64, e := r.RowsAffected()
		if e != nil {
			return e
		}
		if n64 != 1 {
			return sql.ErrNoRows
		}
		return nil
	})
}
func (db *DB) CompleteFixedEgressStop(ctx context.Context, resource string) error {
	r, e := db.conn.ExecContext(ctx, `UPDATE fixed_egress_tasks SET state='stopped',result='stopped',updated_at=CURRENT_TIMESTAMP WHERE resource_id=$1 AND state='stop_unknown'`, resource)
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return errors.New("stop reconciliation required")
	}
	return nil
}
