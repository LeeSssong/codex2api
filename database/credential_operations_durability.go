package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"time"
)

// Candidate ACKs require power-loss durability before upstream activation.
func (db *DB) withCredentialOpsDurableTx(ctx context.Context, fn func(*sql.Tx) error) error {
	return db.withSQLiteWriteLock(ctx, func() (resultErr error) {
		if !db.isSQLite() {
			tx, e := db.conn.BeginTx(ctx, nil)
			if e != nil {
				return e
			}
			defer tx.Rollback()
			if _, e = tx.ExecContext(ctx, "SET LOCAL synchronous_commit=on"); e != nil {
				return e
			}
			if e = fn(tx); e != nil {
				return e
			}
			return tx.Commit()
		}
		conn, e := db.conn.Conn(ctx)
		if e != nil {
			return e
		}
		defer conn.Close()
		var previous int
		if e = conn.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&previous); e != nil {
			return e
		}
		if _, e = conn.ExecContext(ctx, "PRAGMA synchronous=FULL"); e != nil {
			return e
		}
		defer func() {
			resetCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if _, e := conn.ExecContext(resetCtx, fmt.Sprintf("PRAGMA synchronous=%d", previous)); e != nil {
				_ = conn.Raw(func(any) error { return driver.ErrBadConn })
				resultErr = errors.Join(resultErr, e)
			}
		}()
		tx, e := conn.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		if e = fn(tx); e != nil {
			return e
		}
		return tx.Commit()
	})
}
