package database

import (
	"context"
	"database/sql"
	"time"
)

func (db *DB) ClaimFixedEgressLease(ctx context.Context, id, resource string) error {
	return db.withWriteTx(ctx, func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, `UPDATE fixed_egress_config SET revision=revision WHERE id=1`); e != nil {
			return e
		}
		var st string
		if e := tx.QueryRowContext(ctx, `SELECT state FROM fixed_egress_resources WHERE id=$1`, resource).Scan(&st); e != nil {
			return e
		}
		if st != "bound" && st != "healthy" {
			return ErrFixedEgressUnavailable
		}
		_, e := tx.ExecContext(ctx, `INSERT INTO fixed_egress_leases(id,resource_id,expires_at) VALUES($1,$2,$3)`, id, resource, time.Now().UTC().Add(90*time.Second))
		return e
	})
}
func (db *DB) RenewFixedEgressLease(ctx context.Context, id string) error {
	_, e := db.conn.ExecContext(ctx, `UPDATE fixed_egress_leases SET expires_at=$1 WHERE id=$2`, time.Now().UTC().Add(90*time.Second), id)
	return e
}
func (db *DB) ReleaseFixedEgressLease(ctx context.Context, id string) error {
	_, e := db.conn.ExecContext(ctx, `DELETE FROM fixed_egress_leases WHERE id=$1`, id)
	return e
}
