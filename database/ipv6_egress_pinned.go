package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"
)

const PinnedIPv6CredentialKey = "ipv6_egress_pinned_ip"
const pinnedIPv6Reason = "pinned"

func pinnedIPv6LeaseID(taskID int64) string { return fmt.Sprintf("credential-ipv6:%d", taskID) }

func lockIPv6Pool(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `UPDATE ipv6_egress_config SET revision=revision WHERE id=1`)
	return err
}

func pinnedIPv6Allowed(ctx context.Context, q pinnedIPv6Query, ip string) error {
	cfg, err := scanIPv6Config(q.QueryRowContext(ctx, ipv6ConfigSelect))
	if err != nil {
		return err
	}
	if !cfg.Enabled || (len(cfg.SourceIPs) > 0 && !slices.Contains(cfg.SourceIPs, ip)) {
		return ErrIPv6Unavailable
	}
	return nil
}

func reservePinnedIPv6(ctx context.Context, tx *sql.Tx, taskID int64, ip string) error {
	addr, err := netip.ParseAddr(ip)
	if err != nil || !addr.Is6() || addr.Is4In6() || !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.Zone() != "" || ip != addr.String() {
		return ErrIPv6InvalidSource
	}
	if err := lockIPv6Pool(ctx, tx); err != nil {
		return err
	}
	if err := pinnedIPv6Allowed(ctx, tx, ip); err != nil {
		return err
	}
	var used int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM ipv6_egress_bindings WHERE ip=$1)+(SELECT COUNT(*) FROM ipv6_egress_leases WHERE ip=$1)+(SELECT COUNT(*) FROM ipv6_egress_cooldowns WHERE ip=$1 AND until_time>$2)`, ip, time.Now().Unix()).Scan(&used); err != nil {
		return err
	}
	if used != 0 {
		return ErrIPv6SelectionConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO ipv6_egress_leases(id,account_id,ip) VALUES($1,0,$2)`, pinnedIPv6LeaseID(taskID), ip)
	return err
}

type pinnedIPv6Query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func checkPinnedIPv6Reservation(ctx context.Context, q pinnedIPv6Query, taskID int64, ip string) error {
	if err := pinnedIPv6Allowed(ctx, q, ip); err != nil {
		return err
	}
	var reserved string
	err := q.QueryRowContext(ctx, `SELECT ip FROM ipv6_egress_leases WHERE id=$1 AND account_id=0`, pinnedIPv6LeaseID(taskID)).Scan(&reserved)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && reserved != ip) {
		return ErrIPv6Unavailable
	}
	return err
}

func (db *DB) CheckPinnedIPv6Reservation(ctx context.Context, taskID int64, ip string) error {
	return checkPinnedIPv6Reservation(ctx, db.conn, taskID, ip)
}

func (db *DB) checkPinnedIPv6ImportIdentity(ctx context.Context, tx *sql.Tx, cfg CredentialOpsLoginConfigRow) error {
	if !db.isSQLite() {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "credential-ipv6:"+strings.ToLower(cfg.LoginEmail)); err != nil {
			return err
		}
	}
	accountEmail, accountPin := "json_extract(credentials,'$.email')", "json_extract(credentials,'$.ipv6_egress_pinned_ip')"
	taskEmail, taskPin := "json_extract(config_json,'$.LoginEmail')", "json_extract(config_json,'$.PinnedIPv6')"
	if !db.isSQLite() {
		accountEmail, accountPin = "credentials->>'email'", "credentials->>'ipv6_egress_pinned_ip'"
		taskEmail, taskPin = "config_json::jsonb->>'LoginEmail'", "config_json::jsonb->>'PinnedIPv6'"
	}
	var conflict bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM accounts WHERE status<>'deleted' AND LOWER(TRIM(`+accountEmail+`))=LOWER($1) AND (COALESCE(`+accountPin+`,'')<>'' OR ($2<>'' AND platform='openai' AND type='oauth'))) OR EXISTS(SELECT 1 FROM credential_operations_tasks JOIN credential_operations_task_inputs ON task_id=id WHERE account_id=0 AND status IN ('queued','running') AND LOWER(TRIM(`+taskEmail+`))=LOWER($1) AND ($2<>'' OR COALESCE(`+taskPin+`,'')<>''))`, cfg.LoginEmail, cfg.PinnedIPv6).Scan(&conflict)
	if err == nil && conflict {
		return errors.New("strict IPv6 import conflicts with an existing account or in-progress import")
	}
	return err
}

func (db *DB) ReleasePinnedIPv6Import(ctx context.Context, taskID int64, attempt int) error {
	return db.withSQLiteWriteLock(ctx, func() error {
		tx, err := db.conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var status string
		var current int
		if err := tx.QueryRowContext(ctx, db.credentialOpsLockQuery(`SELECT status,attempt FROM credential_operations_tasks WHERE id=$1`), taskID).Scan(&status, &current); err != nil {
			return err
		}
		if current != attempt || (status != "cancelled" && status != "failed" && status != "succeeded") {
			return nil
		}
		var reserved bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ipv6_egress_leases WHERE id=$1 AND account_id=0)`, pinnedIPv6LeaseID(taskID)).Scan(&reserved); err != nil || !reserved {
			return err
		}
		if err := lockIPv6Pool(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM ipv6_egress_leases WHERE id=$1 AND account_id=0`, pinnedIPv6LeaseID(taskID)); err != nil {
			return err
		}
		return tx.Commit()
	})
}
