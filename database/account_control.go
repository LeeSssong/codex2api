package database

import (
	"context"
	"database/sql"
	"fmt"
)

// Account control ownership has a separate monotonic fence from credentials.
// Triggers cover every writer, including direct administrative SQL and ABA edits.
func (db *DB) ensureAccountControlSchema(ctx context.Context) error {
	if db.isSQLite() {
		columns, err := db.sqliteTableColumns(ctx, "accounts")
		if err != nil {
			return err
		}
		if _, exists := columns["control_revision"]; !exists {
			if _, err = db.conn.ExecContext(ctx, "ALTER TABLE accounts ADD COLUMN control_revision BIGINT NOT NULL DEFAULT 1"); err != nil {
				return err
			}
		}
	} else {
		if _, err := db.conn.ExecContext(ctx, "ALTER TABLE accounts ADD COLUMN IF NOT EXISTS control_revision BIGINT NOT NULL DEFAULT 1"); err != nil {
			return err
		}
	}
	fields := "name,platform,type,proxy_url,status,error_message,cooldown_reason,cooldown_until,enabled,locked,deleted_at,score_bias_override,base_concurrency_override,credit_enabled,credit_skip_usage_window,skip_warm_tier,tags,note"
	var statements []string
	if db.isSQLite() {
		statements = []string{
			"CREATE TRIGGER IF NOT EXISTS accounts_control_revision AFTER UPDATE OF " + fields + " ON accounts BEGIN UPDATE accounts SET control_revision=OLD.control_revision+1 WHERE id=NEW.id; END",
			"CREATE TRIGGER IF NOT EXISTS account_members_control_insert AFTER INSERT ON account_group_members BEGIN UPDATE accounts SET control_revision=control_revision+1 WHERE id=NEW.account_id; END",
			"CREATE TRIGGER IF NOT EXISTS account_members_control_delete AFTER DELETE ON account_group_members BEGIN UPDATE accounts SET control_revision=control_revision+1 WHERE id=OLD.account_id; END",
			"CREATE TRIGGER IF NOT EXISTS account_members_control_update AFTER UPDATE ON account_group_members BEGIN UPDATE accounts SET control_revision=control_revision+1 WHERE id IN (OLD.account_id,NEW.account_id); END",
			"CREATE TRIGGER IF NOT EXISTS account_groups_control_update AFTER UPDATE ON account_groups BEGIN UPDATE accounts SET control_revision=control_revision+1 WHERE id IN (SELECT account_id FROM account_group_members WHERE group_id=NEW.id); END",
		}
	} else {
		statements = []string{
			"CREATE OR REPLACE FUNCTION bump_account_control_revision() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.control_revision := OLD.control_revision + 1; RETURN NEW; END $$",
			"DROP TRIGGER IF EXISTS accounts_control_revision ON accounts",
			"CREATE TRIGGER accounts_control_revision BEFORE UPDATE OF " + fields + " ON accounts FOR EACH ROW EXECUTE FUNCTION bump_account_control_revision()",
			"CREATE OR REPLACE FUNCTION bump_account_member_control_revision() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF TG_OP <> 'INSERT' THEN UPDATE accounts SET control_revision=control_revision+1 WHERE id=OLD.account_id; END IF; IF TG_OP <> 'DELETE' THEN UPDATE accounts SET control_revision=control_revision+1 WHERE id=NEW.account_id; END IF; RETURN NULL; END $$",
			"DROP TRIGGER IF EXISTS account_members_control_revision ON account_group_members",
			"CREATE TRIGGER account_members_control_revision AFTER INSERT OR UPDATE OR DELETE ON account_group_members FOR EACH ROW EXECUTE FUNCTION bump_account_member_control_revision()",
			"CREATE OR REPLACE FUNCTION bump_group_account_control_revision() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN UPDATE accounts SET control_revision=control_revision+1 WHERE id IN (SELECT account_id FROM account_group_members WHERE group_id=NEW.id); RETURN NULL; END $$",
			"DROP TRIGGER IF EXISTS account_groups_control_revision ON account_groups",
			"CREATE TRIGGER account_groups_control_revision AFTER UPDATE ON account_groups FOR EACH ROW EXECUTE FUNCTION bump_group_account_control_revision()",
		}
	}
	for _, statement := range statements {
		if _, err := db.conn.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("account control revision: %w", err)
		}
	}
	return nil
}

// WithAccountControlTx shares the native SQLite writer gate. All operations in
// fn must use tx; calling a top-level DB writer from fn would re-enter the gate.
func (db *DB) WithAccountControlTx(ctx context.Context, fn func(*sql.Tx) error) error {
	return db.withWriteTx(ctx, fn)
}

// LockAccountControlTx takes a row lock on PG and the write reservation on
// SQLite, preventing a read-to-write snapshot upgrade race across processes.
func (db *DB) LockAccountControlTx(ctx context.Context, tx *sql.Tx, id int64) (int64, error) {
	if db.isSQLite() {
		if _, err := tx.ExecContext(ctx, "UPDATE accounts SET control_revision=control_revision WHERE id=$1", id); err != nil {
			return 0, err
		}
	}
	query := "SELECT control_revision FROM accounts WHERE id=$1 AND status <> 'deleted'"
	if !db.isSQLite() {
		query += " FOR UPDATE"
	}
	var revision int64
	err := tx.QueryRowContext(ctx, query, id).Scan(&revision)
	return revision, err
}
func (db *DB) AccountControlRevision(ctx context.Context, id int64) (int64, error) {
	var revision int64
	err := db.conn.QueryRowContext(ctx, "SELECT control_revision FROM accounts WHERE id=$1 AND status <> 'deleted'", id).Scan(&revision)
	return revision, err
}
func (db *DB) AccountControlOutboxTx(ctx context.Context, tx *sql.Tx, id int64) error {
	return insertSchedulerOutboxEventTx(ctx, tx, SchedulerEntityAccount, id, "updated")
}
func (db *DB) AccountControlIsSQLite() bool { return db.isSQLite() }
