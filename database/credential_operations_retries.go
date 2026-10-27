package database

import (
	"context"
	"database/sql"
	"errors"
)

func credentialOpsReusableRetry(task *CredentialOpsTaskRow) bool {
	return task != nil && (task.Status == "queued" || task.Status == "running" || task.Status == "succeeded")
}

// FindCredentialOpsRetryOutcome permits recovery of a lost retry response
// before resolving a route that an already active or completed job does not need.
func (db *DB) FindCredentialOpsRetryOutcome(ctx context.Context, id int64) (*CredentialOpsTaskRow, error) {
	source, e := db.GetCredentialOpsTask(ctx, id)
	if e != nil || source == nil {
		return source, e
	}
	if source.Kind != "login" {
		return nil, errors.New("only login tasks can be retried; use recovery verification for 2FA")
	}
	if credentialOpsReusableRetry(source) {
		return source, nil
	}
	cfg, e := db.CredentialOpsTaskConfig(ctx, id)
	if e != nil {
		return nil, e
	}
	root := cfg.RetrySourceID
	if root == 0 {
		root = id
	}
	var childID int64
	e = db.conn.QueryRowContext(ctx, "SELECT child_task_id FROM credential_operations_retry_lineages WHERE source_task_id=$1", root).Scan(&childID)
	if e == sql.ErrNoRows {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	child, e := db.GetCredentialOpsTask(ctx, childID)
	if e != nil {
		return nil, e
	}
	if credentialOpsReusableRetry(child) {
		return child, nil
	}
	return nil, nil
}
