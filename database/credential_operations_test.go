package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/codex2api/credentialops"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCredentialOperationsInitialImportPersistsAndEnrolls(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "credential-operations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err = db.EnsureCredentialOperationsSchema(ctx); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	prepared, err := credentialops.PrepareConfig(credentialops.LoginConfigInput{Email: "import@example.com", Password: "secret-password", TOTPSecret: "JBSWY3DPEHPK3PXP", ProxySource: "direct"}, credentialops.LoginConfig{})
	if err != nil {
		t.Fatal(err)
	}
	cfg := CredentialOpsLoginConfigRow{Name: "first import", LoginEmail: prepared.Email, CredentialMode: prepared.Mode, Engine: prepared.Engine, ProxySource: prepared.ProxySource, PasswordCiphertext: prepared.PasswordCiphertext, TOTPCiphertext: prepared.TOTPCiphertext}
	queue := func() int64 {
		task, err := db.CreateCredentialOpsTaskWithConfig(ctx, 0, 0, cfg)
		if err != nil {
			t.Fatal(err)
		}
		claim, err := db.ClaimCredentialOpsTask(ctx, "test-worker", time.Minute)
		if err != nil || claim == nil || claim.ID != task.ID {
			t.Fatalf("claim = %#v %v", claim, err)
		}
		token := workspaceIdentityJWT(t, "import@example.com", "workspace-import")
		id, err := db.CommitCredentialOpsLogin(ctx, claim.ID, "test-worker", claim.Attempt, map[string]any{"email": "import@example.com", "workspace_id": "workspace-import", "access_token": token, "id_token": token, "refresh_token": "import-refresh"})
		if err != nil {
			t.Fatal(err)
		}
		saved, err := db.GetCredentialOpsTask(ctx, claim.ID)
		row, rowErr := db.GetAccountByID(ctx, id)
		if err != nil || rowErr != nil || saved.ExpectedGeneration+1 != row.CredentialGeneration {
			t.Fatalf("committed generation missing for import/dedup: task=%+v row=%+v errors=%v/%v", saved, row, err, rowErr)
		}
		return id
	}
	id := queue()
	if id <= 0 {
		t.Fatal("account not created")
	}
	stored, err := db.GetCredentialOpsLoginConfig(ctx, id)
	if err != nil || stored == nil || !strings.HasPrefix(stored.PasswordCiphertext, "enc:") || stored.TOTPCiphertext == "" {
		t.Fatalf("config = %#v %v", stored, err)
	}
	monitors, err := db.ListCredentialOpsMonitors(ctx)
	if err != nil || len(monitors) != 1 || !monitors[0].Enabled {
		t.Fatalf("monitor = %#v %v", monitors, err)
	}
	if duplicate := queue(); duplicate != id {
		t.Fatalf("duplicate import created account %d, want %d", duplicate, id)
	}
	tasks, err := db.ListCredentialOpsTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(tasks)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret-password", "JBSWY3DPEHPK3PXP", stored.PasswordCiphertext, stored.TOTPCiphertext} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("ordinary task response exposed secret")
		}
	}
	var count int
	if err = db.conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM accounts").Scan(&count); err != nil || count != 1 {
		t.Fatalf("accounts = %d %v", count, err)
	}
}

func TestCredentialOperationsConfigRevisionAndGrantIsolation(t *testing.T) {
	db, e := New("sqlite", filepath.Join(t.TempDir(), "fences.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	id, e := db.InsertAccountWithCredentials(ctx, "saved", map[string]any{"plan_type": "plus", "email": "saved@example.com", "workspace_id": "ws", "access_token": "codex-at", "refresh_token": "codex-rt"}, "")
	if e != nil {
		t.Fatal(e)
	}
	row, _ := db.GetAccountByID(ctx, id)
	cfg := CredentialOpsLoginConfigRow{AccountID: id, LoginEmail: "saved@example.com", PasswordCiphertext: "enc:placeholder", CredentialMode: "password_totp", Engine: "local_worker", ProxySource: "direct"}
	if e = db.UpsertCredentialOpsLoginConfig(ctx, cfg); e != nil {
		t.Fatal(e)
	}
	cfgp, _ := db.GetCredentialOpsLoginConfig(ctx, id)
	cfg = *cfgp
	cfg.OAuthProfile = "excel"
	job, e := db.CreateCredentialOpsTaskWithConfig(ctx, id, row.CredentialGeneration, cfg)
	if e != nil {
		t.Fatal(e)
	}
	other, e := db.CreateCredentialOpsTaskWithConfig(ctx, id, row.CredentialGeneration, cfg)
	if e != nil || other.ID != job.ID {
		t.Fatalf("duplicate = %#v %v", other, e)
	}
	claimed, e := db.ClaimCredentialOpsTaskWithCapabilities(ctx, "worker", time.Minute, []string{"excel_oauth_v1"})
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.CommitCredentialOpsLogin(ctx, job.ID, "worker", claimed.Attempt, map[string]any{"email": "saved@example.com", "workspace_id": "ws", "access_token": "excel-at", "refresh_token": "excel-rt"})
	if e != nil {
		t.Fatal(e)
	}
	after, _ := db.GetAccountByID(ctx, id)
	if after.GetCredential("access_token") != "codex-at" || after.CredentialGeneration != row.CredentialGeneration {
		t.Fatal("Excel grant changed primary credentials")
	}
	grant, e := db.GetCredentialOpsProfileGrant(ctx, id, "excel")
	if e != nil || grant == nil || grant.Credentials["access_token"] != "excel-at" {
		t.Fatalf("grant = %#v %v", grant, e)
	}
	if ok, e := db.InvalidateCredentialOpsProfileGrantCAS(ctx, id, "excel", grant.Generation-1); e != nil || ok {
		t.Fatal("stale invalidation accepted")
	}
	cfgp, _ = db.GetCredentialOpsLoginConfig(ctx, id)
	job, e = db.CreateCredentialOpsTaskWithConfig(ctx, id, after.CredentialGeneration, *cfgp)
	if e != nil {
		t.Fatal(e)
	}
	claimed, _ = db.ClaimCredentialOpsTask(ctx, "worker", time.Minute)
	if e = db.UpsertCredentialOpsLoginConfig(ctx, *cfgp); e != nil {
		t.Fatal(e)
	}
	if _, e = db.CommitCredentialOpsLogin(ctx, job.ID, "worker", claimed.Attempt, map[string]any{"email": "saved@example.com", "workspace_id": "ws", "access_token": "late"}); e == nil {
		t.Fatal("old config task overwrote account")
	}
}

func TestCredentialOperationsTOTPRecoveryBlocksLoginAndNeedsExplicitVerification(t *testing.T) {
	db, e := New("sqlite", filepath.Join(t.TempDir(), "totp.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	id, e := db.InsertAccountWithCredentials(ctx, "rotate", map[string]any{"email": "rotate@example.com", "workspace_id": "ws", "access_token": "at"}, "")
	if e != nil {
		t.Fatal(e)
	}
	cfg, e := credentialops.PrepareConfig(credentialops.LoginConfigInput{AccountID: id, Email: "rotate@example.com", Password: "password", TOTPSecret: "JBSWY3DPEHPK3PXP"}, credentialops.LoginConfig{})
	if e != nil {
		t.Fatal(e)
	}
	if e = db.UpsertCredentialOpsLoginConfig(ctx, CredentialOpsLoginConfigRow{AccountID: id, LoginEmail: cfg.Email, CredentialMode: cfg.Mode, Engine: cfg.Engine, ProxySource: cfg.ProxySource, PasswordCiphertext: cfg.PasswordCiphertext, TOTPCiphertext: cfg.TOTPCiphertext}); e != nil {
		t.Fatal(e)
	}
	task, e := db.CreateCredentialOpsTOTPTask(ctx, id, "rotate")
	if e != nil {
		t.Fatal(e)
	}
	claim, e := db.ClaimCredentialOpsTaskWithCapabilities(ctx, "worker", time.Minute, []string{"totp_rotation_v1"})
	if e != nil || claim == nil {
		t.Fatalf("claim %#v %v", claim, e)
	}
	if e = db.CredentialOpsTOTPPhase(ctx, task.ID, "worker", claim.Attempt, "enrolling", ""); e != nil {
		t.Fatal(e)
	}
	if e = db.CredentialOpsTOTPPhase(ctx, task.ID, "worker", claim.Attempt, "activating", ""); e == nil {
		t.Fatal("activation without database candidate accepted")
	}
	if _, e = db.conn.ExecContext(ctx, "CREATE TRIGGER credential_candidate_requires_durability BEFORE UPDATE OF state ON credential_operations_totp_recoveries WHEN NEW.state='prepared' BEGIN SELECT CASE WHEN (SELECT synchronous FROM pragma_synchronous)<>2 THEN RAISE(ABORT,'candidate persistence must use FULL durability') END; END"); e != nil {
		t.Fatal(e)
	}
	const candidate = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	if e = db.CredentialOpsTOTPPhase(ctx, task.ID, "worker", claim.Attempt, "prepared", candidate); e != nil {
		t.Fatal(e)
	}
	if e = db.CredentialOpsTOTPPhase(ctx, task.ID, "worker", claim.Attempt, "activating", ""); e != nil {
		t.Fatal(e)
	}
	if e = db.CredentialOpsTOTPFinish(ctx, task.ID, "worker", claim.Attempt, false); e != nil {
		t.Fatal(e)
	}
	recovery, e := db.GetCredentialOpsTOTPRecovery(ctx, id)
	if e != nil || recovery == nil || recovery.State != "uncertain" || recovery.CandidateCiphertext == "" {
		t.Fatalf("recovery %#v %v", recovery, e)
	}
	saved, _ := db.GetCredentialOpsLoginConfig(ctx, id)
	row, _ := db.GetAccountByID(ctx, id)
	if _, e = db.CreateCredentialOpsTaskWithConfig(ctx, id, row.CredentialGeneration, *saved); e == nil {
		t.Fatal("ordinary login allowed during uncertainty")
	}
	if _, e = db.CreateCredentialOpsTOTPTask(ctx, id, "rotate"); e == nil {
		t.Fatal("second rotation allowed during uncertainty")
	}
	verify, e := db.CreateCredentialOpsTOTPTask(ctx, id, "verify_new")
	if e != nil {
		t.Fatal(e)
	}
	claim, e = db.ClaimCredentialOpsTaskWithCapabilities(ctx, "worker", time.Minute, []string{"totp_rotation_v1"})
	if e != nil || claim == nil || claim.ID != verify.ID {
		t.Fatalf("verify claim %#v %v", claim, e)
	}
	if e = db.CredentialOpsTOTPPhase(ctx, verify.ID, "worker", claim.Attempt, "verify_recovery", ""); e != nil {
		t.Fatal(e)
	}
	if e = db.CredentialOpsTOTPFinish(ctx, verify.ID, "worker", claim.Attempt, true); e != nil {
		t.Fatal(e)
	}
	saved, _ = db.GetCredentialOpsLoginConfig(ctx, id)
	_, dec := credentialops.EnvCrypto()
	actual, e := dec(saved.TOTPCiphertext)
	if e != nil || actual != candidate {
		t.Fatal("candidate not adopted", e)
	}
}

func TestCredentialOperationsImportKeyAndRouteFence(t *testing.T) {
	db, e := New("sqlite", filepath.Join(t.TempDir(), "route.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	cfg := CredentialOpsLoginConfigRow{ImportKey: "same-import-request", LoginEmail: "first@example.com", CredentialMode: "password_totp", Engine: "local_worker", ProxySource: "direct", PasswordCiphertext: "sealed-first"}
	a, e := db.CreateCredentialOpsTaskWithConfig(ctx, 0, 0, cfg)
	if e != nil {
		t.Fatal(e)
	}
	cfg.LoginEmail = "unrelated@example.com"
	b, e := db.CreateCredentialOpsTaskWithConfig(ctx, 0, 0, cfg)
	if e != nil || b.ID != a.ID {
		t.Fatal("response loss created duplicate task", e)
	}
	saved, e := db.CredentialOpsTaskConfig(ctx, a.ID)
	if e != nil || saved.LoginEmail != "first@example.com" {
		t.Fatal("idempotent import replaced durable input")
	}
	id, e := db.InsertAccountWithCredentials(ctx, "route", map[string]any{"email": "route@example.com", "workspace_id": "ws", "access_token": "old"}, "")
	if e != nil {
		t.Fatal(e)
	}
	cfg = CredentialOpsLoginConfigRow{AccountID: id, LoginEmail: "route@example.com", CredentialMode: "password_totp", Engine: "local_worker", ProxySource: "account", PasswordCiphertext: "sealed"}
	if e = db.UpsertCredentialOpsLoginConfig(ctx, cfg); e != nil {
		t.Fatal(e)
	}
	saved, _ = db.GetCredentialOpsLoginConfig(ctx, id)
	row, _ := db.GetAccountByID(ctx, id)
	job, e := db.CreateCredentialOpsTaskWithConfig(ctx, id, row.CredentialGeneration, *saved)
	if e != nil {
		t.Fatal(e)
	}
	db.CancelCredentialOpsTask(ctx, a.ID)
	claim, e := db.ClaimCredentialOpsTask(ctx, "worker", time.Minute)
	if e != nil || claim == nil {
		t.Fatalf("claim %#v %v", claim, e)
	}
	if _, e = db.conn.ExecContext(ctx, "UPDATE accounts SET proxy_url='http://new-route:1234' WHERE id=$1", id); e != nil {
		t.Fatal(e)
	}
	if _, e = db.CommitCredentialOpsLogin(ctx, job.ID, "worker", claim.Attempt, map[string]any{"email": "route@example.com", "workspace_id": "ws", "access_token": "late"}); e == nil {
		t.Fatal("route change accepted stale login")
	}
}

func TestCredentialOperationsMonitorMembershipPauseAndTransient(t *testing.T) {
	db, e := New("sqlite", filepath.Join(t.TempDir(), "monitor.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	id, e := db.InsertAccountWithCredentials(ctx, "monitor", map[string]any{"email": "monitor@example.com", "access_token": "at"}, "")
	if e != nil {
		t.Fatal(e)
	}
	all, e := db.ListCredentialOpsMonitors(ctx)
	if e != nil || len(all) != 0 {
		t.Fatal("read enrolled an unmanaged account", e)
	}
	m := CredentialOpsMonitorRow{AccountID: id, Enabled: true, AutoRelogin: true, IntervalSeconds: 1800, FailureThreshold: 2, CooldownSeconds: 1800}
	if e = db.SaveCredentialOpsMonitor(ctx, m); e != nil {
		t.Fatal(e)
	}
	lease, e := db.ClaimCredentialOpsMonitor(ctx, id, "probe")
	if e != nil || lease == nil {
		t.Fatal(e)
	}
	if relogin, e := db.CompleteCredentialOpsMonitor(ctx, *lease, "auth"); e != nil || relogin {
		t.Fatal(e)
	}
	lease, _ = db.ClaimCredentialOpsMonitor(ctx, id, "probe")
	if relogin, e := db.CompleteCredentialOpsMonitor(ctx, *lease, "transient"); e != nil || relogin {
		t.Fatal("transient queued login", e)
	}
	all, _ = db.ListCredentialOpsMonitors(ctx)
	if len(all) != 1 || all[0].FailStreak != 1 || all[0].LastProbeAt == nil {
		t.Fatal("probe streak or timestamp lost")
	}
	lease, _ = db.ClaimCredentialOpsMonitor(ctx, id, "probe")
	m.Enabled = false
	if e = db.SaveCredentialOpsMonitor(ctx, m); e != nil {
		t.Fatal(e)
	}
	if _, e = db.CompleteCredentialOpsMonitor(ctx, *lease, "auth"); e == nil {
		t.Fatal("paused monitor accepted late probe")
	}
	if e = db.DeleteCredentialOpsMonitor(ctx, id); e != nil {
		t.Fatal(e)
	}
	all, e = db.ListCredentialOpsMonitors(ctx)
	if e != nil || len(all) != 0 {
		t.Fatal("removed monitor returned", e)
	}
}

func TestCredentialOperationsLeaseRestartCancelAndStaleAttempt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart.db")
	db, e := New("sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	cfg := CredentialOpsLoginConfigRow{ImportKey: "durable-restart", LoginEmail: "durable@example.com", CredentialMode: "password_totp", Engine: "local_worker", ProxySource: "direct", PasswordCiphertext: "sealed"}
	job, e := db.CreateCredentialOpsTaskWithConfig(ctx, 0, 0, cfg)
	if e != nil {
		t.Fatal(e)
	}
	claim, e := db.ClaimCredentialOpsTask(ctx, "worker", time.Minute)
	if e != nil || claim == nil {
		t.Fatal(e)
	}
	if _, e = db.conn.ExecContext(ctx, "UPDATE credential_operations_tasks SET lease_until=$1 WHERE id=$2", db.timeArg(time.Now().UTC().Add(-time.Minute)), job.ID); e != nil {
		t.Fatal(e)
	}
	db.Close()
	db, e = New("sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	newClaim, e := db.ClaimCredentialOpsTask(ctx, "worker", time.Minute)
	if e != nil || newClaim == nil || newClaim.Attempt != claim.Attempt+1 {
		t.Fatalf("reclaim = %#v %v", newClaim, e)
	}
	if e = db.FailCredentialOpsTask(ctx, job.ID, "worker", claim.Attempt); e == nil {
		t.Fatal("old attempt completed reclaimed lease")
	}
	if e = db.CancelCredentialOpsTask(ctx, job.ID); e != nil {
		t.Fatal(e)
	}
	if e = db.RenewCredentialOpsTask(ctx, job.ID, "worker", newClaim.Attempt, "late"); e == nil {
		t.Fatal("cancelled lease renewed")
	}
	next, e := db.RetryCredentialOpsTask(ctx, job.ID)
	if e != nil || next.ID == job.ID || next.Status != "queued" {
		t.Fatalf("durable retry %#v %v", next, e)
	}
	input, e := db.CredentialOpsTaskConfig(ctx, next.ID)
	if e != nil || input.LoginEmail != "durable@example.com" {
		t.Fatal("retry lost durable input", e)
	}
}

func TestCredentialOperationsProfileRefreshPendingSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grant-refresh.db")
	db, e := New("sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	tx, e := db.conn.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = db.putCredentialOpsGrantTx(ctx, tx, 42, "excel", map[string]any{"access_token": "old-at", "refresh_token": "single-use-rt"}); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if _, e = db.conn.ExecContext(ctx, "CREATE TRIGGER profile_refresh_requires_durability BEFORE UPDATE OF refresh_pending ON credential_operations_profile_grants WHEN NEW.refresh_pending=1 BEGIN SELECT CASE WHEN (SELECT synchronous FROM pragma_synchronous)<>2 THEN RAISE(ABORT,'refresh reservation must use FULL durability') END; END"); e != nil {
		t.Fatal(e)
	}
	grant, e := db.GetCredentialOpsProfileGrant(ctx, 42, "excel")
	if e != nil {
		t.Fatal(e)
	}
	if ok, e := db.BeginCredentialOpsProfileRefresh(ctx, 42, "excel", grant.Generation); e != nil || !ok {
		t.Fatal("refresh was not durably reserved", e)
	}
	if ok, e := db.BeginCredentialOpsProfileRefresh(ctx, 42, "excel", grant.Generation); e != nil || ok {
		t.Fatal("reserved single use refresh repeated", e)
	}
	db.Close()
	db, e = New("sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	grant, e = db.GetCredentialOpsProfileGrant(ctx, 42, "excel")
	if e != nil || !grant.RefreshPending {
		t.Fatal("restart lost refresh reservation", e)
	}
	if ok, e := db.UpdateCredentialOpsProfileGrantCAS(ctx, 42, "excel", grant.Generation, map[string]any{"access_token": "new-at", "refresh_token": "new-rt"}); e != nil || !ok {
		t.Fatal(e)
	}
	next, e := db.GetCredentialOpsProfileGrant(ctx, 42, "excel")
	if e != nil || next.RefreshPending || next.Generation != grant.Generation+1 {
		t.Fatal("new grant retained old reservation", e)
	}
	if ok, e := db.BeginCredentialOpsProfileRefresh(ctx, 42, "excel", grant.Generation); e != nil || ok {
		t.Fatal("old generation reserved new grant", e)
	}
}

func TestCredentialOperationsRetryPreservesExcelProfileAndRejectsRecoveryKinds(t *testing.T) {
	db, e := New("sqlite", filepath.Join(t.TempDir(), "retry-profile.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	id, e := db.InsertAccountWithCredentials(ctx, "retry", map[string]any{"plan_type": "plus", "email": "retry@example.com", "workspace_id": "ws", "access_token": "codex"}, "")
	if e != nil {
		t.Fatal(e)
	}
	cfg := CredentialOpsLoginConfigRow{AccountID: id, LoginEmail: "retry@example.com", CredentialMode: "password_totp", Engine: "inherit", ProxySource: "direct", PasswordCiphertext: "sealed"}
	if e = db.UpsertCredentialOpsLoginConfig(ctx, cfg); e != nil {
		t.Fatal(e)
	}
	saved, e := db.GetCredentialOpsLoginConfig(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	saved.OAuthProfile = "excel"
	row, e := db.GetAccountByID(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	original, e := db.CreateCredentialOpsTaskWithConfig(ctx, id, row.CredentialGeneration, *saved)
	if e != nil {
		t.Fatal(e)
	}
	if e = db.CancelCredentialOpsTask(ctx, original.ID); e != nil {
		t.Fatal(e)
	}
	retry, e := db.RetryCredentialOpsTask(ctx, original.ID)
	if e != nil {
		t.Fatal(e)
	}
	if retry.OAuthProfile != "excel" || retry.Kind != "login" {
		t.Fatalf("retry changed OAuth operation: %#v", retry)
	}
	input, e := db.CredentialOpsTaskConfig(ctx, retry.ID)
	if e != nil || input.OAuthProfile != "excel" || input.Engine != "local_worker" {
		t.Fatalf("retry input profile=%#v: %v", input, e)
	}
	excelClaim, e := db.ClaimCredentialOpsTaskWithCapabilities(ctx, "excel-worker", time.Minute, []string{"excel_oauth_v1"})
	if e != nil || excelClaim == nil {
		t.Fatalf("Excel retry claim %#v: %v", excelClaim, e)
	}
	if e = db.FailCredentialOpsTask(ctx, excelClaim.ID, "excel-worker", excelClaim.Attempt); e != nil {
		t.Fatal(e)
	}
	if _, e = db.RetryCredentialOpsTask(ctx, excelClaim.ID); e == nil {
		t.Fatal("Excel retry bypassed the acquisition cooldown")
	}
	native, e := db.GetAccountByID(ctx, id)
	if e != nil || native.GetCredential("access_token") != "codex" {
		t.Fatal("Excel retry changed primary credentials", e)
	}
	for _, kind := range []string{"totp_rotate", "totp_verify_new", "other_operation"} {
		task, e := db.CreateCredentialOpsTask(ctx, 0, 0)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = db.conn.ExecContext(ctx, "UPDATE credential_operations_tasks SET kind=$1 WHERE id=$2", kind, task.ID); e != nil {
			t.Fatal(e)
		}
		if _, e = db.RetryCredentialOpsTask(ctx, task.ID); e == nil {
			t.Fatalf("generic retry accepted %s", kind)
		}
	}
}

func TestCredentialOperationsEmailOTPClaimRequiresModeCapability(t *testing.T) {
	db, e := New("sqlite", filepath.Join(t.TempDir(), "email-capability.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	ctx := context.Background()
	job, e := db.CreateCredentialOpsTaskWithConfig(ctx, 0, 0, CredentialOpsLoginConfigRow{LoginEmail: "mail@example.com", CredentialMode: "email_otp_url", Engine: "local_worker", ProxySource: "direct", OTPURLCiphertext: "sealed"})
	if e != nil {
		t.Fatal(e)
	}
	if claim, e := db.ClaimCredentialOpsTaskWithCapabilities(ctx, "password-only", time.Minute, []string{"login_codex_v1"}); e != nil || claim != nil {
		t.Fatalf("password-only worker received mailbox task %#v: %v", claim, e)
	}
	claim, e := db.ClaimCredentialOpsTaskWithCapabilities(ctx, "email-worker", time.Minute, []string{"login_codex_v1", "email_otp_v1"})
	if e != nil || claim == nil || claim.ID != job.ID {
		t.Fatalf("capable claim %#v: %v", claim, e)
	}
}

func TestCredentialOperationsDurableTransactionRestoresSQLiteDefault(t *testing.T) {
	db, e := New("sqlite", filepath.Join(t.TempDir(), "durable-commit.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.conn.SetMaxOpenConns(1)
	db.conn.SetMaxIdleConns(1)
	ctx := context.Background()
	var initial int
	if e = db.conn.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&initial); e != nil {
		t.Fatal(e)
	}
	if e = db.withCredentialOpsDurableTx(ctx, func(tx *sql.Tx) error {
		var mode int
		if e := tx.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&mode); e != nil {
			return e
		}
		if mode != 2 {
			return fmt.Errorf("candidate transaction synchronous=%d, want FULL", mode)
		}
		_, e := tx.ExecContext(ctx, "INSERT INTO credential_operations_settings(key,value) VALUES('durability-test','persisted')")
		return e
	}); e != nil {
		t.Fatal(e)
	}
	var after int
	if e = db.conn.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&after); e != nil || after != initial {
		t.Fatalf("native SQLite synchronous changed from %d to %d: %v", initial, after, e)
	}
}

func TestCredentialOperationsExcelEligibilityRequiresPaidOwner(t *testing.T) {
	for _, tc := range []struct {
		plan  string
		extra map[string]any
		want  bool
	}{
		{"plus", nil, true}, {"chatgpt-pro-20x", nil, true}, {"team", nil, true}, {"business", nil, true}, {"enterprise", nil, true}, {"edu", nil, true}, {"go", nil, true},
		{"free", nil, false}, {"", nil, false}, {"unknown", nil, false}, {"plus", map[string]any{"parent_account_id": 42}, false}, {"pro", map[string]any{"is_shadow": true}, false},
	} {
		row := &AccountRow{Platform: "openai", Type: "oauth", Status: "active", Credentials: map[string]any{"plan_type": tc.plan}}
		for key, value := range tc.extra {
			row.Credentials[key] = value
		}
		if got := CredentialOperationsExcelEligible(row); got != tc.want {
			t.Fatalf("Excel eligibility plan=%q extra=%v: got %t want %t", tc.plan, tc.extra, got, tc.want)
		}
		if tc.plan == "free" && !CredentialOperationsEligible(row) {
			t.Fatal("Free plan excluded from ordinary Codex operations")
		}
	}
	db, e := New("sqlite", filepath.Join(t.TempDir(), "excel-free.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	id, e := db.InsertAccountWithCredentials(ctx, "Free", map[string]any{"email": "free@example.com", "access_token": "token", "plan_type": "free"}, "")
	if e != nil {
		t.Fatal(e)
	}
	cfg := CredentialOpsLoginConfigRow{AccountID: id, LoginEmail: "free@example.com", CredentialMode: "password_totp", Engine: "inherit", ProxySource: "direct", PasswordCiphertext: "sealed"}
	if e = db.UpsertCredentialOpsLoginConfig(ctx, cfg); e != nil {
		t.Fatal(e)
	}
	saved, _ := db.GetCredentialOpsLoginConfig(ctx, id)
	row, _ := db.GetAccountByID(ctx, id)
	saved.OAuthProfile = "excel"
	if _, e = db.CreateCredentialOpsTaskWithConfig(ctx, id, row.CredentialGeneration, *saved); e == nil {
		t.Fatal("manual Excel acquisition accepted Free account")
	}
	saved.OAuthProfile = "codex"
	if _, e = db.CreateCredentialOpsTaskWithConfig(ctx, id, row.CredentialGeneration, *saved); e != nil {
		t.Fatal("Free Codex operation rejected", e)
	}
}

func TestCredentialOperationsMissingExcelFiltersFreeAndShadowOwners(t *testing.T) {
	db, e := New("sqlite", filepath.Join(t.TempDir(), "excel-candidates.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	var paidID int64
	for _, entry := range []struct {
		name, plan string
		parent     any
	}{{"Free", "free", nil}, {"Paid", "chatgpt-pro-20x", nil}, {"Shadow", "plus", 42}} {
		credentials := map[string]any{"email": strings.ToLower(entry.name) + "@example.com", "access_token": "token", "plan_type": entry.plan}
		if entry.parent != nil {
			credentials["parent_account_id"] = entry.parent
		}
		id, e := db.InsertAccountWithCredentials(ctx, entry.name, credentials, "")
		if e != nil {
			t.Fatal(e)
		}
		cfg := CredentialOpsLoginConfigRow{AccountID: id, LoginEmail: credentials["email"].(string), CredentialMode: "password_totp", Engine: "inherit", ProxySource: "direct", PasswordCiphertext: "sealed"}
		if e = db.UpsertCredentialOpsLoginConfig(ctx, cfg); e != nil {
			t.Fatal(e)
		}
		if entry.name == "Paid" {
			paidID = id
		}
	}
	ids, e := db.ListCredentialOpsMissingExcelAccounts(ctx, true)
	if e != nil || len(ids) != 1 || ids[0] != paidID {
		t.Fatalf("missing grant candidates %v, want only paid owner %d: %v", ids, paidID, e)
	}
}

func TestCredentialOperationsConcurrentFirstImportRetryUsesActiveLineage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retry-lineage.db")
	db, e := New("sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { db.Close() }()
	ctx := context.Background()
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	source, e := db.CreateCredentialOpsTaskWithConfig(ctx, 0, 0, CredentialOpsLoginConfigRow{ImportKey: "lineage-source", LoginEmail: "lineage@example.com", CredentialMode: "password_totp", Engine: "local_worker", ProxySource: "direct", PasswordCiphertext: "sealed"})
	if e != nil {
		t.Fatal(e)
	}
	if e = db.CancelCredentialOpsTask(ctx, source.ID); e != nil {
		t.Fatal(e)
	}
	concurrentRetry := func(sources ...int64) *CredentialOpsTaskRow {
		const count = 8
		type result struct {
			task *CredentialOpsTaskRow
			err  error
		}
		results := make(chan result, count)
		start := make(chan struct{})
		for i := 0; i < count; i++ {
			id := sources[i%len(sources)]
			go func() { <-start; task, err := db.RetryCredentialOpsTask(ctx, id); results <- result{task, err} }()
		}
		close(start)
		unique := map[int64]*CredentialOpsTaskRow{}
		var firstErr error
		for i := 0; i < count; i++ {
			r := <-results
			if r.err != nil {
				firstErr = r.err
			} else if r.task != nil {
				unique[r.task.ID] = r.task
			}
		}
		if firstErr != nil {
			t.Fatal(firstErr)
		}
		if len(unique) != 1 {
			t.Fatalf("concurrent retries queued %d distinct jobs, want one: %v", len(unique), unique)
		}
		for _, task := range unique {
			return task
		}
		return nil
	}
	first := concurrentRetry(source.ID)
	var count int
	if e = db.conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM credential_operations_tasks").Scan(&count); e != nil || count != 2 {
		t.Fatalf("first retry task count=%d: %v", count, e)
	}
	claim, e := db.ClaimCredentialOpsTask(ctx, "retry-worker", time.Minute)
	if e != nil || claim == nil || claim.ID != first.ID {
		t.Fatalf("claim %#v: %v", claim, e)
	}
	if e = db.FailCredentialOpsTask(ctx, claim.ID, "retry-worker", claim.Attempt); e != nil {
		t.Fatal(e)
	}
	db.Close()
	db, e = New("sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	next := concurrentRetry(source.ID, first.ID)
	if next.ID == first.ID || next.Status != "queued" {
		t.Fatal("failed retry child remained pinned")
	}
	claim, e = db.ClaimCredentialOpsTask(ctx, "retry-worker", time.Minute)
	if e != nil || claim == nil || claim.ID != next.ID {
		t.Fatalf("second claim %#v: %v", claim, e)
	}
	token := workspaceIdentityJWT(t, "lineage@example.com", "workspace-lineage")
	account, e := db.CommitCredentialOpsLogin(ctx, claim.ID, "retry-worker", claim.Attempt, map[string]any{"email": "lineage@example.com", "workspace_id": "workspace-lineage", "access_token": token, "id_token": token, "refresh_token": "lineage-refresh"})
	if e != nil {
		t.Fatal(e)
	}
	completed := concurrentRetry(source.ID, first.ID)
	if completed.ID != next.ID || completed.Status != "succeeded" || completed.AccountID != account {
		t.Fatalf("successful retry was not reused: %#v", completed)
	}
	if e = db.conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM credential_operations_tasks").Scan(&count); e != nil || count != 3 {
		t.Fatalf("lineage task count=%d, want source and two attempts: %v", count, e)
	}
}

func TestCredentialOperationsRetryRejectsStaleResolvedConfig(t *testing.T) {
	db, e := New("sqlite", filepath.Join(t.TempDir(), "retry-revision.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	id, e := db.InsertAccountWithCredentials(ctx, "revision", map[string]any{"email": "revision@example.com", "access_token": "token"}, "http://route-b:9001")
	if e != nil {
		t.Fatal(e)
	}
	cfg := CredentialOpsLoginConfigRow{AccountID: id, LoginEmail: "revision@example.com", CredentialMode: "password_totp", Engine: "inherit", ProxySource: "global", PasswordCiphertext: "sealed"}
	if e = db.UpsertCredentialOpsLoginConfig(ctx, cfg); e != nil {
		t.Fatal(e)
	}
	before, e := db.GetCredentialOpsLoginConfig(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	row, _ := db.GetAccountByID(ctx, id)
	before.ResolvedProxyURL = "http://route-a:9000"
	before.RouteResolved = true
	source, e := db.CreateCredentialOpsTaskWithConfig(ctx, id, row.CredentialGeneration, *before)
	if e != nil {
		t.Fatal(e)
	}
	if e = db.CancelCredentialOpsTask(ctx, source.ID); e != nil {
		t.Fatal(e)
	}
	edited := *before
	edited.ProxySource = "account"
	if e = db.UpsertCredentialOpsLoginConfig(ctx, edited); e != nil {
		t.Fatal(e)
	}
	if _, e = db.RetryCredentialOpsTask(ctx, source.ID, CredentialOpsRetryRoute{ProxyURL: "http://route-a:9000", ConfigRevision: before.Revision}); !errors.Is(e, ErrCredentialOpsStale) {
		t.Fatalf("retry accepted a route from older config revision: %v", e)
	}
	tasks, e := db.ListCredentialOpsTasks(ctx)
	if e != nil || len(tasks) != 1 {
		t.Fatalf("stale retry inserted a job: %d %v", len(tasks), e)
	}
}

func TestCredentialOperationsTOTPSnapshotRejectsConcurrentRouteEdit(t *testing.T) {
	db, e := New("sqlite", filepath.Join(t.TempDir(), "totp-route-revision.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	id, e := db.InsertAccountWithCredentials(ctx, "totp route", map[string]any{"email": "totp-route@example.test", "access_token": "native"}, "http://account-b:9001")
	if e != nil {
		t.Fatal(e)
	}
	cfg := CredentialOpsLoginConfigRow{AccountID: id, LoginEmail: "totp-route@example.test", CredentialMode: "password_totp", Engine: "local_worker", ProxySource: "global", PasswordCiphertext: "sealed", TOTPCiphertext: "sealed"}
	if e = db.UpsertCredentialOpsLoginConfig(ctx, cfg); e != nil {
		t.Fatal(e)
	}
	before, _ := db.GetCredentialOpsLoginConfig(ctx, id)
	// The handler resolved A using this revision; an edit commits before task creation.
	edited := *before
	edited.ProxySource = "account"
	if e = db.UpsertCredentialOpsLoginConfig(ctx, edited); e != nil {
		t.Fatal(e)
	}
	if _, e = db.CreateCredentialOpsTOTPTask(ctx, id, "rotate", CredentialOpsRetryRoute{ProxyURL: "http://global-a:9000", ConfigRevision: before.Revision}); !errors.Is(e, ErrCredentialOpsStale) {
		t.Fatalf("rotation accepted older route snapshot: %v", e)
	}
	tasks, e := db.ListCredentialOpsTasks(ctx)
	if e != nil || len(tasks) != 0 {
		t.Fatalf("stale route queued mutation: %#v %v", tasks, e)
	}
	current, _ := db.GetCredentialOpsLoginConfig(ctx, id)
	task, e := db.CreateCredentialOpsTOTPTask(ctx, id, "rotate", CredentialOpsRetryRoute{ProxyURL: "http://account-b:9001", ConfigRevision: current.Revision})
	if e != nil {
		t.Fatal(e)
	}
	input, _ := db.CredentialOpsTaskConfig(ctx, task.ID)
	_, dec := credentialops.EnvCrypto()
	route, e := dec(input.ProxyURLCiphertext)
	if e != nil || route != "http://account-b:9001" {
		t.Fatalf("current route was not preserved: %q %v", route, e)
	}
}

func TestCredentialRuntimeStateRejectsReplacedGeneration(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "runtime-generation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	id, err := db.InsertAccountWithCredentials(ctx, "generation fence", map[string]any{"access_token": "synthetic"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.ExecContext(ctx, "UPDATE accounts SET credential_generation=2,enabled=FALSE WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	for _, state := range []string{"cooldown", "error", "clear_error", "clear_cooldown"} {
		applied, err := db.UpdateAccountRuntimeStateForGeneration(ctx, id, 1, state, "stale failure", "unauthorized", &deadline)
		if err != nil || applied {
			t.Fatalf("stale %s applied=%v err=%v", state, applied, err)
		}
	}
	row, err := db.GetAccountByID(ctx, id)
	if err != nil || row.Status != "active" || row.ErrorMessage != "" || row.CooldownUntil.Valid || row.Enabled {
		t.Fatalf("stale mutation changed account: %+v %v", row, err)
	}
	if ok, err := db.UpdateAccountRuntimeStateForGeneration(ctx, id, 2, "cooldown", "new failure", "unauthorized", &deadline); err != nil || !ok {
		t.Fatalf("current cooldown: %v %v", ok, err)
	}
	row, err = db.GetAccountByID(ctx, id)
	if err != nil || row.CooldownReason != "unauthorized" || row.ErrorMessage != "new failure" || row.Enabled {
		t.Fatalf("current mutation missing: %+v %v", row, err)
	}
	if ok, err := db.UpdateAccountRuntimeStateForGeneration(ctx, id, 1, "clear_error", "", "", nil); err != nil || ok {
		t.Fatalf("stale clear: %v %v", ok, err)
	}
	row, err = db.GetAccountByID(ctx, id)
	if err != nil || row.CooldownReason != "unauthorized" {
		t.Fatalf("stale clear erased current failure: %+v %v", row, err)
	}
}
