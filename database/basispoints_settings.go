package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

type BasispointsSettings struct {
	ConfigSource           string   `json:"-"`
	Enabled                bool     `json:"enabled"`
	ModelScope             string   `json:"model_scope"`
	Models                 []string `json:"models"`
	ImageRelayEnabled      bool     `json:"image_relay_enabled"`
	ImageRelayPublicOrigin string   `json:"image_relay_public_origin"`
	ImageRelayEpoch        int64    `json:"image_relay_epoch"`
}

func DefaultBasispointsSettings() BasispointsSettings {
	s := BasispointsSettings{ConfigSource: "environment", ModelScope: "selected", Models: []string{"gpt-5.6-sol", "gpt-6-astra"}, ImageRelayPublicOrigin: strings.TrimSpace(os.Getenv("BASISPOINTS_IMAGE_PUBLIC_ORIGIN"))}
	raw := strings.TrimSpace(os.Getenv("BASISPOINTS_MODELS"))
	if raw == "*" || strings.EqualFold(raw, "all") {
		s.ModelScope = "all"
		s.Models = []string{}
	} else if raw != "" {
		s.Models = normalizeBasispointsModels(strings.Split(raw, ","))
	}
	if s.ImageRelayPublicOrigin == "" {
		s.ImageRelayPublicOrigin = strings.TrimSpace(os.Getenv("IMAGE_ASSET_PUBLIC_BASE_URL"))
	}
	s.ImageRelayPublicOrigin = strings.TrimRight(s.ImageRelayPublicOrigin, "/")
	relayEnv := strings.ToLower(strings.TrimSpace(os.Getenv("BASISPOINTS_IMAGE_RELAY_ENABLED")))
	s.ImageRelayEnabled = relayEnv == "true" || relayEnv == "1" || relayEnv == "on"
	if relayEnv == "" && s.ImageRelayPublicOrigin != "" {
		s.ImageRelayEnabled = true
		if s.Validate() != nil {
			s.ImageRelayEnabled = false
		}
	}
	return s
}
func normalizeBasispointsModels(models []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, m := range models {
		m = strings.ToLower(strings.TrimSpace(m))
		if m != "" && !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}
func basispointsModelsAllow(scope string, models []string, model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return false
	}
	if scope == "all" {
		return true
	}
	for _, m := range models {
		m = strings.ToLower(strings.TrimSpace(m))
		if model == m {
			return true
		}
		suffix, ok := strings.CutPrefix(model, m+"-")
		if ok && len(suffix) == 10 {
			if date, e := time.Parse("2006-01-02", suffix); e == nil && date.Format("2006-01-02") == suffix {
				return true
			}
		}
	}
	return false
}
func (s BasispointsSettings) AllowsModel(model string) bool {
	return basispointsModelsAllow(s.ModelScope, s.Models, model)
}
func validateBasispointsModels(models []string) error {
	if len(models) > 256 {
		return fmt.Errorf("models exceeds 256 entries")
	}
	for _, model := range models {
		if len(model) > 256 {
			return fmt.Errorf("model name exceeds 256 bytes")
		}
	}
	return nil
}
func (s BasispointsSettings) Validate() error {
	if e := validateBasispointsModels(s.Models); e != nil {
		return e
	}
	if s.ModelScope != "all" && s.ModelScope != "selected" {
		return fmt.Errorf("model_scope must be all or selected")
	}
	if s.ImageRelayPublicOrigin != "" {
		u, e := url.Parse(s.ImageRelayPublicOrigin)
		if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return fmt.Errorf("image_relay_public_origin must be an HTTPS origin")
		}
	}
	if s.ImageRelayEnabled && s.ImageRelayPublicOrigin == "" {
		return fmt.Errorf("image relay requires an HTTPS public origin")
	}
	return nil
}
func decodeBasispointsSettings(raw string, enabled bool) (BasispointsSettings, error) {
	s := DefaultBasispointsSettings()
	if strings.TrimSpace(raw) != "" {
		s.ConfigSource = "persisted"
		if e := json.Unmarshal([]byte(raw), &s); e != nil {
			return BasispointsSettings{}, fmt.Errorf("invalid persisted Basispoints settings: %w", e)
		}
	}
	s.Enabled = enabled
	s.Models = normalizeBasispointsModels(s.Models)
	return s, nil
}
func (db *DB) GetBasispointsSettings(ctx context.Context) (BasispointsSettings, error) {
	var raw string
	var enabled bool
	var epoch int64
	e := db.conn.QueryRowContext(ctx, "SELECT COALESCE(basispoints_config,''), COALESCE(codex_basispoints_enabled,false),basispoints_image_relay_epoch FROM system_settings WHERE id=1").Scan(&raw, &enabled, &epoch)
	if errors.Is(e, sql.ErrNoRows) {
		return DefaultBasispointsSettings(), nil
	}
	if e != nil {
		return BasispointsSettings{}, e
	}
	s, e := decodeBasispointsSettings(raw, enabled)
	s.ImageRelayEpoch = epoch
	return s, e
}
func (db *DB) SaveBasispointsSettings(ctx context.Context, s BasispointsSettings) error {
	if e := s.Validate(); e != nil {
		return e
	}
	s.Models = normalizeBasispointsModels(s.Models)
	s.ImageRelayPublicOrigin = strings.TrimRight(strings.TrimSpace(s.ImageRelayPublicOrigin), "/")
	if e := s.Validate(); e != nil {
		return e
	}
	return db.withWriteTx(ctx, func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, "INSERT INTO system_settings(id) VALUES(1) ON CONFLICT(id) DO NOTHING"); e != nil {
			return e
		}
		query := "SELECT COALESCE(basispoints_config,''),COALESCE(codex_basispoints_enabled,false),basispoints_image_relay_epoch FROM system_settings WHERE id=1"
		if !db.isSQLite() {
			query += " FOR UPDATE"
		}
		var raw string
		var enabled bool
		var epoch int64
		if e := tx.QueryRowContext(ctx, query).Scan(&raw, &enabled, &epoch); e != nil {
			return e
		}
		old, e := decodeBasispointsSettings(raw, enabled)
		if e != nil {
			return e
		}
		s.ImageRelayEpoch = epoch
		if raw == "" || s.ImageRelayEnabled != old.ImageRelayEnabled || s.ImageRelayPublicOrigin != old.ImageRelayPublicOrigin || s.Enabled != old.Enabled {
			s.ImageRelayEpoch++
		}
		encoded, e := json.Marshal(s)
		if e != nil {
			return e
		}
		var object map[string]json.RawMessage
		json.Unmarshal(encoded, &object)
		delete(object, "enabled")
		delete(object, "image_relay_epoch")
		encoded, e = json.Marshal(object)
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, "UPDATE system_settings SET basispoints_config=$1,codex_basispoints_enabled=$2,basispoints_image_relay_epoch=$3 WHERE id=1", string(encoded), s.Enabled, s.ImageRelayEpoch)
		return e
	})
}

type BasispointsAccountPolicy struct {
	ModelScope           string   `json:"model_scope"`
	Models               []string `json:"models"`
	AutoDisableOn403     bool     `json:"auto_disable_on_403"`
	CacheCreationAsInput bool     `json:"cache_creation_as_input"`
	Revision             int64    `json:"revision"`
	DisabledBy           string   `json:"disabled_by,omitempty"`
	DisabledReason       string   `json:"disabled_reason,omitempty"`
	DisabledAt           int64    `json:"disabled_at,omitempty"`
}

func (p BasispointsAccountPolicy) Validate() error {
	if e := validateBasispointsModels(p.Models); e != nil {
		return e
	}
	if p.ModelScope != "inherit" && p.ModelScope != "all" && p.ModelScope != "selected" {
		return fmt.Errorf("model_scope must be inherit, all or selected")
	}
	return nil
}
func (p BasispointsAccountPolicy) AllowsModel(model string, global BasispointsSettings) bool {
	if !global.AllowsModel(model) {
		return false
	}
	if p.ModelScope == "" || p.ModelScope == "inherit" {
		return true
	}
	return basispointsModelsAllow(p.ModelScope, p.Models, model)
}
func (db *DB) GetBasispointsAccountPolicy(ctx context.Context, id int64) (BasispointsAccountPolicy, error) {
	p := BasispointsAccountPolicy{ModelScope: "inherit", Models: []string{}}
	var models string
	var auto, cache int
	e := db.conn.QueryRowContext(ctx, "SELECT model_scope,models_json,auto_disable_on_403,cache_creation_as_input,policy_revision,disabled_by,disabled_reason,disabled_at FROM account_codex_paths WHERE account_id=$1 AND upstream='basispoints'", id).Scan(&p.ModelScope, &models, &auto, &cache, &p.Revision, &p.DisabledBy, &p.DisabledReason, &p.DisabledAt)
	if errors.Is(e, sql.ErrNoRows) {
		return p, nil
	}
	if e != nil {
		return p, e
	}
	if e = json.Unmarshal([]byte(models), &p.Models); e != nil {
		return p, e
	}
	p.AutoDisableOn403 = auto != 0
	p.CacheCreationAsInput = cache != 0
	return p, nil
}
func (db *DB) basispointsOAuthGeneration(ctx context.Context, tx *sql.Tx, id int64) (int64, error) {
	query := "SELECT credential_generation,credentials FROM accounts WHERE id=$1 AND deleted_at IS NULL AND status<>'deleted'"
	if !db.isSQLite() {
		query += " FOR UPDATE"
	}
	var generation int64
	var raw any
	if e := tx.QueryRowContext(ctx, query, id).Scan(&generation, &raw); e != nil {
		return 0, e
	}
	c := decodeCredentials(raw)
	upstream := credentialStringFromMap(c, "upstream_type")
	if (upstream != "" && upstream != "codex") || strings.EqualFold(strings.TrimSpace(credentialStringFromMap(c, "auth_mode")), "agentIdentity") || (credentialStringFromMap(c, "refresh_token") == "" && credentialStringFromMap(c, "access_token") == "") {
		return 0, fmt.Errorf("Basispoints policy requires a Codex OAuth account")
	}
	return generation, nil
}
func (db *DB) SetBasispointsAccountPolicy(ctx context.Context, id int64, p BasispointsAccountPolicy) error {
	return db.UpdateBasispointsAccountRoute(ctx, id, nil, &p)
}

// Administrative permission and policy edits share one lock and revision bump.
func (db *DB) UpdateBasispointsAccountRoute(ctx context.Context, id int64, allowed *bool, p *BasispointsAccountPolicy) error {
	if p == nil && allowed == nil {
		return fmt.Errorf("missing Basispoints action")
	}
	if p != nil {
		if e := p.Validate(); e != nil {
			return e
		}
	}
	return db.withWriteTx(ctx, func(tx *sql.Tx) error {
		if _, e := db.basispointsOAuthGeneration(ctx, tx, id); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, "INSERT INTO account_codex_paths(account_id,upstream) VALUES($1,'basispoints') ON CONFLICT(account_id,upstream) DO NOTHING", id); e != nil {
			return e
		}
		if allowed != nil {
			if _, e := tx.ExecContext(ctx, "UPDATE account_codex_paths SET allowed=$1,disabled_by='',disabled_reason='',disabled_at=0 WHERE account_id=$2 AND upstream='basispoints'", boolInt(*allowed), id); e != nil {
				return e
			}
		}
		if p != nil {
			models, _ := json.Marshal(normalizeBasispointsModels(p.Models))
			if _, e := tx.ExecContext(ctx, "UPDATE account_codex_paths SET model_scope=$1,models_json=$2,auto_disable_on_403=$3,cache_creation_as_input=$4 WHERE account_id=$5 AND upstream='basispoints'", p.ModelScope, string(models), boolInt(p.AutoDisableOn403), boolInt(p.CacheCreationAsInput), id); e != nil {
				return e
			}
		}
		if _, e := tx.ExecContext(ctx, "UPDATE account_codex_paths SET policy_revision=policy_revision+1 WHERE account_id=$1 AND upstream='basispoints'", id); e != nil {
			return e
		}
		return insertSchedulerOutboxEventTx(ctx, tx, "account", id, "codex_routes_updated")
	})
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func (db *DB) DisableBasispointsForHTTP403(ctx context.Context, id, expectedGeneration, expectedRevision int64) (bool, error) {
	changed := false
	e := db.withWriteTx(ctx, func(tx *sql.Tx) error {
		generation, e := db.basispointsOAuthGeneration(ctx, tx, id)
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		if generation != expectedGeneration {
			return nil
		}
		result, e := tx.ExecContext(ctx, "UPDATE account_codex_paths SET allowed=0,disabled_by='auto_403',disabled_reason='http_403',disabled_at=$1,policy_revision=policy_revision+1 WHERE account_id=$2 AND upstream='basispoints' AND allowed=1 AND auto_disable_on_403=1 AND policy_revision=$3", time.Now().Unix(), id, expectedRevision)
		if e != nil {
			return e
		}
		n, e := result.RowsAffected()
		if e != nil || n == 0 {
			return e
		}
		changed = true
		if e = insertAccountEventsTx(ctx, tx, []int64{id}, "updated", "basispoints_auto_403"); e != nil {
			return e
		}
		return insertSchedulerOutboxEventTx(ctx, tx, "account", id, "codex_routes_updated")
	})
	return changed && e == nil, e
}
func (db *DB) ensureBasispointsPolicySchema(ctx context.Context) error {
	columns := [][2]string{{"model_scope", "TEXT NOT NULL DEFAULT 'inherit'"}, {"models_json", "TEXT NOT NULL DEFAULT '[]'"}, {"auto_disable_on_403", "INTEGER NOT NULL DEFAULT 0"}, {"cache_creation_as_input", "INTEGER NOT NULL DEFAULT 0"}, {"policy_revision", "BIGINT NOT NULL DEFAULT 0"}, {"disabled_by", "TEXT NOT NULL DEFAULT ''"}, {"disabled_reason", "TEXT NOT NULL DEFAULT ''"}, {"disabled_at", "BIGINT NOT NULL DEFAULT 0"}}
	for _, c := range columns {
		if db.isSQLite() {
			if e := db.ensureSQLiteColumn(ctx, "account_codex_paths", c[0], c[1]); e != nil {
				return e
			}
		} else {
			if _, e := db.conn.ExecContext(ctx, "ALTER TABLE account_codex_paths ADD COLUMN IF NOT EXISTS "+c[0]+" "+c[1]); e != nil {
				return e
			}
		}
	}
	for _, c := range [][2]string{{"basispoints_config", "TEXT NOT NULL DEFAULT ''"}, {"basispoints_image_relay_epoch", "BIGINT NOT NULL DEFAULT 0"}} {
		if db.isSQLite() {
			if e := db.ensureSQLiteColumn(ctx, "system_settings", c[0], c[1]); e != nil {
				return e
			}
		} else {
			if _, e := db.conn.ExecContext(ctx, "ALTER TABLE system_settings ADD COLUMN IF NOT EXISTS "+c[0]+" "+c[1]); e != nil {
				return e
			}
		}
	}
	return nil
}
