package database

import (
	"context"
	"database/sql"
	"github.com/google/uuid"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func guardTestDB(t *testing.T, driver string) *DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "guard.db")
	if driver == "postgres" {
		dsn = os.Getenv("CODEX2API_TEST_POSTGRES_DSN")
		if dsn == "" {
			t.Skip("requires isolated CODEX2API_TEST_POSTGRES_DSN")
		}
	}
	schema := ""
	if driver == "postgres" {
		schema = "guard_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		u, e := url.Parse(dsn)
		if e != nil {
			t.Fatal(e)
		}
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		dsn = u.String()
	}
	if driver == "postgres" {
		return guardPostgresFixture(t, dsn, schema)
	}
	db, err := New(driver, dsn, schema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		if schema != "" {
			conn, e := sql.Open("pgx", dsn)
			if e == nil {
				_, _ = conn.Exec("DROP SCHEMA " + quotePostgresIdent(schema) + " CASCADE")
				conn.Close()
			}
		}
	})
	return db
}
func TestAccountControlRevisionFencesManualAndGroupABA(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			db := guardTestDB(t, driver)
			ctx := context.Background()
			id, err := db.InsertAccountWithCredentials(ctx, "guard-control", map[string]any{"refresh_token": "rt-" + uuid.NewString()}, "")
			if err != nil {
				t.Fatal(err)
			}
			read := func() int64 {
				var rev int64
				if err := db.conn.QueryRowContext(ctx, "SELECT control_revision FROM accounts WHERE id=$1", id).Scan(&rev); err != nil {
					t.Fatal(err)
				}
				return rev
			}
			rev := read()
			if rev < 1 {
				t.Fatalf("initial revision %d", rev)
			}
			for _, enable := range []bool{false, true, true} {
				if err := db.SetAccountEnabled(ctx, id, enable); err != nil {
					t.Fatal(err)
				}
				next := read()
				if next <= rev {
					t.Fatalf("manual change failed fence: %d -> %d", rev, next)
				}
				rev = next
			}
			group, err := db.CreateAccountGroup(ctx, "guard-"+uuid.NewString(), "", "", 0, 0, sql.NullInt64{})
			if err != nil {
				t.Fatal(err)
			}
			for _, groups := range [][]int64{{group}, {}, {group}} {
				if err := db.SetAccountGroups(ctx, id, groups); err != nil {
					t.Fatal(err)
				}
				next := read()
				if next <= rev {
					t.Fatalf("membership ABA failed fence: %d -> %d", rev, next)
				}
				rev = next
			}
			if _, err := db.conn.ExecContext(ctx, "UPDATE account_groups SET name=$1 WHERE id=$2", "changed-"+uuid.NewString(), group); err != nil {
				t.Fatal(err)
			}
			if read() <= rev {
				t.Fatal("group configuration failed to invalidate account ownership")
			}
		})
	}
}

// Guard persistence tests initialize the actual guard migrations against the
// native account/group columns they operate on. Avoid unrelated full gateway
// startup migrations and background backfills for each isolated PG schema.
func guardPostgresFixture(t *testing.T, dsn, schema string) *DB {
	t.Helper()
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(4)
	conn.SetMaxIdleConns(4)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	t.Cleanup(func() { _, _ = conn.Exec("DROP SCHEMA " + quotePostgresIdent(schema) + " CASCADE"); conn.Close() })
	if _, err = conn.ExecContext(ctx, "CREATE SCHEMA "+quotePostgresIdent(schema)); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"CREATE TABLE accounts(id BIGSERIAL PRIMARY KEY,name TEXT NOT NULL DEFAULT '',platform TEXT NOT NULL DEFAULT 'openai',type TEXT NOT NULL DEFAULT 'oauth',credentials JSONB NOT NULL DEFAULT '{}',proxy_url TEXT NOT NULL DEFAULT '',status TEXT NOT NULL DEFAULT 'active',error_message TEXT NOT NULL DEFAULT '',enabled BOOLEAN NOT NULL DEFAULT TRUE,locked BOOLEAN NOT NULL DEFAULT FALSE,deleted_at TIMESTAMPTZ,cooldown_reason TEXT NOT NULL DEFAULT '',cooldown_until TIMESTAMPTZ,score_bias_override BIGINT,base_concurrency_override BIGINT,credit_enabled BOOLEAN NOT NULL DEFAULT FALSE,credit_skip_usage_window BOOLEAN NOT NULL DEFAULT FALSE,skip_warm_tier BOOLEAN NOT NULL DEFAULT FALSE,tags JSONB NOT NULL DEFAULT '[]',note TEXT NOT NULL DEFAULT '',credential_generation BIGINT NOT NULL DEFAULT 1,credential_family_id TEXT NOT NULL DEFAULT '',created_at TIMESTAMPTZ DEFAULT NOW(),updated_at TIMESTAMPTZ DEFAULT NOW())",
		"CREATE TABLE account_groups(id BIGSERIAL PRIMARY KEY,name TEXT UNIQUE NOT NULL,description TEXT NOT NULL DEFAULT '',color TEXT NOT NULL DEFAULT '',sort_order BIGINT NOT NULL DEFAULT 0,auto_pause_5h_threshold DOUBLE PRECISION NOT NULL DEFAULT 0,auto_pause_7d_threshold DOUBLE PRECISION NOT NULL DEFAULT 0,base_concurrency_override BIGINT)",
		"CREATE TABLE account_group_members(account_id BIGINT NOT NULL,group_id BIGINT NOT NULL,PRIMARY KEY(account_id,group_id))",
		"CREATE TABLE scheduler_outbox(id BIGSERIAL PRIMARY KEY,entity_type TEXT NOT NULL,entity_id BIGINT NOT NULL,event_type TEXT NOT NULL,created_at TIMESTAMPTZ DEFAULT NOW())",
	} {
		if _, err = conn.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	db := &DB{conn: conn, driver: "postgres"}
	for _, migrate := range []func(context.Context) error{db.ensureTokenGuardSchema, db.ensureAccountControlSchema, db.ensureCodexRefreshSchema, db.ensureCodexRoutesSchema} {
		if err = migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestAccountControlDDLFailurePreservesInstalledFence(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			db := guardTestDB(t, driver)
			ctx := context.Background()
			id, e := db.InsertAccountWithCredentials(ctx, "ddl-fence", map[string]any{"refresh_token": "ddl-" + uuid.NewString()}, "")
			if e != nil {
				t.Fatal(e)
			}
			before, e := db.AccountControlRevision(ctx, id)
			if e != nil {
				t.Fatal(e)
			}
			drop := "DROP TRIGGER accounts_control_revision"
			if !db.isSQLite() {
				drop += " ON accounts"
			}
			if e = db.execControlDDLTransaction(ctx, []string{drop, "CREATE TRIGGER invalid syntax"}); e == nil {
				t.Fatal("invalid replacement unexpectedly succeeded")
			}
			if e = db.SetAccountEnabled(ctx, id, false); e != nil {
				t.Fatal(e)
			}
			after, e := db.AccountControlRevision(ctx, id)
			if e != nil || after <= before {
				t.Fatal("failed DDL replacement removed live control fence", e)
			}
			if e = db.ensureAccountControlSchema(ctx); e != nil {
				t.Fatal("idempotent trigger reinstall failed", e)
			}
		})
	}
}
