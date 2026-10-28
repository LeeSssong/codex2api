package database

import (
	"context"
	"database/sql"
	"errors"
	"slices"
)

var ErrIPv6NotEnrolled = errors.New("account is not enrolled in local IPv6 egress")
var ErrIPv6Unavailable = errors.New("local IPv6 egress is disabled or unavailable")

// Serialize configuration, claims and retirement using the existing pool lock.
func (db *DB) ipv6LeaseTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := db.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE ipv6_egress_config SET revision=revision WHERE id=1`); err != nil {
		return err
	}
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
func (db *DB) TrackIPv6EgressLease(ctx context.Context, leaseID string, accountID int64, ip string) error {
	return db.ipv6LeaseTx(ctx, func(tx *sql.Tx) error {
		// Credentials are read under the same transaction/lock as retirement. A stale
		// runtime account or route can never create a lease after removal.
		var raw string
		var proxy string
		if err := tx.QueryRowContext(ctx, `SELECT credentials,COALESCE(proxy_url,'') FROM accounts WHERE id=$1`, accountID).Scan(&raw, &proxy); err != nil {
			return err
		}
		row := AccountRow{Credentials: decodeCredentials(raw)}
		if !row.GetCredentialBool("ipv6_egress_enabled") || proxy != "" {
			return ErrIPv6NotEnrolled
		}
		var currentIP, reason string
		if err := tx.QueryRowContext(ctx, `SELECT ip,reason FROM ipv6_egress_bindings WHERE account_id=$1`, accountID).Scan(&currentIP, &reason); err != nil {
			return err
		}
		c, err := scanIPv6Config(tx.QueryRowContext(ctx, ipv6ConfigSelect))
		if err != nil {
			return err
		}
		if !c.Enabled || ip != currentIP || reason == "retired" {
			return ErrIPv6Unavailable
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO ipv6_egress_leases(id,account_id,ip) VALUES($1,$2,$3)`, leaseID, accountID, ip)
		return err
	})
}
func (db *DB) RetireIPv6Egress(ctx context.Context, accountID, now int64) error {
	return db.ipv6LeaseTx(ctx, func(tx *sql.Tx) error {
		var ip string
		err := tx.QueryRowContext(ctx, `SELECT ip FROM ipv6_egress_bindings WHERE account_id=$1`, accountID).Scan(&ip)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE ipv6_egress_bindings SET reason='retired' WHERE account_id=$1`, accountID); err != nil {
			return err
		}
		return finishIPv6Retirement(ctx, tx, ip, now)
	})
}
func (db *DB) ReleaseIPv6EgressLease(ctx context.Context, leaseID string, now int64) error {
	return db.ipv6LeaseTx(ctx, func(tx *sql.Tx) error {
		var ip string
		err := tx.QueryRowContext(ctx, `SELECT ip FROM ipv6_egress_leases WHERE id=$1`, leaseID).Scan(&ip)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM ipv6_egress_leases WHERE id=$1`, leaseID); err != nil {
			return err
		}
		return finishIPv6Retirement(ctx, tx, ip, now)
	})
}
func finishIPv6Retirement(ctx context.Context, tx *sql.Tx, ip string, now int64) error {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM ipv6_egress_leases WHERE ip=$1`, ip).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	var reason string
	err := tx.QueryRowContext(ctx, `SELECT reason FROM ipv6_egress_bindings WHERE ip=$1`, ip).Scan(&reason)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && reason != "retired" {
		return nil
	}
	c, err := scanIPv6Config(tx.QueryRowContext(ctx, ipv6ConfigSelect))
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM ipv6_egress_bindings WHERE ip=$1 AND reason='retired'`, ip); err != nil {
		return err
	}
	return extendIPv6Cooldown(ctx, tx, ip, now+int64(c.CooldownSeconds))
}
func (db *DB) CurrentIPv6EgressIP(ctx context.Context, id int64) (string, error) {
	var ip string
	err := db.conn.QueryRowContext(ctx, `SELECT ip FROM ipv6_egress_bindings WHERE account_id=$1 AND reason<>'retired'`, id).Scan(&ip)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return ip, err
}

// ReserveIPv6BootstrapLease reserves an exclusive source for OAuth before an
// account identity exists. It shares the durable claim/retirement lock and is
// released into cooldown; no placeholder account or proxy URL is created.
func (db *DB) ReserveIPv6BootstrapLease(ctx context.Context, leaseID string, localIPs []string, now int64) (string, error) {
	var ip string
	err := db.ipv6LeaseTx(ctx, func(tx *sql.Tx) error {
		c, err := scanIPv6Config(tx.QueryRowContext(ctx, ipv6ConfigSelect))
		if err != nil {
			return err
		}
		if !c.Enabled {
			return ErrIPv6Unavailable
		}
		for _, candidate := range localIPs {
			if len(c.SourceIPs) > 0 && !slices.Contains(c.SourceIPs, candidate) {
				continue
			}
			var used int
			if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM ipv6_egress_bindings WHERE ip=$1)+(SELECT COUNT(*) FROM ipv6_egress_leases WHERE ip=$1)+(SELECT COUNT(*) FROM ipv6_egress_cooldowns WHERE ip=$1 AND until_time>$2)`, candidate, now).Scan(&used); err != nil {
				return err
			}
			if used != 0 {
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO ipv6_egress_leases(id,account_id,ip) VALUES($1,0,$2)`, leaseID, candidate); err != nil {
				return err
			}
			ip = candidate
			return nil
		}
		return ErrIPv6PoolExhausted
	})
	return ip, err
}
