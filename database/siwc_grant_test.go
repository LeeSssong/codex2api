package database

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSIWCGrantGenerationAndStorageIsolation(t *testing.T) {
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	db, err := New("sqlite", filepath.Join(t.TempDir(), "grants.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	id, err := db.InsertAccountWithCredentials(ctx, "subscriber", map[string]any{"refresh_token": "native-token"}, "")
	if err != nil {
		t.Fatal(err)
	}
	row, err := db.GetAccountByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	save := func(parent, grant int64, token string) error {
		return db.SaveSIWCGrant(ctx, id, parent, grant, map[string]any{"refresh_token": token})
	}
	if err := save(row.CredentialGeneration, 0, "initial-token"); err != nil {
		t.Fatal(err)
	}
	if err := save(row.CredentialGeneration, 0, "duplicate"); !errors.Is(err, ErrSIWCGrantChanged) {
		t.Fatal("duplicate registration not rejected", err)
	}
	if err := save(row.CredentialGeneration+1, 1, "stale-parent"); !errors.Is(err, ErrSIWCGrantChanged) {
		t.Fatal("parent generation mismatch not rejected", err)
	}
	if err := save(row.CredentialGeneration, 1, "rotated-token"); err != nil {
		t.Fatal(err)
	}
	if err := save(row.CredentialGeneration, 1, "late-writer"); !errors.Is(err, ErrSIWCGrantChanged) {
		t.Fatal("stale grant accepted", err)
	}
	var ciphertext string
	if err := db.conn.QueryRowContext(ctx, `SELECT credentials_ciphertext FROM credential_operations_profile_grants WHERE account_id=$1 AND profile='siwc'`, id).Scan(&ciphertext); err != nil || !strings.HasPrefix(ciphertext, "enc:v1:") || strings.Contains(ciphertext, "rotated-token") {
		t.Fatal("grant not stored encrypted", err)
	}
	grant, err := db.GetCredentialOpsProfileGrant(ctx, id, "siwc")
	if err != nil || grant == nil || grant.Generation != 2 || grant.Credentials["refresh_token"] != "rotated-token" {
		t.Fatal("rotation was not preserved", err)
	}
	if ok, err := db.InvalidateCredentialOpsProfileGrantCAS(ctx, id, "siwc", 2); err != nil || !ok {
		t.Fatal("invalidation failed", err)
	}
	if err := save(row.CredentialGeneration, 2, "late-refresh"); !errors.Is(err, ErrSIWCGrantChanged) {
		t.Fatal("refresh revived invalid grant", err)
	}
	if err := db.RebindInvalidatedSIWCGrant(ctx, id, row.CredentialGeneration, 2, map[string]any{"refresh_token": "new-consent"}); err != nil {
		t.Fatal(err)
	}
	if err := save(row.CredentialGeneration, 2, "old-authorization"); !errors.Is(err, ErrSIWCGrantChanged) {
		t.Fatal("previous authorization overwrote rebind", err)
	}
	after, err := db.GetAccountByID(ctx, id)
	if err != nil || after.GetCredential("refresh_token") != "native-token" || after.CredentialGeneration != row.CredentialGeneration {
		t.Fatal("native authorization changed", err)
	}
}

func TestSIWCRefreshSurvivesNativeRotationButRejectsIdentityChange(t *testing.T) {
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	db, err := New("sqlite", filepath.Join(t.TempDir(), "identity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	id, err := db.InsertAccountWithCredentials(ctx, "saved", map[string]any{"refresh_token": "native", "email": "test@example.com", "workspace_id": "workspace"}, "")
	if err != nil {
		t.Fatal(err)
	}
	before, err := db.GetAccountByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveSIWCGrant(ctx, id, before.CredentialGeneration, 0, map[string]any{"refresh_token": "siwc"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.ExecContext(ctx, `UPDATE accounts SET credential_generation=credential_generation+1 WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveSIWCRefreshedGrant(ctx, before, 1, map[string]any{"refresh_token": "siwc-next"}); err != nil {
		t.Fatal("native rotation discarded SIWC refresh", err)
	}
	if err := db.UpdateCredentials(ctx, id, map[string]any{"email": "other@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveSIWCRefreshedGrant(ctx, before, 2, map[string]any{"refresh_token": "must-not-save"}); !errors.Is(err, ErrSIWCGrantChanged) {
		t.Fatal("identity change accepted", err)
	}
}

func TestSIWCWorkerCompletionCommitsWithGrant(t *testing.T) {
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	db, err := New("sqlite", filepath.Join(t.TempDir(), "worker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	id, err := db.InsertAccountWithCredentials(ctx, "saved", map[string]any{"refresh_token": "native"}, "")
	if err != nil {
		t.Fatal(err)
	}
	row, err := db.GetAccountByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var taskID int64
	err = db.conn.QueryRowContext(ctx, `INSERT INTO credential_operations_tasks(account_id,status,stage,expected_generation,oauth_profile,lease_owner,lease_until,attempt) VALUES($1,'running','login',$2,'siwc','worker',$3,1) RETURNING id`, id, row.CredentialGeneration, db.timeArg(time.Now().Add(time.Minute))).Scan(&taskID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.ExecContext(ctx, `INSERT INTO credential_operations_login_configs(account_id,login_email,revision) VALUES($1,'test@example.com',1)`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.ExecContext(ctx, `UPDATE credential_operations_tasks SET config_revision=1 WHERE id=$1`, taskID); err != nil {
		t.Fatal(err)
	}
	cfg := CredentialOpsLoginConfigRow{AccountID: id}
	if err := db.conn.QueryRowContext(ctx, `SELECT control_revision FROM accounts WHERE id=$1`, id).Scan(&cfg.RouteRevision); err != nil {
		t.Fatal(err)
	}
	_ = db.conn.QueryRowContext(ctx, `SELECT revision FROM fixed_egress_config WHERE id=1`).Scan(&cfg.FixedConfigRevision)
	raw, _ := json.Marshal(cfg)
	if _, err := db.conn.ExecContext(ctx, `INSERT INTO credential_operations_task_inputs(task_id,config_json) VALUES($1,$2)`, taskID, string(raw)); err != nil {
		t.Fatal(err)
	}
	credentials := map[string]any{"refresh_token": "new-siwc"}
	worker := SIWCWorkerFence{TaskID: taskID, Owner: "worker", Attempt: 2}
	if err := db.CommitSIWCWorkerGrant(ctx, id, row.CredentialGeneration, 0, credentials, false, worker); !errors.Is(err, ErrCredentialOpsStale) {
		t.Fatal("stale worker accepted", err)
	}
	worker.Attempt = 1
	if err := db.CommitSIWCWorkerGrant(ctx, id, row.CredentialGeneration, 9, credentials, false, worker); !errors.Is(err, ErrSIWCGrantChanged) {
		t.Fatal("stale grant accepted", err)
	}
	job, err := db.GetCredentialOpsTask(ctx, taskID)
	if err != nil || job.Status != "running" {
		t.Fatal("grant failure committed successful task", err)
	}
	if err := db.CommitSIWCWorkerGrant(ctx, id, row.CredentialGeneration, 0, credentials, false, worker); err != nil {
		t.Fatal(err)
	}
	job, err = db.GetCredentialOpsTask(ctx, taskID)
	if err != nil || job.Status != "succeeded" {
		t.Fatal("task completion not committed", err)
	}
	if err := db.CommitSIWCWorkerGrant(ctx, id, row.CredentialGeneration, 1, credentials, false, worker); !errors.Is(err, ErrCredentialOpsStale) {
		t.Fatal("completion replayed", err)
	}
}
