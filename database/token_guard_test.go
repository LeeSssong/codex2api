package database

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"testing"
	"time"
)

func guardEnable(t *testing.T, db *DB) int64 {
	t.Helper()
	ctx := context.Background()
	_, err := db.conn.ExecContext(ctx, "INSERT INTO account_ops_settings(key,value) VALUES('module_enabled','true') ON CONFLICT(key) DO UPDATE SET value='true'")
	if err != nil {
		t.Fatal(err)
	}
	_, v, err := db.TokenGuardConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func guardJWT(email, workspace string) string {
	return "e30." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"email":%q,"https://api.openai.com/auth":{"chatgpt_account_id":%q}}`, email, workspace))) + ".signature"
}
func TestTokenGuardDurableClaimCancelConfigFence(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			db := guardTestDB(t, driver)
			ctx := context.Background()
			version := guardEnable(t, db)
			job, err := db.CreateTokenGuardJob(ctx, "cycle", 0, version)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.CreateTokenGuardJob(ctx, "cycle", 0, version); !errors.Is(err, ErrTokenGuardBusy) {
				t.Fatalf("duplicate accepted: %v", err)
			}
			claimed, err := db.ClaimTokenGuardJob(ctx, "owner-a", time.Now())
			if err != nil || claimed == nil {
				t.Fatalf("claim: %v", err)
			}
			if second, err := db.ClaimTokenGuardJob(ctx, "owner-b", time.Now()); err != nil || second != nil {
				t.Fatalf("second owner admitted: %v", err)
			}
			ok, err := db.CancelTokenGuardJob(ctx, job.ID)
			if err != nil || !ok {
				t.Fatalf("cancel: %v", err)
			}
			if err = db.RenewTokenGuardJob(ctx, *claimed, time.Now()); !errors.Is(err, ErrTokenGuardStale) {
				t.Fatalf("cancelled owner renewed: %v", err)
			}
			job, err = db.CreateTokenGuardJob(ctx, "cycle", 0, version)
			if err != nil {
				t.Fatal(err)
			}
			claimed, err = db.ClaimTokenGuardJob(ctx, "owner-b", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.SaveTokenGuardConfig(ctx, "{}", version); err != nil {
				t.Fatal(err)
			}
			if err = db.FinishTokenGuardJob(ctx, *claimed, "completed", "{}", "ok"); !errors.Is(err, ErrTokenGuardStale) {
				t.Fatalf("old config committed: %v", err)
			}
			stored, err := db.TokenGuardJob(ctx, job.ID)
			if err != nil || stored.State != "cancelled" {
				t.Fatalf("changed config did not cancel job: %+v %v", stored, err)
			}
		})
	}
}
func TestTokenGuardPublicationPreservesControlsAndConsumptionJournal(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			db := guardTestDB(t, driver)
			ctx := context.Background()
			version := guardEnable(t, db)
			rt := "old-" + uuid.NewString()
			old := guardJWT("a@example.com", "workspace-a")
			id, err := db.InsertAccountWithCredentials(ctx, "guard-account", map[string]any{"refresh_token": rt, "access_token": old, "id_token": old, "email": "a@example.com", "account_id": "workspace-a", "custom_headers": map[string]string{"Chatgpt-Account-Id": "route-workspace"}, "models": []string{"keep-model"}}, "proxy-route")
			if err != nil {
				t.Fatal(err)
			}
			snap, err := db.TokenGuardAccount(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.ObserveCodexCapability(ctx, id, CodexCapability{CredentialGeneration: snap.Generation, Upstream: CodexPathBasispoints, Model: "guard-model", Capability: CapabilitySupported, Source: "probe", ObservedAt: time.Now().UnixNano()}); err != nil {
				t.Fatal(err)
			}
			if _, err := db.conn.ExecContext(ctx, "INSERT INTO account_codex_capabilities(account_id,upstream,model,capability,credential_generation) VALUES($1,'basispoints','legacy-model','supported',0)", id); err != nil {
				t.Fatal(err)
			}
			originalRow, err := db.GetAccountByID(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			attempt, err := db.BeginCodexRefresh(ctx, id, snap.Generation, rt)
			if err != nil {
				t.Fatal(err)
			}
			_, err = db.CreateTokenGuardJob(ctx, "relogin", id, version)
			if err != nil {
				t.Fatal(err)
			}
			job, err := db.ClaimTokenGuardJob(ctx, "publisher", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			fresh, err := db.PublishTokenGuardCredentials(ctx, *job, *snap, map[string]any{"refresh_token": "new-" + rt, "access_token": old, "id_token": old, "proxy_url": "evil", "account_id": "evil"})
			if err != nil {
				t.Fatal(err)
			}
			if fresh.FamilyID != originalRow.CredentialFamilyID || fresh.Credentials["credential_family_id"] != originalRow.CredentialFamilyID {
				t.Fatal("fresh authentication replaced stable credential family")
			}
			if fresh.Generation != snap.Generation+1 || fresh.Credentials["account_id"] != "workspace-a" || fresh.Credentials["proxy_url"] != nil {
				t.Fatal("generation or credential whitelist violated")
			}
			_, facts, factErr := db.GetCodexRoutes(ctx, id)
			if factErr != nil {
				t.Fatal(factErr)
			}
			for _, fact := range facts {
				if fact.Capability == CapabilitySupported {
					t.Fatal("fresh login inherited stale BPS capability evidence")
				}
			}
			var count int
			if err := db.conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM codex_oauth_refresh_attempts WHERE attempt_id=$1", attempt.ID).Scan(&count); err != nil || count != 1 {
				t.Fatal("publication cleared consumption journal")
			}
			if err := db.SetAccountEnabled(ctx, id, false); err != nil {
				t.Fatal(err)
			}
			if _, err = db.PublishTokenGuardCredentials(ctx, *job, *fresh, map[string]any{"refresh_token": "late", "access_token": old, "id_token": old}); !errors.Is(err, ErrTokenGuardStale) {
				t.Fatalf("manual control overwritten: %v", err)
			}
		})
	}
}
func TestTokenGuardRecoveryOwnershipAndCooldown(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			db := guardTestDB(t, driver)
			ctx := context.Background()
			v := guardEnable(t, db)
			id, e := db.InsertAccountWithCredentials(ctx, "guard-owner", map[string]any{"refresh_token": "owner-" + uuid.NewString()}, "")
			if e != nil {
				t.Fatal(e)
			}
			_, e = db.CreateTokenGuardJob(ctx, "cycle", 0, v)
			if e != nil {
				t.Fatal(e)
			}
			j, e := db.ClaimTokenGuardJob(ctx, "owner", time.Now())
			if e != nil {
				t.Fatal(e)
			}
			a, _ := db.TokenGuardAccount(ctx, id)
			isolated, changed, e := db.IsolateTokenGuardAccount(ctx, *j, *a)
			if e != nil || !changed {
				t.Fatalf("isolate %v", e)
			}
			if isolated.Revision <= a.Revision {
				t.Fatal("ownership lacks revision")
			}
			restored, changed, e := db.RecoverTokenGuardAccount(ctx, *j, *isolated)
			if e != nil || !changed || !restored.Schedulable() {
				t.Fatalf("restore: %+v %v", restored, e)
			}
			isolated, _, e = db.IsolateTokenGuardAccount(ctx, *j, *restored)
			if e != nil {
				t.Fatal(e)
			}
			if e = db.SetAccountEnabled(ctx, id, false); e != nil {
				t.Fatal(e)
			}
			if e = db.SetAccountEnabled(ctx, id, true); e != nil {
				t.Fatal(e)
			}
			a, _ = db.TokenGuardAccount(ctx, id)
			restored, changed, e = db.RecoverTokenGuardAccount(ctx, *j, *a)
			if e != nil || changed || restored.Status != "error" {
				t.Fatalf("manual ABA incorrectly recovered: %+v %v", restored, e)
			}
			// Native concurrent 429 status must also win over an old ownership record.
			_, e = db.conn.ExecContext(ctx, "UPDATE accounts SET cooldown_reason='rate_limit',cooldown_until=$1 WHERE id=$2", db.timeArg(time.Now().Add(time.Hour)), id)
			if e != nil {
				t.Fatal(e)
			}
			a, _ = db.TokenGuardAccount(ctx, id)
			restored, changed, e = db.RecoverTokenGuardAccount(ctx, *j, *a)
			if e != nil || changed || restored.CooldownReason != "rate_limit" {
				t.Fatal("cooldown lost")
			}
		})
	}
}
func TestTokenGuardExpiredOwnerAndModuleABAFenced(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			db := guardTestDB(t, driver)
			ctx := context.Background()
			v := guardEnable(t, db)
			queued, e := db.CreateTokenGuardJob(ctx, "cycle", 0, v)
			if e != nil {
				t.Fatal(e)
			}
			j, e := db.ClaimTokenGuardJob(ctx, "crashed", time.Now().Add(-2*time.Minute))
			if e != nil {
				t.Fatal(e)
			}
			if _, e = db.CreateTokenGuardJob(ctx, "cycle", 0, v); e != nil {
				t.Fatal("expired running job blocked restart", e)
			}
			if e = db.FinishTokenGuardJob(ctx, *j, "completed", "{}", "late"); !errors.Is(e, ErrTokenGuardStale) {
				t.Fatalf("expired result accepted: %v", e)
			}
			old, e := db.TokenGuardJob(ctx, queued.ID)
			if e != nil || old.State != "failed" {
				t.Fatalf("expired outcome: %+v %v", old, e)
			}
			j, e = db.ClaimTokenGuardJob(ctx, "restarted", time.Now())
			if e != nil {
				t.Fatal(e)
			}
			for _, enabled := range []string{"false", "true"} {
				if _, e = db.conn.ExecContext(ctx, "UPDATE account_ops_settings SET value=$1 WHERE key='module_enabled'", enabled); e != nil {
					t.Fatal(e)
				}
			}
			if e = db.FinishTokenGuardJob(ctx, *j, "completed", "{}", "late"); !errors.Is(e, ErrTokenGuardStale) {
				t.Fatalf("module ABA accepted: %v", e)
			}
		})
	}
}

func TestTokenGuardScopeSnapshotAndMovementFence(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			db := guardTestDB(t, driver)
			ctx := context.Background()
			v := guardEnable(t, db)
			id, e := db.InsertAccountWithCredentials(ctx, "scope", map[string]any{"refresh_token": "scope-" + uuid.NewString()}, "")
			if e != nil {
				t.Fatal(e)
			}
			group, e := db.CreateAccountGroup(ctx, "scope-"+uuid.NewString(), "", "", 0, 0, sql.NullInt64{})
			if e != nil {
				t.Fatal(e)
			}
			if e = db.SetAccountGroups(ctx, id, []int64{group}); e != nil {
				t.Fatal(e)
			}
			scoped, e := db.TokenGuardAccounts(ctx, []int64{group}, 1)
			if e != nil || len(scoped) != 1 {
				t.Fatal("scope lookup failed", e)
			}
			if _, e = db.CreateTokenGuardJob(ctx, "cycle", 0, v); e != nil {
				t.Fatal(e)
			}
			j, e := db.ClaimTokenGuardJob(ctx, "scope", time.Now())
			if e != nil {
				t.Fatal(e)
			}
			if e = db.SetAccountGroups(ctx, id, nil); e != nil {
				t.Fatal(e)
			}
			if _, _, e = db.IsolateTokenGuardAccount(ctx, *j, scoped[0]); !errors.Is(e, ErrTokenGuardStale) {
				t.Fatal("post-selection group movement not fenced", e)
			}
			scoped, e = db.TokenGuardAccounts(ctx, []int64{group}, 1)
			if e != nil || len(scoped) != 0 {
				t.Fatal("removed member remained in scope", e)
			}
		})
	}
}
