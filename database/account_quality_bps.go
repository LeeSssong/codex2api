package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/codex2api/accountops"
)

func (db *DB) validateQualityBPSGroupTx(ctx context.Context, tx *sql.Tx, id int64) error {
	query := `SELECT COALESCE(channel,'codex') FROM account_groups WHERE id=$1`
	if !db.isSQLite() {
		query += ` FOR SHARE`
	}
	var channel string
	if err := tx.QueryRowContext(ctx, query, id).Scan(&channel); err != nil {
		return fmt.Errorf("BPS target group unavailable: %w", err)
	}
	if NormalizeAccountGroupChannel(channel) != AccountGroupChannelCodex {
		return fmt.Errorf("BPS target group must use the Codex channel")
	}
	return nil
}

func (db *DB) applyQualityBPSOutcomeTx(ctx context.Context, tx *sql.Tx, p accountops.Plan, outcome, status string, revision int64, baseline qualityRecovery, owned bool) (string, error) {
	var failed, passed int
	if err := tx.QueryRowContext(ctx, `SELECT failure_streak,pass_streak FROM account_quality_plans WHERE id=$1`, p.ID).Scan(&failed, &passed); err != nil {
		return "", err
	}
	switch outcome {
	case "failed":
		failed++
		passed = 0
	case "passed":
		failed = 0
		if p.AutoRestore && passed < accountops.QualityBPSPassThreshold(p.BPS) {
			passed++
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE account_quality_plans SET failure_streak=$1,pass_streak=$2 WHERE id=$3`, failed, passed, p.ID); err != nil {
		return "", err
	}
	var current string
	var allowed int
	err := tx.QueryRowContext(ctx, `SELECT quality_bps,allowed FROM account_codex_paths WHERE account_id=$1 AND upstream='basispoints'`, p.AccountID).Scan(&current, &allowed)
	if errors.Is(err, sql.ErrNoRows) {
		current = ""
		allowed = 1
	} else if err != nil {
		return "", err
	}
	if owned {
		if outcome == "failed" {
			return "already_quarantined", nil
		}
		if outcome == "inconclusive" {
			return outcome, nil
		}
		if !p.AutoRestore {
			return "passed", nil
		}
		if passed < accountops.QualityBPSPassThreshold(p.BPS) {
			return fmt.Sprintf("restore_counted:%d/%d", passed, accountops.QualityBPSPassThreshold(p.BPS)), nil
		}
		if accountops.QualityBPSHoldForUsage(p.BPS, p.UsagePercent, p.HasUsage) {
			return "bps_kept_usage", nil
		}
		if status != "active" || baseline.ControlRevision != revision || current != baseline.BPSApplied || allowed == 0 {
			return "restore_conflict", nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE account_codex_paths SET quality_bps=$1 WHERE account_id=$2 AND upstream='basispoints'`, baseline.BPSPrevious, p.AccountID); err != nil {
			return "", err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM account_quality_recovery WHERE plan_id=$1`, p.ID); err != nil {
			return "", err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE account_quality_plans SET failure_streak=0,pass_streak=0 WHERE id=$1`, p.ID); err != nil {
			return "", err
		}
		return "restored", db.AccountControlOutboxTx(ctx, tx, p.AccountID)
	}
	trigger := accountops.QualityBPSTrigger(p.BPS, failed, p.UsagePercent, p.HasUsage)
	if trigger == "" {
		if outcome == "failed" && p.BPS != nil && p.BPS.FailureThreshold > 0 {
			return fmt.Sprintf("failure_counted:%d/%d", failed, p.BPS.FailureThreshold), nil
		}
		return outcome, nil
	}
	var platform, kind string
	var credentials any
	if err := tx.QueryRowContext(ctx, `SELECT platform,type,credentials FROM accounts WHERE id=$1`, p.AccountID).Scan(&platform, &kind, &credentials); err != nil {
		return "", err
	}
	creds := decodeCredentials(credentials)
	plan, _ := creds["plan_type"].(string)
	authMode, _ := creds["openai_auth_mode"].(string)
	if platform != "openai" || kind != "oauth" || strings.EqualFold(plan, "free") || authMode == "agent_identity" || authMode == "personal_access_token" {
		return "bps_unsupported", nil
	}
	if current != "" {
		return "bps_already_enabled", nil
	}
	if allowed == 0 {
		return "bps_blocked_manual", nil
	}
	raw, err := json.Marshal(p.BPS)
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO account_codex_paths(account_id,upstream,allowed,quality_bps) VALUES($1,'basispoints',1,$2) ON CONFLICT(account_id,upstream) DO UPDATE SET quality_bps=EXCLUDED.quality_bps`, p.AccountID, string(raw)); err != nil {
		return "", err
	}
	baseline = qualityRecovery{Action: "enable_bps", BPSPrevious: current, BPSApplied: string(raw), PlanVersion: p.Version}
	baseline.ControlRevision, err = db.LockAccountControlTx(ctx, tx, p.AccountID)
	if err != nil {
		return "", err
	}
	snapshot, err := json.Marshal(baseline)
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO account_quality_recovery(plan_id,snapshot) VALUES($1,$2)`, p.ID, string(snapshot)); err != nil {
		return "", err
	}
	if err := db.AccountControlOutboxTx(ctx, tx, p.AccountID); err != nil {
		return "", err
	}
	if trigger == "usage" {
		return "bps_enabled_usage", nil
	}
	return "bps_enabled", nil
}
