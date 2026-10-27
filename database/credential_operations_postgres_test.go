package database

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/codex2api/credentialops"
)

// This suite uses an explicitly supplied isolated database and actual pgx SQL,
// so transaction locks/types are exercised independently from SQLite fixtures.
func TestCredentialOperationsPostgresLifecycle(t *testing.T) {
	dsn := os.Getenv("CODEX2API_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("requires isolated CODEX2API_TEST_POSTGRES_DSN")
	}
	control, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	name := fmt.Sprintf("credentials_%d", time.Now().UnixNano())
	if _, err = control.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	if _, err = control.Exec("ALTER DATABASE " + name + " SET synchronous_commit TO off"); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = control.Exec("DROP DATABASE " + name + " WITH (FORCE)") }()
	address, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	address.Path = "/" + name
	dsn = address.String()
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	db, err := New("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	// A private trigger proves that a candidate's ACK forces synchronous
	// durability even when every new pool connection defaults to async commit.
	if _, err = db.conn.ExecContext(ctx, `CREATE FUNCTION credential_test_require_sync() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='prepared' AND current_setting('synchronous_commit')<>'on' THEN RAISE EXCEPTION 'candidate ACK is not synchronous'; END IF; RETURN NEW; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.conn.ExecContext(ctx, `CREATE TRIGGER credential_test_require_sync BEFORE UPDATE ON credential_operations_totp_recoveries FOR EACH ROW EXECUTE FUNCTION credential_test_require_sync()`); err != nil {
		t.Fatal(err)
	}
	prepared, err := credentialops.PrepareConfig(credentialops.LoginConfigInput{Email: "pg-credential@example.test", Password: "private-password", TOTPSecret: "JBSWY3DPEHPK3PXP", ProxySource: "direct"}, credentialops.LoginConfig{})
	if err != nil {
		t.Fatal(err)
	}
	cfg := CredentialOpsLoginConfigRow{ImportKey: "pg-credential-first-import", LoginEmail: prepared.Email, CredentialMode: prepared.Mode, Engine: prepared.Engine, ProxySource: prepared.ProxySource, PasswordCiphertext: prepared.PasswordCiphertext, TOTPCiphertext: prepared.TOTPCiphertext}
	job, err := db.CreateCredentialOpsTaskWithConfig(ctx, 0, 0, cfg)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := db.CreateCredentialOpsTaskWithConfig(ctx, 0, 0, cfg)
	if err != nil || duplicate.ID != job.ID {
		t.Fatalf("idempotency %v", err)
	}
	claim, err := db.ClaimCredentialOpsTask(ctx, "pg-worker", time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("claim %v", err)
	}
	id, err := db.CommitCredentialOpsLogin(ctx, claim.ID, "pg-worker", claim.Attempt, map[string]any{"email": "pg-credential@example.test", "workspace_id": "pg-workspace", "plan_type": "plus", "access_token": "codex-pg-at", "refresh_token": "codex-pg-rt"})
	if err != nil {
		t.Fatal(err)
	}
	row, err := db.GetAccountByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := db.GetCredentialOpsLoginConfig(ctx, id)
	if err != nil || saved == nil {
		t.Fatalf("config %v", err)
	}
	saved.OAuthProfile = "excel"
	saved.Engine = "local_worker"
	excel, err := db.CreateCredentialOpsTaskWithConfig(ctx, id, row.CredentialGeneration, *saved)
	if err != nil {
		t.Fatal(err)
	}
	claim, err = db.ClaimCredentialOpsTaskWithCapabilities(ctx, "pg-worker", time.Minute, []string{"excel_oauth_v1"})
	if err != nil || claim == nil || claim.ID != excel.ID {
		t.Fatalf("Excel claim %v", err)
	}
	if _, err = db.CommitCredentialOpsLogin(ctx, claim.ID, "pg-worker", claim.Attempt, map[string]any{"email": "pg-credential@example.test", "workspace_id": "pg-workspace", "access_token": "excel-pg-at", "refresh_token": "excel-pg-rt"}); err != nil {
		t.Fatal(err)
	}
	grant, err := db.GetCredentialOpsProfileGrant(ctx, id, "excel")
	if err != nil || grant == nil {
		t.Fatalf("grant %v", err)
	}
	if ok, err := db.BeginCredentialOpsProfileRefresh(ctx, id, "excel", grant.Generation); err != nil || !ok {
		t.Fatal("durable refresh reservation failed", err)
	}
	if ok, err := db.BeginCredentialOpsProfileRefresh(ctx, id, "excel", grant.Generation); err != nil || ok {
		t.Fatal("refresh reservation repeated", err)
	}
	if ok, err := db.UpdateCredentialOpsProfileGrantCAS(ctx, id, "excel", grant.Generation, map[string]any{"access_token": "new-excel", "refresh_token": "new-excel-rt"}); err != nil || !ok {
		t.Fatal("grant CAS", err)
	}
	after, _ := db.GetAccountByID(ctx, id)
	if after.CredentialGeneration != row.CredentialGeneration || after.GetCredential("access_token") != "codex-pg-at" {
		t.Fatal("Excel grant damaged native Codex credential")
	}
	rotation, err := db.CreateCredentialOpsTOTPTask(ctx, id, "rotate")
	if err != nil {
		t.Fatal(err)
	}
	claim, err = db.ClaimCredentialOpsTaskWithCapabilities(ctx, "pg-worker", time.Minute, []string{"totp_rotation_v1"})
	if err != nil || claim == nil || claim.ID != rotation.ID {
		t.Fatalf("rotation claim %v", err)
	}
	for _, phase := range []string{"enrolling", "prepared", "activating", "verifying"} {
		secret := ""
		if phase == "prepared" {
			secret = "JBSWY3DPEHPK3PXX"
		}
		if err = db.CredentialOpsTOTPPhase(ctx, claim.ID, "pg-worker", claim.Attempt, phase, secret); err != nil {
			t.Fatalf("phase %s: %v", phase, err)
		}
	}
	if err = db.CredentialOpsTOTPFinish(ctx, claim.ID, "pg-worker", claim.Attempt, true); err != nil {
		t.Fatal(err)
	}
	recovery, err := db.GetCredentialOpsTOTPRecovery(ctx, id)
	if err != nil || recovery.State != "adopted" {
		t.Fatalf("recovery %#v %v", recovery, err)
	}
	if err = db.EnsureCredentialOperationsSchema(ctx); err != nil {
		t.Fatal("repeat schema", err)
	}
	grant, err = db.GetCredentialOpsProfileGrant(ctx, id, "excel")
	if err != nil || grant.RefreshPending || grant.Credentials["access_token"] != "new-excel" {
		t.Fatal("reinitialization lost profile state", err)
	}
	// Hold the real phase fence, then try to cancel through another SQL pool
	// connection. Cancellation must wait, then a later callback must be stale.
	rotation, err = db.CreateCredentialOpsTOTPTask(ctx, id, "rotate")
	if err != nil {
		t.Fatal(err)
	}
	claim, err = db.ClaimCredentialOpsTaskWithCapabilities(ctx, "pg-worker", time.Minute, []string{"totp_rotation_v1"})
	if err != nil || claim == nil {
		t.Fatalf("second rotation %v", err)
	}
	fenced, err := db.conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = db.credentialOpsFenceTx(ctx, fenced, claim.ID, "pg-worker", claim.Attempt); err != nil {
		fenced.Rollback()
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	err = db.CancelCredentialOpsTask(short, rotation.ID)
	cancel()
	if err == nil {
		fenced.Rollback()
		t.Fatal("PostgreSQL cancellation bypassed the active TOTP fence")
	}
	fenced.Rollback()
	if err = db.CancelCredentialOpsTask(ctx, rotation.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.CredentialOpsTOTPPhase(ctx, claim.ID, "pg-worker", claim.Attempt, "enrolling", ""); err == nil {
		t.Fatal("cancelled TOTP callback was accepted")
	}
	t.Run("concurrent retry lineage", func(t *testing.T) {
		cfg.ImportKey = "pg-retry-lineage"
		source, err := db.CreateCredentialOpsTaskWithConfig(ctx, 0, 0, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err = db.CancelCredentialOpsTask(ctx, source.ID); err != nil {
			t.Fatal(err)
		}
		retry := func(ids ...int64) int64 {
			t.Helper()
			type outcome struct {
				id  int64
				err error
			}
			const concurrency = 8
			start := make(chan struct{})
			results := make(chan outcome, concurrency)
			for i := 0; i < concurrency; i++ {
				id := ids[i%len(ids)]
				go func() {
					<-start
					job, e := db.RetryCredentialOpsTask(ctx, id)
					if job == nil {
						results <- outcome{err: e}
						return
					}
					results <- outcome{id: job.ID, err: e}
				}()
			}
			close(start)
			var child int64
			for i := 0; i < concurrency; i++ {
				r := <-results
				if r.err != nil || r.id == 0 {
					t.Errorf("concurrent retry: %v", r.err)
					continue
				}
				if child != 0 && child != r.id {
					t.Errorf("retry created distinct children: %d and %d", child, r.id)
				}
				child = r.id
			}
			if t.Failed() {
				t.FailNow()
			}
			return child
		}
		first := retry(source.ID)
		claimed, err := db.ClaimCredentialOpsTask(ctx, "pg-retry-worker", time.Minute)
		if err != nil || claimed == nil || claimed.ID != first {
			t.Fatalf("retry claim: %#v, %v", claimed, err)
		}
		if err = db.FailCredentialOpsTask(ctx, claimed.ID, "pg-retry-worker", claimed.Attempt); err != nil {
			t.Fatal(err)
		}
		if second := retry(source.ID, first); second == first {
			t.Fatal("failed retry blocked a new attempt")
		}
	})
}
