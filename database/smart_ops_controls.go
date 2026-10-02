package database

import (
	"context"
	"database/sql"
	"github.com/codex2api/smartops"
)

func (db *DB) fenceSmartOpsPluginDisableTx(ctx context.Context, tx *sql.Tx, id string) error {
	table := ""
	switch id {
	case smartops.PluginPelicanTests:
		table = "smart_ops_test_jobs"
	case smartops.PluginAutoConfig:
		table = "smart_ops_plugin_epochs"
	default:
		return nil
	}
	var exists bool
	q := `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=$1)`
	if !db.isSQLite() {
		q = `SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema=current_schema() AND table_name=$1)`
	}
	if e := tx.QueryRowContext(ctx, q, table).Scan(&exists); e != nil {
		return e
	}
	if !exists {
		return nil
	}
	if id == smartops.PluginAutoConfig {
		_, e := tx.ExecContext(ctx, `INSERT INTO smart_ops_plugin_epochs(plugin_id,epoch) VALUES($1,1) ON CONFLICT(plugin_id) DO UPDATE SET epoch=smart_ops_plugin_epochs.epoch+1`, id)
		return e
	}
	_, e := tx.ExecContext(ctx, `UPDATE smart_ops_test_jobs SET status='cancelled',error='plugin disabled',lease_until=0 WHERE status IN ('queued','running')`)
	return e
}
func (db *DB) SmartOpsControlEpoch(ctx context.Context, id string) (int64, error) {
	var epoch int64
	e := db.conn.QueryRowContext(ctx, `SELECT epoch FROM smart_ops_plugin_epochs WHERE plugin_id=$1`, id).Scan(&epoch)
	if e == sql.ErrNoRows {
		return 0, nil
	}
	return epoch, e
}
