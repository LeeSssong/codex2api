package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/codex2api/plugins"
)

// PluginStore persists plugin controls without coupling the registry to the
// database package. Each plugin has its own row and version/flags payload.
type PluginStore struct{ db *DB }

func NewPluginStore(db *DB) *PluginStore { return &PluginStore{db: db} }

// Pin controls through publication so a disable and old result have a defined
// transaction order across application instances.
func (db *DB) PluginEnabledTx(ctx context.Context, tx *sql.Tx, id string) (bool, error) {
	if _, err := tx.ExecContext(ctx, `INSERT INTO plugin_settings(id,enabled) VALUES($1,TRUE) ON CONFLICT(id) DO NOTHING`, id); err != nil {
		return false, err
	}
	q := `SELECT enabled FROM plugin_settings WHERE id=$1`
	if !db.isSQLite() {
		q += ` FOR UPDATE`
	}
	var enabled bool
	err := tx.QueryRowContext(ctx, q, id).Scan(&enabled)
	return enabled, err
}

func (db *DB) ensurePluginSettingsSchema(ctx context.Context) error {
	table := `CREATE TABLE IF NOT EXISTS plugin_settings (
		id TEXT PRIMARY KEY,
		version TEXT NOT NULL DEFAULT '',
		source_sha TEXT NOT NULL DEFAULT '',
		sdk_compatibility TEXT NOT NULL DEFAULT 'plugins/v1',
		update_mode TEXT NOT NULL DEFAULT 'compiled',
		enabled BOOLEAN NOT NULL DEFAULT TRUE,
		flags TEXT NOT NULL DEFAULT '{}',
		updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`
	_, err := db.conn.ExecContext(ctx, table)
	if err != nil {
		return err
	}
	columns := []struct{ name, ddl string }{
		{"source_sha", `TEXT NOT NULL DEFAULT ''`},
		{"sdk_compatibility", `TEXT NOT NULL DEFAULT 'plugins/v1'`},
		{"update_mode", `TEXT NOT NULL DEFAULT 'compiled'`},
	}
	for _, column := range columns {
		var count int
		var query string
		if db.isSQLite() {
			query = `SELECT COUNT(*) FROM pragma_table_info('plugin_settings') WHERE name=$1`
		} else {
			query = `SELECT COUNT(*) FROM information_schema.columns WHERE table_name='plugin_settings' AND column_name=$1`
		}
		if err := db.conn.QueryRowContext(ctx, query, column.name).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			if _, err := db.conn.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE plugin_settings ADD COLUMN %s %s`, column.name, column.ddl)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *PluginStore) Get(ctx context.Context, id string) (plugins.Setting, error) {
	var setting plugins.Setting
	var enabled bool
	var rawFlags string
	var sourceSHA, sdkCompatibility, updateMode string
	err := s.db.conn.QueryRowContext(ctx, `SELECT id,version,source_sha,sdk_compatibility,update_mode,enabled,flags FROM plugin_settings WHERE id=$1`, id).Scan(&setting.ID, &setting.Version, &sourceSHA, &sdkCompatibility, &updateMode, &enabled, &rawFlags)
	if err != nil {
		if err == sql.ErrNoRows {
			return plugins.Setting{}, plugins.ErrNotFound
		}
		return plugins.Setting{}, err
	}
	setting.Enabled = enabled
	setting.SourceSHA = sourceSHA
	setting.SDKCompatibility = sdkCompatibility
	setting.UpdateMode = updateMode
	if err := json.Unmarshal([]byte(rawFlags), &setting.Flags); err != nil {
		return plugins.Setting{}, err
	}
	return setting, nil
}

func (s *PluginStore) Put(ctx context.Context, setting plugins.Setting) error {
	flags, err := json.Marshal(setting.Flags)
	if err != nil {
		return err
	}
	return s.db.withWriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO plugin_settings(id,version,source_sha,sdk_compatibility,update_mode,enabled,flags,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,CURRENT_TIMESTAMP) ON CONFLICT(id) DO UPDATE SET version=EXCLUDED.version,source_sha=EXCLUDED.source_sha,sdk_compatibility=EXCLUDED.sdk_compatibility,update_mode=EXCLUDED.update_mode,enabled=EXCLUDED.enabled,flags=EXCLUDED.flags,updated_at=CURRENT_TIMESTAMP`, setting.ID, setting.Version, setting.SourceSHA, setting.SDKCompatibility, setting.UpdateMode, setting.Enabled, string(flags))
		if err != nil || setting.Enabled {
			return err
		}
		if err = s.db.fenceSmartOpsPluginDisableTx(ctx, tx, setting.ID); err != nil {
			return err
		}
		switch setting.ID {
		case "token-guard":
			_, err = tx.ExecContext(ctx, `UPDATE account_token_guard_jobs SET state='cancelled',cancellation=TRUE,fence=fence+1 WHERE state IN ('queued','running','cancelling')`)
		case "quality-ops":
			_, err = tx.ExecContext(ctx, `UPDATE account_quality_plans SET lease='',lease_until=NULL,version=version+1 WHERE lease<>''`)
			if err == nil {
				_, err = tx.ExecContext(ctx, `UPDATE account_codex_paths SET quality_bps_recovery_epoch=quality_bps_recovery_epoch+1 WHERE upstream='basispoints'`)
			}
		case "credential-ops":
			for table, query := range map[string]string{
				"credential_ops_tasks":    `UPDATE credential_ops_tasks SET status='cancelled',stage='cancelled',lease_owner='',lease_until=NULL,finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE status IN ('queued','running')`,
				"credential_ops_monitors": `UPDATE credential_ops_monitors SET lease_owner='',lease_until=NULL,updated_at=CURRENT_TIMESTAMP WHERE lease_owner<>''`,
			} {
				var exists bool
				lookup := `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=$1)`
				if !s.db.isSQLite() {
					lookup = `SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema=current_schema() AND table_name=$1)`
				}
				if err = tx.QueryRowContext(ctx, lookup, table).Scan(&exists); err != nil {
					return err
				}
				if exists {
					if _, err = tx.ExecContext(ctx, query); err != nil {
						return err
					}
				}
			}
		}
		return err
	})
}
