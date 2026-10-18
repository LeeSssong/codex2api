package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// CodexProbeResult contains safe test evidence, never request bodies or credentials.
type CodexProbeResult struct {
	AccountID      int64     `json:"account_id"`
	Model          string    `json:"model"`
	Upstream       string    `json:"upstream"`
	Level          string    `json:"level"`
	Outcome        string    `json:"outcome"`
	Capability     string    `json:"capability"`
	BasicOutcome   string    `json:"basic_outcome"`
	ToolsOutcome   string    `json:"tools_outcome"`
	HTTPStatus     int       `json:"http_status"`
	ReportedStatus int       `json:"reported_status"`
	ErrorCode      string    `json:"error_code,omitempty"`
	Message        string    `json:"message"`
	StartedAt      time.Time `json:"started_at"`
	FinishedAt     time.Time `json:"finished_at"`
	DurationMS     int64     `json:"duration_ms"`
	Attempts       int       `json:"attempts"`
}

const codexProbesSchema = `CREATE TABLE IF NOT EXISTS account_codex_probes (
 account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
 upstream TEXT NOT NULL, model TEXT NOT NULL, level TEXT NOT NULL,
 credential_generation BIGINT NOT NULL, started_at BIGINT NOT NULL,
 result_json TEXT NOT NULL, PRIMARY KEY(account_id,upstream,model,level))`

// SaveCodexProbeResult atomically fences both latest health and capability evidence.
// Transient outcomes pass nil evidence and leave prior successful evidence intact.
func (db *DB) SaveCodexProbeResult(ctx context.Context, generation int64, result CodexProbeResult, evidence *CodexCapability) (bool, error) {
	if result.AccountID <= 0 || result.Upstream != CodexPathBasispoints || strings.TrimSpace(result.Model) == "" || (result.Level != "basic" && result.Level != "tools") || result.StartedAt.IsZero() {
		return false, fmt.Errorf("invalid Codex probe result")
	}
	result.Model = strings.ToLower(strings.TrimSpace(result.Model))
	raw, err := json.Marshal(result)
	if err != nil {
		return false, err
	}
	applied := false
	err = db.withWriteTx(ctx, func(tx *sql.Tx) error {
		query := `SELECT credential_generation FROM accounts WHERE id=$1 AND deleted_at IS NULL`
		if !db.isSQLite() {
			query += ` FOR UPDATE`
		}
		var current int64
		if err := tx.QueryRowContext(ctx, query, result.AccountID).Scan(&current); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		if current != generation {
			return nil
		}
		var resetAt int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(observed_at),0) FROM account_codex_capabilities WHERE account_id=$1 AND upstream=$2 AND source='admin_reset' AND model IN ('',$3)`, result.AccountID, result.Upstream, result.Model).Scan(&resetAt); err != nil {
			return err
		}
		if resetAt >= result.StartedAt.UnixNano() {
			return nil
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO account_codex_probes(account_id,upstream,model,level,credential_generation,started_at,result_json) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(account_id,upstream,model,level) DO UPDATE SET credential_generation=excluded.credential_generation,started_at=excluded.started_at,result_json=excluded.result_json WHERE account_codex_probes.credential_generation <> excluded.credential_generation OR account_codex_probes.started_at < excluded.started_at`, result.AccountID, result.Upstream, result.Model, result.Level, generation, result.StartedAt.UnixNano(), string(raw))
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		applied = n > 0
		if !applied || evidence == nil {
			return nil
		}
		if evidence.Upstream != result.Upstream || !strings.EqualFold(evidence.Model, result.Model) || (evidence.Capability != CapabilitySupported && evidence.Capability != CapabilityUnsupported) {
			return fmt.Errorf("invalid probe evidence")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO account_codex_capabilities(account_id,upstream,model,capability,source,reason,observed_at,credential_generation) SELECT $1,$2,$3,$4,$5,$6,$7,$8 WHERE NOT EXISTS (SELECT 1 FROM account_codex_capabilities WHERE account_id=$1 AND upstream=$2 AND model='' AND source='admin_reset' AND observed_at >= $7) ON CONFLICT(account_id,upstream,model) DO UPDATE SET capability=excluded.capability,source=excluded.source,reason=excluded.reason,observed_at=excluded.observed_at,credential_generation=excluded.credential_generation WHERE account_codex_capabilities.observed_at < excluded.observed_at`, result.AccountID, result.Upstream, result.Model, evidence.Capability, evidence.Source, evidence.Reason, evidence.ObservedAt, generation)
		return err
	})
	return applied, err
}

func (db *DB) GetCodexCapabilityProbeResults(ctx context.Context, accountID int64) ([]CodexProbeResult, error) {
	rows, err := db.conn.QueryContext(ctx, `SELECT p.result_json FROM account_codex_probes p JOIN accounts a ON a.id=p.account_id AND a.credential_generation=p.credential_generation WHERE p.account_id=$1 AND a.deleted_at IS NULL ORDER BY p.model,p.level`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := []CodexProbeResult{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var result CodexProbeResult
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, rows.Err()
}
