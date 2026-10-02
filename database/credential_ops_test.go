package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/codex2api/plugins"
	"github.com/codex2api/smartops"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCredentialOpsTaskLeaseIsDurableAndExclusive(t *testing.T) {
	db, err := New("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.EnsureCredentialOpsSchema(ctx); err != nil {
		t.Fatal(err)
	}
	task, err := db.CreateCredentialOpsTask(ctx, 42, 7)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := db.ClaimCredentialOpsTask(ctx, "worker-a", time.Minute)
	if err != nil || claimed == nil || claimed.ID != task.ID {
		t.Fatalf("claim = %#v, %v", claimed, err)
	}
	if next, err := db.ClaimCredentialOpsTask(ctx, "worker-b", time.Minute); err != nil || next != nil {
		t.Fatalf("second claim = %#v, %v", next, err)
	}
	if err := db.CompleteCredentialOpsTask(ctx, task.ID, "worker-b", "failed", "failed", "wrong owner"); err == nil {
		t.Fatal("wrong worker completed task")
	}
	if err := db.CompleteCredentialOpsTask(ctx, task.ID, "worker-a", "succeeded", "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetCredentialOpsTask(ctx, task.ID)
	if err != nil || stored.Status != "succeeded" || stored.FinishedAt == nil {
		t.Fatalf("stored = %#v, %v", stored, err)
	}
}

func TestCredentialOpsAtomicFencingAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.db")
	db, err := New("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { db.Close() }()
	ctx := context.Background()
	if err = db.EnsureCredentialOpsSchema(ctx); err != nil {
		t.Fatal(err)
	}
	id, err := db.InsertAccountWithCredentials(ctx, "a@example.com", map[string]any{"email": "a@example.com", "workspace_id": "workspace-123", "access_token": "old"}, "")
	if err != nil {
		t.Fatal(err)
	}
	row, _ := db.GetAccountByID(ctx, id)
	cfg := CredentialOpsLoginConfigRow{AccountID: id, LoginEmail: "a@example.com", CredentialMode: "password_totp", PasswordCiphertext: "enc:v1:fixture"}
	job, err := db.CreateCredentialOpsTaskWithConfig(ctx, id, row.CredentialGeneration, cfg)
	if err != nil {
		t.Fatal(err)
	}
	first, err := db.ClaimCredentialOpsTask(ctx, "same-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.RenewCredentialOpsTask(ctx, job.ID, "same-worker", first.Attempt, "waiting_totp"); err != nil {
		t.Fatal(err)
	}
	_, err = db.conn.ExecContext(ctx, `UPDATE credential_ops_tasks SET lease_until=$1 WHERE id=$2`, time.Now().UTC().Add(-time.Minute), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	credentials := map[string]any{"email": "a@example.com", "workspace_id": "workspace-123", "access_token": "new"}
	if _, err = db.CommitCredentialOpsLogin(ctx, job.ID, "same-worker", first.Attempt, credentials); !errors.Is(err, ErrCredentialOpsStale) {
		t.Fatalf("expired write %v", err)
	}
	reclaimed, err := db.ClaimCredentialOpsTask(ctx, "same-worker", time.Minute)
	if err != nil || reclaimed == nil || reclaimed.Attempt != 2 {
		t.Fatalf("reclaim %#v %v", reclaimed, err)
	}
	if err = db.FailCredentialOpsTask(ctx, job.ID, "same-worker", first.Attempt); !errors.Is(err, ErrCredentialOpsStale) {
		t.Fatalf("old failure callback %v", err)
	}
	if _, err = db.CommitCredentialOpsLogin(ctx, job.ID, "same-worker", first.Attempt, credentials); !errors.Is(err, ErrCredentialOpsStale) {
		t.Fatalf("old success callback %v", err)
	}
	if err = db.CancelCredentialOpsTask(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.CommitCredentialOpsLogin(ctx, job.ID, "same-worker", reclaimed.Attempt, credentials); !errors.Is(err, ErrCredentialOpsStale) {
		t.Fatalf("cancelled write %v", err)
	}
	untouched, _ := db.GetAccountByID(ctx, id)
	if untouched.CredentialGeneration != row.CredentialGeneration || untouched.Credentials["access_token"] != "old" {
		t.Fatal("stale callback changed credentials")
	}
	job, err = db.CreateCredentialOpsTaskWithConfig(ctx, id, row.CredentialGeneration, cfg)
	if err != nil {
		t.Fatal(err)
	}
	claimed, _ := db.ClaimCredentialOpsTask(ctx, "worker", time.Minute)
	_, applied, err := db.UpdateAccountCredentialsCAS(ctx, id, row.CredentialGeneration, map[string]any{"access_token": "winner"})
	if err != nil || !applied {
		t.Fatal(err)
	}
	if _, err = db.CommitCredentialOpsLogin(ctx, job.ID, "worker", claimed.Attempt, credentials); !errors.Is(err, ErrCredentialOpsStale) {
		t.Fatalf("generation mismatch %v", err)
	}
	task, _ := db.GetCredentialOpsTask(ctx, job.ID)
	if task.Status != "running" {
		t.Fatal("failed transaction committed terminal task")
	}
	m := CredentialOpsMonitorRow{AccountID: id, Enabled: true, AutoRelogin: true, IntervalSeconds: 60, FailureThreshold: 2, CooldownSeconds: 600}
	if err = db.SaveCredentialOpsMonitor(ctx, m); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = New("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.CredentialOpsTaskConfig(ctx, job.ID)
	if err != nil || got.PasswordCiphertext != cfg.PasswordCiphertext {
		t.Fatal("task config lost after restart")
	}
	monitor, err := db.ClaimCredentialOpsMonitor(ctx, 0, "probe-owner")
	if err != nil || monitor == nil {
		t.Fatalf("claim monitor %#v %v", monitor, err)
	}
	if duplicate, _ := db.ClaimCredentialOpsMonitor(ctx, id, "other-owner"); duplicate != nil {
		t.Fatal("monitor concurrent claim")
	}
	if reauth, err := db.CompleteCredentialOpsMonitor(ctx, *monitor, "auth"); err != nil || reauth {
		t.Fatalf("first auth %#v %v", reauth, err)
	}
	monitor, err = db.ClaimCredentialOpsMonitor(ctx, id, "probe-owner-2")
	if err != nil || monitor == nil {
		t.Fatal(err)
	}
	if reauth, err := db.CompleteCredentialOpsMonitor(ctx, *monitor, "auth"); err != nil || !reauth {
		t.Fatalf("threshold %#v %v", reauth, err)
	}
	if due, _ := db.ClaimCredentialOpsMonitor(ctx, 0, "during-cooldown"); due != nil {
		t.Fatal("monitor ignored cooldown")
	}
}

func TestCredentialOpsPostgresTaskLease(t *testing.T) {
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
	if err := db.EnsureCredentialOpsSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.ExecContext(ctx, `DELETE FROM credential_ops_tasks`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateCredentialOpsTask(ctx, 99, 1); err != nil {
		t.Fatal(err)
	}
	got, err := db.ClaimCredentialOpsTask(ctx, "pg-worker", time.Minute)
	if err != nil || got == nil || got.LeaseOwner != "pg-worker" {
		t.Fatalf("claim = %#v, %v", got, err)
	}
	if err = db.CancelCredentialOpsTask(ctx, got.ID); err != nil {
		t.Fatal(err)
	}
	email := fmt.Sprintf("pg-%d@example.com", time.Now().UnixNano())
	accountID, err := db.InsertAccountWithCredentials(ctx, "pg-credential-test", map[string]any{"email": email, "workspace_id": "workspace-pg", "access_token": "old"}, "")
	if err != nil {
		t.Fatal(err)
	}
	row, _ := db.GetAccountByID(ctx, accountID)
	job, err := db.CreateCredentialOpsTaskWithConfig(ctx, accountID, row.CredentialGeneration, CredentialOpsLoginConfigRow{AccountID: accountID, LoginEmail: email, CredentialMode: "password_totp", Engine: "local_worker", PasswordCiphertext: "enc:v1:test"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := db.ClaimCredentialOpsTask(ctx, "pg-worker", time.Minute)
	if err != nil || claimed == nil || claimed.ID != job.ID {
		t.Fatalf("atomic claim %#v %v", claimed, err)
	}
	if err = db.RenewCredentialOpsTask(ctx, job.ID, "pg-worker", claimed.Attempt, "waiting_totp"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.CommitCredentialOpsLogin(ctx, job.ID, "pg-worker", claimed.Attempt, map[string]any{"email": email, "workspace_id": "workspace-pg", "access_token": "new"}); err != nil {
		t.Fatal(err)
	}
	stored, _ := db.GetAccountByID(ctx, accountID)
	if stored.CredentialGeneration != row.CredentialGeneration+1 || stored.Credentials["access_token"] != "new" {
		t.Fatal("postgres atomic publication failed")
	}
	if _, err = db.CommitCredentialOpsLogin(ctx, job.ID, "pg-worker", claimed.Attempt, map[string]any{"email": email, "workspace_id": "workspace-pg", "access_token": "late"}); !errors.Is(err, ErrCredentialOpsStale) {
		t.Fatalf("postgres late callback %v", err)
	}
	m := CredentialOpsMonitorRow{AccountID: accountID, Enabled: true, AutoRelogin: true, IntervalSeconds: 60, FailureThreshold: 1, CooldownSeconds: 60}
	if err = db.SaveCredentialOpsMonitor(ctx, m); err != nil {
		t.Fatal(err)
	}
	monitor, err := db.ClaimCredentialOpsMonitor(ctx, accountID, "pg-probe")
	if err != nil || monitor == nil {
		t.Fatalf("pg monitor %#v %v", monitor, err)
	}
	if reauth, err := db.CompleteCredentialOpsMonitor(ctx, *monitor, "auth"); err != nil || !reauth {
		t.Fatalf("pg probe %v %v", reauth, err)
	}
	// Verify first-import dedup uses the same native account identity.
	job, err = db.CreateCredentialOpsTaskWithConfig(ctx, 0, 0, CredentialOpsLoginConfigRow{LoginEmail: email, CredentialMode: "password_totp", Engine: "local_worker", PasswordCiphertext: "enc:v1:test"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err = db.ClaimCredentialOpsTask(ctx, "pg-worker", time.Minute)
	if err != nil || claimed == nil {
		t.Fatal(err)
	}
	dedupID, err := db.CommitCredentialOpsLogin(ctx, job.ID, "pg-worker", claimed.Attempt, map[string]any{"email": email, "workspace_id": "workspace-pg", "access_token": "dedup"})
	if err != nil || dedupID != accountID {
		t.Fatalf("PG native dedup %d %v", dedupID, err)
	}
}

func TestCredentialOpsFirstImportNativeDefaultsTransaction(t *testing.T) {
	db, err := New("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err = db.EnsureCredentialOpsSchema(ctx); err != nil {
		t.Fatal(err)
	}
	group, err := db.CreateAccountGroup(ctx, "credential-defaults", "", "", 0, 0, sql.NullInt64{})
	if err != nil {
		t.Fatal(err)
	}
	cfg := smartops.DefaultOAuthAutoConfig()
	cfg.Enabled = true
	cfg.GroupIDs = []int64{group}
	cfg.Concurrency = 4
	cfg.Priority = 25
	cfg.Revision = "credential-test"
	db.SetSmartOpsOAuthDefaultsProvider(func(context.Context) (smartops.OAuthAutoConfig, bool) { return cfg, true })
	defer db.SetSmartOpsOAuthDefaultsProvider(nil)
	job, err := db.CreateCredentialOpsTaskWithConfig(ctx, 0, 0, CredentialOpsLoginConfigRow{Name: "Named native import", LoginEmail: "defaults@example.com", CredentialMode: "password_totp", PasswordCiphertext: "enc:v1:test"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := db.ClaimCredentialOpsTask(ctx, "defaults-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	id, err := db.CommitCredentialOpsLogin(ctx, job.ID, "defaults-worker", claimed.Attempt, map[string]any{"email": "defaults@example.com", "workspace_id": "workspace-defaults", "access_token": "test-only"})
	if err != nil {
		t.Fatal(err)
	}
	row, err := db.GetAccountByID(ctx, id)
	if err != nil || row.Name != "Named native import" || row.BaseConcurrencyOverride.Int64 != 4 || row.Credentials["scheduler_priority"] != float64(25) {
		t.Fatalf("defaults %#v %v", row, err)
	}
	var members int
	if err = db.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_group_members WHERE account_id=$1 AND group_id=$2`, id, group).Scan(&members); err != nil || members != 1 {
		t.Fatal("native group binding not committed")
	}
	cfg.GroupIDs = []int64{999999}
	job, err = db.CreateCredentialOpsTaskWithConfig(ctx, 0, 0, CredentialOpsLoginConfigRow{LoginEmail: "invalid@example.com", CredentialMode: "password_totp", PasswordCiphertext: "enc:v1:test"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err = db.ClaimCredentialOpsTask(ctx, "defaults-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.CommitCredentialOpsLogin(ctx, job.ID, "defaults-worker", claimed.Attempt, map[string]any{"email": "invalid@example.com", "workspace_id": "workspace-invalid", "access_token": "test-only"}); err == nil {
		t.Fatal("invalid default group committed native account")
	}
	var count int
	if err = db.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts WHERE name='invalid@example.com'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("defaults failure leaked partial native account")
	}
}

func TestCredentialOpsDatabaseDisableFence(t *testing.T) {
	db, err := New("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err = db.EnsureCredentialOpsSchema(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = db.CreateCredentialOpsTaskWithConfig(ctx, 0, 0, CredentialOpsLoginConfigRow{LoginEmail: "fence@example.com", CredentialMode: "password_totp", PasswordCiphertext: "enc:v1:fixture"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := db.ClaimCredentialOpsTask(ctx, "worker", time.Minute)
	if err != nil || task == nil {
		t.Fatal(err)
	}
	registry := plugins.NewRegistry(NewPluginStore(db))
	setting, err := registry.Get(ctx, "credential-ops")
	if err != nil {
		t.Fatal(err)
	}
	setting.Enabled = false
	if err = registry.Set(ctx, setting); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ClaimCredentialOpsTask(ctx, "worker-next", time.Minute); !errors.Is(err, ErrCredentialOpsDisabled) {
		t.Fatalf("disabled claim %v", err)
	}
	if err = db.RenewCredentialOpsTask(ctx, task.ID, "worker", task.Attempt, "waiting_otp"); !errors.Is(err, ErrCredentialOpsDisabled) {
		t.Fatalf("disabled renewal %v", err)
	}
	if _, err = db.CommitCredentialOpsLogin(ctx, task.ID, "worker", task.Attempt, map[string]any{"email": "fence@example.com", "workspace_id": "workspace-fence", "access_token": "test-only"}); !errors.Is(err, ErrCredentialOpsDisabled) {
		t.Fatalf("disabled commit %v", err)
	}
	if _, err = db.CreateCredentialOpsTaskWithConfig(ctx, 0, 0, CredentialOpsLoginConfigRow{LoginEmail: "fence@example.com"}); !errors.Is(err, ErrCredentialOpsDisabled) {
		t.Fatalf("disabled queue %v", err)
	}
}
