package database

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestRetireSmartOpsLegacyFixtureIsTransactionalAndIdempotent(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "retire-smart-ops.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err = db.conn.ExecContext(ctx, `
		ALTER TABLE account_codex_paths ADD COLUMN quality_bps TEXT NOT NULL DEFAULT '';
		ALTER TABLE account_codex_paths ADD COLUMN quality_bps_disabled_at BIGINT NOT NULL DEFAULT 0;
		ALTER TABLE account_codex_paths ADD COLUMN quality_bps_recovery_at BIGINT NOT NULL DEFAULT 0;
		ALTER TABLE account_codex_paths ADD COLUMN quality_bps_owner_revision BIGINT NOT NULL DEFAULT 0;
		ALTER TABLE account_codex_paths ADD COLUMN quality_bps_recovery_epoch BIGINT NOT NULL DEFAULT 0;
		INSERT INTO accounts(id,name,credentials,status,enabled,base_concurrency_override,control_revision) VALUES(77,'keep','{"smart_ops_load_factor":3,"smart_ops_initial_revision":4,"smart_ops_bps_defaults":"legacy","api_key":"keep"}','error',1,9,12);
		INSERT INTO api_keys(name,key) VALUES('keep','key-retire-fixture');
		CREATE TABLE smart_ops_settings(key TEXT PRIMARY KEY,value TEXT,updated_at TIMESTAMP);
		CREATE TABLE smart_ops_test_jobs(id INTEGER PRIMARY KEY,plan_id INTEGER,payload TEXT,status TEXT,owner TEXT,lease_until BIGINT,created_at BIGINT,error TEXT);
		CREATE TABLE smart_ops_test_results(job_id BIGINT,sample INTEGER,account_id BIGINT,payload TEXT,output TEXT,PRIMARY KEY(job_id,sample));
		CREATE TABLE smart_ops_test_plans(id INTEGER PRIMARY KEY,payload TEXT,enabled BOOLEAN,next_run BIGINT,interval_minutes INTEGER);
		CREATE TABLE smart_ops_concurrency(account_id BIGINT PRIMARY KEY,payload TEXT);
		CREATE TABLE smart_ops_plugin_epochs(plugin_id TEXT PRIMARY KEY,epoch BIGINT);
		CREATE TABLE plugin_settings(id TEXT PRIMARY KEY,enabled BOOLEAN);
		CREATE TABLE account_ops_settings(key TEXT PRIMARY KEY,value TEXT);
		CREATE TABLE account_ops_alerts(account_id BIGINT,kind TEXT,account_name TEXT,signal TEXT,http_status INTEGER,first_seen TIMESTAMP,last_seen TIMESTAMP,occurrences BIGINT,state TEXT,last_sent_at TIMESTAMP,next_send_at TIMESTAMP,attempts INTEGER,lease TEXT,lease_until TIMESTAMP,PRIMARY KEY(account_id,kind));
		CREATE TABLE account_quality_plans(id INTEGER PRIMARY KEY,account_id BIGINT,version BIGINT,enabled BOOLEAN);
		CREATE TABLE account_quality_rounds(id INTEGER PRIMARY KEY,plan_id BIGINT,account_id BIGINT,created_at TIMESTAMP,record TEXT);
		CREATE TABLE account_quality_recovery(plan_id BIGINT PRIMARY KEY,snapshot TEXT);
		CREATE TABLE account_token_guard_config(id INTEGER PRIMARY KEY,value TEXT,revision BIGINT);
		CREATE TABLE account_token_guard_jobs(id TEXT PRIMARY KEY,state TEXT);
		CREATE TABLE account_token_guard_states(account_id BIGINT PRIMARY KEY,state TEXT);
		CREATE TABLE account_token_guard_ownership(account_id BIGINT PRIMARY KEY,control_revision BIGINT,previous_status TEXT,previous_error TEXT,created_at BIGINT);
		CREATE TABLE account_token_guard_events(id INTEGER PRIMARY KEY,account_id BIGINT,kind TEXT,detail TEXT,latency_ms INTEGER,created_at BIGINT);
		CREATE TABLE credential_ops_login_configs(account_id BIGINT PRIMARY KEY,login_email TEXT);
		CREATE TABLE credential_ops_tasks(id INTEGER PRIMARY KEY,account_id BIGINT,status TEXT);
		CREATE TABLE credential_ops_monitors(account_id BIGINT PRIMARY KEY,enabled BOOLEAN);
		CREATE TABLE credential_ops_task_inputs(task_id BIGINT PRIMARY KEY,config_json TEXT);
		CREATE TABLE credential_ops_rules(account_id BIGINT PRIMARY KEY,interval_seconds INTEGER);
		INSERT INTO account_token_guard_ownership VALUES(77,12,'active','',1);
		INSERT INTO account_quality_plans VALUES(1,77,3,1);
		INSERT INTO account_quality_recovery VALUES(1,'{"action":"enable_bps","control_revision":12,"plan_version":3,"bps_applied":"{\\"legacy\\":true}","bps_previous":""}');
		INSERT INTO account_codex_paths(account_id,upstream,allowed,quality_bps,quality_bps_disabled_at,quality_bps_recovery_at,quality_bps_owner_revision,quality_bps_recovery_epoch) VALUES(77,'basispoints',0,'{"legacy":true}',1,2,12,1);
		INSERT INTO account_token_guard_config VALUES(1,'{}',1);
		INSERT INTO smart_ops_concurrency VALUES(77,'{"native":true}');
	`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.conn.ExecContext(ctx, `DELETE FROM data_migrations WHERE version=$1`, dataMigrationRetireSmartOpsV1); err != nil {
		t.Fatal(err)
	}
	if err = db.runDataMigrations(ctx); err != nil {
		t.Fatal(err)
	}
	if err = db.runDataMigrations(ctx); err != nil {
		t.Fatal("idempotent rerun: ", err)
	}
	for _, table := range []string{"smart_ops_settings", "smart_ops_concurrency", "account_quality_plans", "account_quality_recovery", "account_token_guard_config", "credential_ops_tasks", "plugin_settings"} {
		var n int
		if err := db.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=$1`, table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("retired table %s remains", table)
		}
	}
	var creds string
	if err := db.conn.QueryRowContext(ctx, `SELECT credentials FROM accounts WHERE id=77`).Scan(&creds); err != nil {
		t.Fatal(err)
	}
	if creds != `{"api_key":"keep"}` {
		t.Errorf("credential cleanup = %s", creds)
	}
	var allowed, concurrency int
	if err := db.conn.QueryRowContext(ctx, `SELECT allowed,base_concurrency_override FROM account_codex_paths p JOIN accounts a ON a.id=p.account_id WHERE p.account_id=77`).Scan(&allowed, &concurrency); err != nil {
		t.Fatal(err)
	}
	if allowed != 0 || concurrency != 9 {
		t.Errorf("native preservation allowed=%d concurrency=%d", allowed, concurrency)
	}
	var keys int
	if err := db.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_keys WHERE key='key-retire-fixture'`).Scan(&keys); err != nil {
		t.Fatal(err)
	}
	if keys != 1 {
		t.Errorf("api key count=%d", keys)
	}
}

func TestRetireSmartOpsPostgresLegacyFixture(t *testing.T) {
	dsn := os.Getenv("CODEX2API_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("requires isolated CODEX2API_TEST_POSTGRES_DSN")
	}
	db, err := New("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	stmts := []string{
		`ALTER TABLE account_codex_paths ADD COLUMN IF NOT EXISTS quality_bps TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE account_codex_paths ADD COLUMN IF NOT EXISTS quality_bps_disabled_at BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE account_codex_paths ADD COLUMN IF NOT EXISTS quality_bps_recovery_at BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE account_codex_paths ADD COLUMN IF NOT EXISTS quality_bps_owner_revision BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE account_codex_paths ADD COLUMN IF NOT EXISTS quality_bps_recovery_epoch BIGINT NOT NULL DEFAULT 0`,
		`CREATE OR REPLACE FUNCTION legacy_path_fence() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$`,
		`DROP TRIGGER IF EXISTS account_paths_control_revision ON account_codex_paths`,
		`CREATE TRIGGER account_paths_control_revision AFTER UPDATE OF allowed,quality_bps,quality_bps_disabled_at ON account_codex_paths FOR EACH ROW EXECUTE FUNCTION legacy_path_fence()`,
		`CREATE TABLE IF NOT EXISTS smart_ops_concurrency(account_id BIGINT PRIMARY KEY,payload TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS plugin_settings(id TEXT PRIMARY KEY,enabled BOOLEAN NOT NULL DEFAULT TRUE,version TEXT NOT NULL DEFAULT '',source_sha TEXT NOT NULL DEFAULT '',sdk_compatibility TEXT NOT NULL DEFAULT '',update_mode TEXT NOT NULL DEFAULT '',flags TEXT NOT NULL DEFAULT '{}',updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS account_token_guard_ownership(account_id BIGINT PRIMARY KEY,control_revision BIGINT NOT NULL,previous_status TEXT NOT NULL,previous_error TEXT NOT NULL,created_at BIGINT NOT NULL)`,
		`INSERT INTO accounts(name,credentials,status,enabled,control_revision) VALUES('pg-retire','{"smart_ops_load_factor":3,"smart_ops_initial_revision":4,"smart_ops_bps_defaults":"legacy","keep":"yes"}','active',TRUE,1) RETURNING id`,
	}
	var id int64
	for i, q := range stmts {
		if i == len(stmts)-1 {
			if err := db.conn.QueryRowContext(ctx, q).Scan(&id); err != nil {
				t.Fatal(err)
			}
		} else if _, err := db.conn.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.conn.ExecContext(ctx, `INSERT INTO account_codex_paths(account_id,upstream,allowed,quality_bps,quality_bps_disabled_at,quality_bps_owner_revision) VALUES($1,'basispoints',0,'{"owned":true}',1,1) ON CONFLICT(account_id,upstream) DO UPDATE SET allowed=0,quality_bps='{"owned":true}',quality_bps_disabled_at=1,quality_bps_owner_revision=1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.ExecContext(ctx, `INSERT INTO smart_ops_concurrency(account_id,payload) VALUES($1,$2)`, id, `{"quality_5xx":{"generation":1,"previous_cooldowns":{"m":{"reason":"manual","reset_at":"2099-01-01T00:00:00Z"}},"applied_cooldowns":{"m":"2099-02-01T00:00:00Z"}}}`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.ExecContext(ctx, `INSERT INTO account_model_cooldowns(account_id,model,reason,reset_at) VALUES($1,'m','quality_5xx_ramp','2099-02-01T00:00:00Z') ON CONFLICT(account_id,model) DO UPDATE SET reason=EXCLUDED.reason,reset_at=EXCLUDED.reset_at`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.ExecContext(ctx, `DELETE FROM data_migrations WHERE version=$1`, dataMigrationRetireSmartOpsV1); err != nil {
		t.Fatal(err)
	}
	if err := db.runDataMigrations(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.runDataMigrations(ctx); err != nil {
		t.Fatal(err)
	}
	var keep string
	if err := db.conn.QueryRowContext(ctx, `SELECT credentials->>'keep' FROM accounts WHERE id=$1`, id).Scan(&keep); err != nil || keep != "yes" {
		t.Fatalf("credentials retained: %q %v", keep, err)
	}
	var reason string
	if err := db.conn.QueryRowContext(ctx, `SELECT reason FROM account_model_cooldowns WHERE account_id=$1 AND model='m'`, id).Scan(&reason); err != nil || reason != "manual" {
		t.Fatalf("cooldown restore: %q %v", reason, err)
	}
	var n int
	if err := db.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='smart_ops_concurrency'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("smart ops table remains: %d %v", n, err)
	}
	if err := db.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_trigger WHERE tgname='account_paths_control_revision'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("legacy path trigger remains: %d %v", n, err)
	}
}

func TestRetireSmartOpsRollbackLeavesLegacyFixture(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "retire-smart-ops-rollback.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err = db.conn.ExecContext(ctx, `CREATE TABLE smart_ops_settings(key TEXT PRIMARY KEY,value TEXT); INSERT INTO smart_ops_settings VALUES('x','y'); CREATE TABLE account_token_guard_ownership(account_id BIGINT PRIMARY KEY,control_revision BIGINT,previous_status TEXT,previous_error TEXT,created_at BIGINT); INSERT INTO account_token_guard_ownership VALUES(1,1,'active','',1); UPDATE accounts SET status='error',error_message='token_guard:verified_auth_failure',control_revision=1 WHERE id=1; CREATE TRIGGER retire_fail BEFORE UPDATE ON accounts BEGIN SELECT RAISE(ABORT,'fixture failure'); END`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.retireSmartOps(ctx, tx); err != nil {
		t.Fatal(err)
	}
	_ = tx.Rollback()
	var got string
	if err = db.conn.QueryRowContext(ctx, `SELECT value FROM smart_ops_settings WHERE key='x'`).Scan(&got); err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
	if got != "y" {
		t.Fatalf("rollback lost legacy row: %q", got)
	}
}
