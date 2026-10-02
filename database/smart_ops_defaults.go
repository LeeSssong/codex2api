package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/codex2api/smartops"
	"strings"
	"sync"
)

type SmartOpsDefaultsProvider func(context.Context) (smartops.OAuthAutoConfig, bool)

var smartOpsDefaultProviders sync.Map

// Startup binds a cached, gated policy so no settings SQL runs inside a native
// credential-publication transaction. Only new identities call this hook.
func (db *DB) SetSmartOpsOAuthDefaultsProvider(provider SmartOpsDefaultsProvider) {
	if provider == nil {
		smartOpsDefaultProviders.Delete(db)
	} else {
		smartOpsDefaultProviders.Store(db, provider)
	}
}
func smartOpsOAuthPlatform(credentials map[string]interface{}) string {
	str := func(key string) string { v, _ := credentials[key].(string); return strings.TrimSpace(v) }
	switch str("upstream_type") {
	case "claude":
		if kind := str("claude_auth_kind"); kind == "" || kind == "oauth" {
			return "claude"
		}
	case "antigravity":
		if str("refresh_token") != "" {
			return "antigravity"
		}
	case "grok":
		if str("refresh_token") != "" || str("grok_client_id") != "" {
			return "grok"
		}
	case "":
		if str("api_key") == "" && (str("access_token") != "" || str("refresh_token") != "") {
			return "openai"
		}
	}
	return ""
}
func (db *DB) ApplySmartOpsOAuthDefaultsTx(ctx context.Context, tx *sql.Tx, accountID int64, platform string) error {
	provider, ok := smartOpsDefaultProviders.Load(db)
	if !ok {
		return nil
	}
	c, enabled := provider.(SmartOpsDefaultsProvider)(ctx)
	if !enabled || !c.Enabled || c.Platform != platform {
		return nil
	}
	return db.applySmartOpsDefaultsTx(ctx, tx, accountID, c)
}
func (db *DB) applySmartOpsDefaultsTx(ctx context.Context, tx *sql.Tx, id int64, c smartops.OAuthAutoConfig) error {
	enabled, e := db.PluginEnabledTx(ctx, tx, smartops.PluginAutoConfig)
	if e != nil {
		return e
	}
	if !enabled {
		return nil
	}
	if e := smartops.ValidateOAuthAutoConfig(c); e != nil {
		return e
	}
	var raw any
	if e := tx.QueryRowContext(ctx, `SELECT credentials FROM accounts WHERE id=$1`, id).Scan(&raw); e != nil {
		return e
	}
	credentials := decodeCredentials(raw)
	mapping := map[string]string{}
	if value, ok := credentials["model_mapping"].(string); ok && value != "" {
		if e := json.Unmarshal([]byte(value), &mapping); e != nil {
			return e
		}
	}
	mapping = smartops.ApplyModelMappings(mapping, c.ModelMappings)
	m, e := json.Marshal(mapping)
	if e != nil {
		return e
	}
	bps, e := json.Marshal(c.BPS)
	if e != nil {
		return e
	}
	credentials["model_mapping"] = string(m)
	credentials["scheduler_priority"] = c.Priority
	credentials["smart_ops_load_factor"] = c.LoadFactor
	credentials["smart_ops_initial_revision"] = c.Revision
	credentials["smart_ops_bps_defaults"] = string(bps)
	data, e := json.Marshal(encryptSensitiveCredentials(credentials))
	if e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE accounts SET credentials=$1,base_concurrency_override=$2,updated_at=CURRENT_TIMESTAMP WHERE id=$3`, data, c.Concurrency, id); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM account_group_members WHERE account_id=$1`, id); e != nil {
		return e
	}
	for _, groupID := range c.GroupIDs {
		var channel string
		if e = tx.QueryRowContext(ctx, `SELECT COALESCE(channel,'codex') FROM account_groups WHERE id=$1`, groupID).Scan(&channel); e != nil {
			return e
		}
		expected := c.Platform
		if expected == "openai" {
			expected = "codex"
		}
		if channel != expected {
			return errors.New("configured OAuth group platform mismatch")
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO account_group_members(account_id,group_id) VALUES($1,$2)`, id, groupID); e != nil {
			return e
		}
	}
	return insertSchedulerOutboxEventTx(ctx, tx, SchedulerEntityAccount, id, "upsert")
}
