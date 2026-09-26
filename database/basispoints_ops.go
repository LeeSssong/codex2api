package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/codex2api/accountops"
	"strconv"
	"strings"
	"time"
)

func basispointsOpsStorageKind(e accountops.AccountOpsEvent) string {
	if accountops.IsBasispointsCategory(e.Kind) {
		return "basispoints:" + e.Kind + "@" + strconv.FormatInt(e.CredentialGeneration, 10)
	}
	return e.Kind
}
func decodeBasispointsOpsKind(e *accountops.AccountOpsEvent) error {
	if !strings.HasPrefix(e.Kind, "basispoints:") {
		return nil
	}
	raw := strings.TrimPrefix(e.Kind, "basispoints:")
	parts := strings.Split(raw, "@")
	if len(parts) != 2 || !accountops.IsBasispointsCategory(parts[0]) {
		return fmt.Errorf("invalid stored BPS event")
	}
	generation, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || generation < 0 {
		return fmt.Errorf("invalid stored BPS generation")
	}
	e.Kind = parts[0]
	e.CredentialGeneration = generation
	return nil
}
func (r *AccountOpsRepository) basispointsOpsCurrent(ctx context.Context, tx *sql.Tx, e accountops.AccountOpsEvent, lock bool) (bool, error) {
	if !accountops.ValidBasispointsEvent(e) {
		return false, nil
	}
	var enabled string
	err := tx.QueryRowContext(ctx, "SELECT value FROM account_ops_settings WHERE key='module_enabled'").Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil || enabled != "true" {
		return false, err
	}
	if e.AccountID == 0 {
		return true, nil
	}
	query := "SELECT credential_generation FROM accounts WHERE id=$1 AND deleted_at IS NULL AND status<>'deleted'"
	if lock && !r.db.isSQLite() {
		query += " FOR UPDATE"
	}
	var generation int64
	err = tx.QueryRowContext(ctx, query, e.AccountID).Scan(&generation)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil && generation == e.CredentialGeneration, err
}
func (r *AccountOpsRepository) recordBasispoints(ctx context.Context, e accountops.AccountOpsEvent) error {
	if !accountops.ValidBasispointsEvent(e) {
		return fmt.Errorf("invalid BPS event")
	}
	return r.db.withWriteTx(ctx, func(tx *sql.Tx) error {
		current, err := r.basispointsOpsCurrent(ctx, tx, e, true)
		if err != nil || !current {
			return err
		}
		e.Signal = e.Kind
		e.AccountName = ""
		e.HTTPStatus = 0
		if e.Kind == "auto_403_disabled" {
			e.HTTPStatus = 403
		}
		_, err = tx.ExecContext(ctx, accountOpsRecordSQL, e.AccountID, basispointsOpsStorageKind(e), e.AccountName, e.Signal, e.HTTPStatus, r.db.timeArg(time.Now().UTC()))
		return err
	})
}

// Recheck generation, module and lease immediately before bounded delivery.
// Release database locks before network I/O; the notification carries the
// original generation and never changes account state.
func (r *AccountOpsRepository) WithCurrentBasispointsEvent(ctx context.Context, e *accountops.AccountOpsEvent, send func(context.Context) error) (bool, error) {
	sent := false
	if e == nil || send == nil {
		return false, nil
	}
	err := r.db.withWriteTx(ctx, func(tx *sql.Tx) error {
		current, err := r.basispointsOpsCurrent(ctx, tx, *e, true)
		if err != nil || !current {
			return err
		}
		var lease, state string
		err = tx.QueryRowContext(ctx, "SELECT lease,state FROM account_ops_alerts WHERE account_id=$1 AND kind=$2", e.AccountID, basispointsOpsStorageKind(*e)).Scan(&lease, &state)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if lease == "" || lease != e.Lease || state != "sending" {
			return nil
		}
		sent = true
		return nil
	})
	if err != nil || !sent {
		return false, err
	}
	err = send(ctx)
	return err == nil, err
}
