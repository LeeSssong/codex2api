package admin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/cache"
	"github.com/codex2api/credentialops"
	"github.com/codex2api/database"
	"github.com/codex2api/proxy"
	"github.com/gin-gonic/gin"
)

// A regression test for a successful worker completion reviving an account in
// SQL but retaining the old authorization failure in the shared cooldown cache.
// All credentials and probes are synthetic; no network or production data.
func TestCredentialOpsReloginMustClearCachedUnauthorized(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	ctx := context.Background()
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "relogin-state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tc := cache.NewMemory(4)
	defer tc.Close()
	store := auth.NewStore(db, tc, nil)
	defer store.Stop()

	id, err := db.InsertAccountWithCredentials(ctx, "synthetic relogin", map[string]any{
		"email": "relogin-state@example.invalid", "workspace_id": "synthetic-workspace",
		"user_id": "synthetic-user", "account_id": "synthetic-workspace",
		"access_token": "synthetic-old-at", "refresh_token": "synthetic-old-rt",
		"client_id": auth.ClientID,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.LoadAccountByID(ctx, id); err != nil {
		t.Fatal(err)
	}
	store.MarkCooldownWithError(store.FindByID(id), time.Hour, "unauthorized", "synthetic old credential rejected")
	cacheKey := strconv.FormatInt(id, 10)
	if _, present, e := tc.GetRuntime(ctx, "account-cooldown", cacheKey); e != nil || !present {
		t.Fatalf("precondition: old unauthorized cooldown missing: %v", e)
	}
	encrypt, _ := credentialops.EnvCrypto()
	sealed, err := encrypt("synthetic-password")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.UpsertCredentialOpsLoginConfig(ctx, database.CredentialOpsLoginConfigRow{
		AccountID: id, LoginEmail: "relogin-state@example.invalid", CredentialMode: "password_totp",
		Engine: "local_worker", ProxySource: "direct", PasswordCiphertext: sealed,
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := db.GetCredentialOpsLoginConfig(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	row, err := db.GetAccountByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	task, err := db.CreateCredentialOpsTaskWithConfig(ctx, id, row.CredentialGeneration, *cfg)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := db.ClaimCredentialOpsTask(ctx, "synthetic-worker", time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("claim: %v", err)
	}

	jwt := func(claims map[string]any) string {
		raw, _ := json.Marshal(claims)
		return "e30." + base64.RawURLEncoding.EncodeToString(raw) + ".synthetic-signature"
	}
	identity := map[string]any{"chatgpt_account_id": "synthetic-workspace", "user_id": "synthetic-user"}
	at := jwt(map[string]any{"client_id": auth.ClientID, "exp": time.Now().Add(time.Hour).Unix(),
		"https://api.openai.com/auth":    identity,
		"https://api.openai.com/profile": map[string]any{"email": "relogin-state@example.invalid"}})
	it := jwt(map[string]any{"aud": auth.ClientID, "email": "relogin-state@example.invalid", "https://api.openai.com/auth": identity})
	body, _ := json.Marshal(map[string]any{"worker_id": "synthetic-worker", "attempt": claim.Attempt, "status": "succeeded",
		"credential": map[string]any{"access_token": at, "refresh_token": "synthetic-new-rt", "id_token": it}})
	offline := errors.New("offline investigation: external probe intentionally stubbed")
	h := &Handler{db: db, store: store,
		probeUsage:          func(context.Context, *auth.Account) error { return offline },
		fetchImportedModels: func(context.Context, *auth.Account, string) (*proxy.CodexModelsManifest, error) { return nil, offline },
	}
	r := gin.New()
	r.POST("/complete/:job_id", h.CompleteCredentialOpsWorker)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", fmt.Sprintf("/complete/%d", task.ID), bytes.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("completion HTTP %d: %s", w.Code, w.Body.String())
	}
	committed, err := db.GetCredentialOpsTask(ctx, task.ID)
	if err != nil || committed.Status != "succeeded" {
		t.Fatalf("task not committed: %v", err)
	}
	row, err = db.GetAccountByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != "active" || row.ErrorMessage != "" || row.CooldownReason != "" || row.CooldownUntil.Valid {
		t.Fatalf("SQL state not recovered: status=%q error=%q cooldown=%q", row.Status, row.ErrorMessage, row.CooldownReason)
	}
	current := store.FindByID(id)
	if current == nil {
		t.Fatal("runtime account missing")
	}
	t.Logf("completion: HTTP=%d task=%s SQL=%s runtime=%s error=%q", w.Code, committed.Status, row.Status, current.RuntimeStatus(), current.ErrorMsg)
	if _, present, e := tc.GetRuntime(ctx, "account-cooldown", cacheKey); e != nil {
		t.Fatal(e)
	} else if present {
		t.Error("BUG: successful relogin left the previous unauthorized cooldown in cache")
	}
	// Selecting this exact account exercises the production cache hydration path.
	if selected := store.TakePreferredAccountWithFilter(id, 0, nil, nil); selected != nil {
		store.Release(selected)
	} else {
		t.Error("BUG: fresh valid credentials are still rejected by the scheduler")
	}
	t.Logf("after scheduling: runtime=%s error=%q available=%v", current.RuntimeStatus(), current.ErrorMsg, current.IsAvailable())
	if current.RuntimeStatus() == "unauthorized" {
		t.Error("BUG: scheduler restored unauthorized state with no error detail after successful relogin")
	}
	// This is exactly the recovery callback used by a successful connection test.
	store.RecordManualTestSuccess(current, time.Millisecond)
	if _, present, e := tc.GetRuntime(ctx, "account-cooldown", cacheKey); e != nil || present {
		t.Fatalf("manual test failed to clear cooldown: present=%v error=%v", present, e)
	}
	if current.RuntimeStatus() == "unauthorized" || !current.IsAvailable() {
		t.Fatalf("manual test failed to recover runtime: %s", current.RuntimeStatus())
	}
	t.Logf("after manual test success: runtime=%s error=%q available=%v", current.RuntimeStatus(), current.ErrorMsg, current.IsAvailable())
}
