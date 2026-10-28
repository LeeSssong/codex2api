package database

import (
	"context"
	"database/sql"
	"errors"
	"slices"
)

var ErrIPv6SelectionConflict = errors.New("IPv6 selection changed or address is occupied or cooling down")
var ErrIPv6InvalidSource = errors.New("IPv6 address must be canonical and available in the local pool")

// AssignIPv6Egress switches a saved member's binding under the same lock as
// automatic claims, lease acquisition and retirement. Old leases remain durable
// reservations after the binding moves; their final release restarts cooldown.
func (db *DB) AssignIPv6Egress(ctx context.Context, accountID int64, localIPs []string, ip, expectedIP string, now int64) (IPv6Binding, error) {
	var b IPv6Binding
	err := db.ipv6LeaseTx(ctx, func(tx *sql.Tx) error {
		var raw, proxy string
		if err := tx.QueryRowContext(ctx, `SELECT credentials,COALESCE(proxy_url,'') FROM accounts WHERE id=$1`, accountID).Scan(&raw, &proxy); err != nil {
			return err
		}
		row := AccountRow{Credentials: decodeCredentials(raw)}
		if !row.GetCredentialBool("ipv6_egress_enabled") || proxy != "" {
			return ErrIPv6NotEnrolled
		}
		cfg, err := scanIPv6Config(tx.QueryRowContext(ctx, ipv6ConfigSelect))
		if err != nil {
			return err
		}
		if !cfg.Enabled {
			return ErrIPv6Unavailable
		}
		err = tx.QueryRowContext(ctx, `SELECT account_id,ip,changed_at,reason,rotations FROM ipv6_egress_bindings WHERE account_id=$1`, accountID).Scan(&b.AccountID, &b.IP, &b.ChangedAt, &b.Reason, &b.Rotations)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if b.Reason == "retired" || b.IP != expectedIP {
			return ErrIPv6SelectionConflict
		}
		if !slices.Contains(localIPs, ip) || (len(cfg.SourceIPs) > 0 && !slices.Contains(cfg.SourceIPs, ip)) {
			return ErrIPv6InvalidSource
		}
		if b.IP == ip {
			return nil
		}
		var used int
		if err = tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM ipv6_egress_bindings WHERE ip=$1)+(SELECT COUNT(*) FROM ipv6_egress_leases WHERE ip=$1)+(SELECT COUNT(*) FROM ipv6_egress_cooldowns WHERE ip=$1 AND until_time>$2)`, ip, now).Scan(&used); err != nil {
			return err
		}
		if used > 0 {
			return ErrIPv6SelectionConflict
		}
		if b.IP != "" {
			if err = extendIPv6Cooldown(ctx, tx, b.IP, now+int64(cfg.CooldownSeconds)); err != nil {
				return err
			}
		}
		b = IPv6Binding{AccountID: accountID, IP: ip, ChangedAt: now, Reason: "manual", Rotations: b.Rotations + 1}
		_, err = tx.ExecContext(ctx, `INSERT INTO ipv6_egress_bindings(account_id,ip,changed_at,reason,rotations) VALUES($1,$2,$3,$4,$5) ON CONFLICT(account_id) DO UPDATE SET ip=$2,changed_at=$3,reason=$4,rotations=$5`, b.AccountID, b.IP, b.ChangedAt, b.Reason, b.Rotations)
		return err
	})
	return b, err
}

func extendIPv6Cooldown(ctx context.Context, tx *sql.Tx, ip string, until int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO ipv6_egress_cooldowns(ip,until_time) VALUES($1,$2) ON CONFLICT(ip) DO UPDATE SET until_time=CASE WHEN ipv6_egress_cooldowns.until_time>excluded.until_time THEN ipv6_egress_cooldowns.until_time ELSE excluded.until_time END`, ip, until)
	return err
}
