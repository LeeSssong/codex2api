package database

import (
	"context"
	"database/sql"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"testing"
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
	db, err := New(driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
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
