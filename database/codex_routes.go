package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const (
	CodexPathNative                 = "codex"
	CodexPathBasispoints            = "basispoints"
	CodexRouteInherit               = "inherit"
	CodexRouteNativeOnly            = "codex_only"
	CodexRouteBasispointsOnly       = "basispoints_only"
	CodexRouteNativePrefer          = "codex_prefer"
	CodexRouteBasispointsPrefer     = "basispoints_prefer"
	CodexRouteBasispointsModelsOnly = "basispoints_models_only"
	CapabilityUnknown               = "unknown"
	CapabilitySupported             = "supported"
	CapabilityUnsupported           = "unsupported"
)

func ValidCodexPath(path string) bool { return path == CodexPathNative || path == CodexPathBasispoints }

func ValidCodexRoutePolicy(policy string) bool {
	switch policy {
	case "", CodexRouteInherit, CodexRouteNativeOnly, CodexRouteBasispointsOnly, CodexRouteNativePrefer, CodexRouteBasispointsPrefer, CodexRouteBasispointsModelsOnly:
		return true
	}
	return false
}

func ValidCodexCapabilityFilter(filter string) bool {
	switch filter {
	case "", "any", "supported", "dual_supported", "codex_supported", "basispoints_supported":
		return true
	}
	return false
}

func (l APIKeyLimits) ValidateCodexRouting() error {
	if !ValidCodexRoutePolicy(l.CodexRoutePolicy) {
		return fmt.Errorf("invalid limits.codex_route_policy")
	}
	if !ValidCodexCapabilityFilter(l.CodexCapabilityFilter) {
		return fmt.Errorf("invalid limits.codex_capability_filter")
	}
	if (l.CodexRoutePolicy != "" && l.CodexRoutePolicy != CodexRouteInherit || l.CodexCapabilityFilter != "" && l.CodexCapabilityFilter != "any") && l.ResolveUpstreamChannel() != UpstreamChannelAuto && l.ResolveUpstreamChannel() != UpstreamChannelCodex {
		return fmt.Errorf("Codex routing requires the auto or codex account channel")
	}
	return nil
}

// CodexCapability records evidence, never administrative permission or health.
// Empty Model is reserved for explicit account/path-wide evidence.
type CodexCapability struct {
	CredentialGeneration int64  `json:"-"`
	Upstream             string `json:"upstream"`
	Model                string `json:"model"`
	Capability           string `json:"capability"`
	Source               string `json:"source"`
	Reason               string `json:"reason"`
	ObservedAt           int64  `json:"observed_at"`
}

type CodexPathConfig struct {
	Upstream string `json:"upstream"`
	Allowed  bool   `json:"allowed"`
}

// Integer timestamps have identical ordering on SQLite and PostgreSQL.
func (db *DB) ensureCodexRoutesSchema(ctx context.Context) error {
	for _, query := range []string{
		`CREATE TABLE IF NOT EXISTS account_codex_paths (account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE, upstream TEXT NOT NULL, allowed INTEGER NOT NULL DEFAULT 1, PRIMARY KEY(account_id, upstream))`,
		`CREATE TABLE IF NOT EXISTS account_codex_capabilities (account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE, upstream TEXT NOT NULL, model TEXT NOT NULL DEFAULT '', capability TEXT NOT NULL DEFAULT 'unknown', source TEXT NOT NULL DEFAULT '', reason TEXT NOT NULL DEFAULT '', observed_at BIGINT NOT NULL DEFAULT 0, PRIMARY KEY(account_id, upstream, model))`,
		`CREATE INDEX IF NOT EXISTS idx_codex_capability_filter ON account_codex_capabilities(upstream, model, capability, account_id)`,
		codexProbesSchema,
	} {
		if _, err := db.conn.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("initialize Codex routes: %w", err)
		}
	}
	if db.isSQLite() {
		return db.ensureSQLiteColumn(ctx, "account_codex_capabilities", "credential_generation", "BIGINT NOT NULL DEFAULT 0")
	}
	_, err := db.conn.ExecContext(ctx, `ALTER TABLE account_codex_capabilities ADD COLUMN IF NOT EXISTS credential_generation BIGINT NOT NULL DEFAULT 0`)
	return err
}

func (db *DB) GetCodexRoutes(ctx context.Context, id int64) ([]CodexPathConfig, []CodexCapability, error) {
	configs := []CodexPathConfig{}
	facts := []CodexCapability{}
	rows, err := db.conn.QueryContext(ctx, `SELECT upstream, allowed FROM account_codex_paths WHERE account_id=$1`, id)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var c CodexPathConfig
		var allowed int
		if err := rows.Scan(&c.Upstream, &allowed); err != nil {
			rows.Close()
			return nil, nil, err
		}
		c.Allowed = allowed != 0
		configs = append(configs, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	rows, err = db.conn.QueryContext(ctx, `SELECT c.upstream, c.model, c.capability, c.source, c.reason, c.observed_at, c.credential_generation FROM account_codex_capabilities c JOIN accounts a ON a.id=c.account_id WHERE c.account_id=$1 AND (c.credential_generation=0 OR c.credential_generation=a.credential_generation)`, id)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var f CodexCapability
		if err := rows.Scan(&f.Upstream, &f.Model, &f.Capability, &f.Source, &f.Reason, &f.ObservedAt, &f.CredentialGeneration); err != nil {
			return nil, nil, err
		}
		facts = append(facts, f)
	}
	return configs, facts, rows.Err()
}

func (db *DB) SetCodexPathAllowed(ctx context.Context, id int64, path string, allowed bool) error {
	if !ValidCodexPath(path) {
		return fmt.Errorf("invalid Codex path")
	}
	value := 0
	if allowed {
		value = 1
	}
	return db.withSQLiteWriteLock(ctx, func() error {
		_, err := db.conn.ExecContext(ctx, `INSERT INTO account_codex_paths(account_id,upstream,allowed) VALUES($1,$2,$3) ON CONFLICT(account_id,upstream) DO UPDATE SET allowed=excluded.allowed`, id, path, value)
		return err
	})
}

func (db *DB) ObserveCodexCapability(ctx context.Context, id int64, f CodexCapability) (bool, error) {
	if !ValidCodexPath(f.Upstream) || (f.Capability != CapabilityUnknown && f.Capability != CapabilitySupported && f.Capability != CapabilityUnsupported) || f.ObservedAt <= 0 {
		return false, fmt.Errorf("invalid Codex capability observation")
	}
	f.Model = strings.ToLower(strings.TrimSpace(f.Model))
	var applied bool
	err := db.withWriteTx(ctx, func(tx *sql.Tx) error {
		if err := db.lockCodexRoutesAccount(ctx, tx, id); err != nil {
			return err
		}
		// Check under the account lock without mixing the accounts INTEGER ID
		// and capability BIGINT ID in a shared PostgreSQL parameter inference.
		if f.CredentialGeneration != 0 {
			var generation int64
			if err := tx.QueryRowContext(ctx, `SELECT credential_generation FROM accounts WHERE id=$1`, id).Scan(&generation); err != nil {
				return err
			}
			if generation != f.CredentialGeneration {
				return nil
			}
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO account_codex_capabilities(account_id,upstream,model,capability,source,reason,observed_at,credential_generation) SELECT $1,$2,$3,$4,$5,$6,$7,$8 WHERE NOT EXISTS (SELECT 1 FROM account_codex_capabilities WHERE account_id=$1 AND upstream=$2 AND model='' AND source='admin_reset' AND observed_at >= $7) ON CONFLICT(account_id,upstream,model) DO UPDATE SET capability=excluded.capability,source=excluded.source,reason=excluded.reason,observed_at=excluded.observed_at,credential_generation=excluded.credential_generation WHERE account_codex_capabilities.observed_at < excluded.observed_at`, id, f.Upstream, f.Model, f.Capability, f.Source, f.Reason, f.ObservedAt, f.CredentialGeneration)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		applied = n > 0
		return err
	})
	return applied, err
}

// Reset retains a timestamp fence so an already-running old failure cannot undo it.
func (db *DB) ResetCodexCapabilities(ctx context.Context, id int64, path string, now time.Time) error {
	if !ValidCodexPath(path) {
		return fmt.Errorf("invalid Codex path")
	}
	return db.withWriteTx(ctx, func(tx *sql.Tx) error {
		if err := db.lockCodexRoutesAccount(ctx, tx, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE account_codex_capabilities SET capability='unknown',source='admin_reset',reason='',observed_at=$3 WHERE account_id=$1 AND upstream=$2`, id, path, now.UnixNano())
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO account_codex_capabilities(account_id,upstream,model,capability,source,reason,observed_at) VALUES($1,$2,'','unknown','admin_reset','',$3) ON CONFLICT(account_id,upstream,model) DO UPDATE SET capability='unknown',source='admin_reset',reason='',observed_at=excluded.observed_at`, id, path, now.UnixNano())
		return err
	})
}

type CodexRouteRecords struct {
	Paths []CodexPathConfig
	Facts []CodexCapability
}

// One bounded projection serves filtering before pagination, without credential reads.
func (db *DB) ListCodexRouteRecords(ctx context.Context) (map[int64]*CodexRouteRecords, error) {
	out := make(map[int64]*CodexRouteRecords)
	get := func(id int64) *CodexRouteRecords {
		if out[id] == nil {
			out[id] = &CodexRouteRecords{}
		}
		return out[id]
	}
	rows, err := db.conn.QueryContext(ctx, `SELECT account_id,upstream,allowed FROM account_codex_paths`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var c CodexPathConfig
		var allowed int
		if err := rows.Scan(&id, &c.Upstream, &allowed); err != nil {
			rows.Close()
			return nil, err
		}
		c.Allowed = allowed != 0
		get(id).Paths = append(get(id).Paths, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = db.conn.QueryContext(ctx, `SELECT c.account_id,c.upstream,c.model,c.capability,c.source,c.reason,c.observed_at,c.credential_generation FROM account_codex_capabilities c JOIN accounts a ON a.id=c.account_id WHERE c.credential_generation=0 OR c.credential_generation=a.credential_generation`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var f CodexCapability
		if err := rows.Scan(&id, &f.Upstream, &f.Model, &f.Capability, &f.Source, &f.Reason, &f.ObservedAt, &f.CredentialGeneration); err != nil {
			return nil, err
		}
		get(id).Facts = append(get(id).Facts, f)
	}
	return out, rows.Err()
}

// PostgreSQL serializes resets and observations per account across replicas.
// SQLite's write transaction already provides this serialization.
func (db *DB) lockCodexRoutesAccount(ctx context.Context, tx *sql.Tx, id int64) error {
	if db.Driver() != "postgres" {
		return nil
	}
	var found int64
	return tx.QueryRowContext(ctx, `SELECT id FROM accounts WHERE id=$1 FOR UPDATE`, id).Scan(&found)
}
