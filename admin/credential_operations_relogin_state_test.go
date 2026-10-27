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
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/cache"
	"github.com/codex2api/credentialops"
	"github.com/codex2api/database"
	"github.com/codex2api/proxy"
	"github.com/gin-gonic/gin"
)

type reloginCache struct {
	cache.TokenCache
	failRead     atomic.Bool
	failDelete   atomic.Bool
	beforeDelete func()
}

func (c *reloginCache) GetRuntime(ctx context.Context, ns, key string) (json.RawMessage, bool, error) {
	if ns == "account-cooldown" && c.failRead.Load() {
		return nil, false, errors.New("synthetic cache read unavailable")
	}
	return c.TokenCache.GetRuntime(ctx, ns, key)
}
func (c *reloginCache) MergeRuntimeCooldown(ctx context.Context, ns, key string, record cache.RuntimeCooldown) (cache.RuntimeCooldown, error) {
	return c.TokenCache.(cache.RuntimeCooldownMerger).MergeRuntimeCooldown(ctx, ns, key, record)
}
func (c *reloginCache) CompareAndDeleteRuntime(ctx context.Context, ns, key string, expected []byte) (bool, error) {
	if c.failDelete.Load() {
		return false, errors.New("synthetic cache delete unavailable")
	}
	if c.beforeDelete != nil {
		c.beforeDelete()
	}
	return c.TokenCache.(cache.RuntimeValueCompareDeleter).CompareAndDeleteRuntime(ctx, ns, key, expected)
}

type reloginFixture struct {
	db      *database.DB
	cache   *reloginCache
	store   *auth.Store
	old     *auth.Account
	task    *database.CredentialOpsTaskRow
	handler *Handler
	router  *gin.Engine
	body    []byte
}

func (f *reloginFixture) complete() *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, httptest.NewRequest("POST", fmt.Sprintf("/complete/%d", f.task.ID), bytes.NewReader(f.body)))
	return w
}
func (f *reloginFixture) cacheRecord(t *testing.T) (cache.RuntimeCooldown, bool) {
	t.Helper()
	raw, present, err := f.cache.TokenCache.GetRuntime(context.Background(), "account-cooldown", strconv.FormatInt(f.old.ID(), 10))
	if err != nil {
		t.Fatal(err)
	}
	var record cache.RuntimeCooldown
	if present {
		if err = json.Unmarshal(raw, &record); err != nil {
			t.Fatal(err)
		}
	}
	return record, present
}
func newReloginFixture(t *testing.T) *reloginFixture {
	return newReloginFixtureOptions(t, "codex", false)
}

func newReloginFixtureOptions(t *testing.T, profile string, paused bool, quotaCooldown ...bool) *reloginFixture {
	t.Helper()

	gin.SetMode(gin.TestMode)
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	ctx := context.Background()
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "relogin-state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	tc := &reloginCache{TokenCache: cache.NewMemory(4)}
	t.Cleanup(func() { tc.Close() })
	store := auth.NewStore(db, tc, nil)
	t.Cleanup(store.Stop)

	id, err := db.InsertAccountWithCredentials(ctx, "synthetic relogin", map[string]any{
		"email": "relogin-state@example.invalid", "workspace_id": "synthetic-workspace",
		"user_id": "synthetic-user", "account_id": "synthetic-workspace",
		"access_token": "synthetic-old-at", "refresh_token": "synthetic-old-rt", "plan_type": "plus",
		"client_id": auth.ClientID,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if paused {
		if err = db.SetAccountEnabled(ctx, id, false); err != nil {
			t.Fatal(err)
		}
	}
	if err = store.LoadAccountByID(ctx, id); err != nil {
		t.Fatal(err)
	}
	old := store.FindByID(id)
	store.MarkCooldownWithError(old, time.Hour, "unauthorized", "synthetic old credential rejected")
	cacheKey := strconv.FormatInt(id, 10)
	if _, present, e := tc.GetRuntime(ctx, "account-cooldown", cacheKey); e != nil || !present {
		t.Fatalf("precondition: old unauthorized cooldown missing: %v", e)
	}
	if len(quotaCooldown) > 0 && quotaCooldown[0] {
		deadline := time.Now().Add(time.Hour)
		if _, err := db.UpdateAccountRuntimeStateForGeneration(ctx, id, old.GetCredentialGeneration(), "cooldown", "", "rate_limited_5h", &deadline); err != nil {
			t.Fatal(err)
		}
		record := cache.RuntimeCooldown{Reason: "rate_limited_5h", ResetAt: deadline, UpdatedAt: time.Now(), Generation: old.GetCredentialGeneration()}
		raw, _ := json.Marshal(record)
		if err := tc.SetRuntime(ctx, "account-cooldown", cacheKey, raw, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	encrypt, _ := credentialops.EnvCrypto()
	sealed, err := encrypt("synthetic-password")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.UpsertCredentialOpsLoginConfig(ctx, database.CredentialOpsLoginConfigRow{
		AccountID: id, LoginEmail: "relogin-state@example.invalid", CredentialMode: "password_totp",
		Engine: "local_worker", ProxySource: "direct", PasswordCiphertext: sealed, OAuthProfile: profile,
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := db.GetCredentialOpsLoginConfig(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	cfg.OAuthProfile = profile
	row, err := db.GetAccountByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	task, err := db.CreateCredentialOpsTaskWithConfig(ctx, id, row.CredentialGeneration, *cfg)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := db.ClaimCredentialOpsTaskWithCapabilities(ctx, "synthetic-worker", time.Minute, []string{"login_codex_v1", "excel_oauth_v1"})
	if err != nil || claim == nil {
		t.Fatalf("claim: %v", err)
	}

	clientID := auth.ClientID
	if profile == "excel" {
		clientID = credentialops.ExcelClientID
	}
	jwt := func(claims map[string]any) string {
		raw, _ := json.Marshal(claims)
		return "e30." + base64.RawURLEncoding.EncodeToString(raw) + ".synthetic-signature"
	}
	identity := map[string]any{"chatgpt_account_id": "synthetic-workspace", "user_id": "synthetic-user"}
	at := jwt(map[string]any{"client_id": clientID, "exp": time.Now().Add(time.Hour).Unix(),
		"https://api.openai.com/auth":    identity,
		"https://api.openai.com/profile": map[string]any{"email": "relogin-state@example.invalid"}})
	it := jwt(map[string]any{"aud": clientID, "email": "relogin-state@example.invalid", "https://api.openai.com/auth": identity})
	body, _ := json.Marshal(map[string]any{"worker_id": "synthetic-worker", "attempt": claim.Attempt, "status": "succeeded",
		"credential": map[string]any{"access_token": at, "refresh_token": "synthetic-new-rt", "id_token": it, "client_id": clientID, "user_id": "synthetic-user"}})
	offline := errors.New("offline investigation: external probe intentionally stubbed")
	h := &Handler{db: db, store: store,
		probeUsage:          func(context.Context, *auth.Account) error { return offline },
		fetchImportedModels: func(context.Context, *auth.Account, string) (*proxy.CodexModelsManifest, error) { return nil, offline },
	}
	r := gin.New()
	r.POST("/complete/:job_id", h.CompleteCredentialOpsWorker)
	h.RegisterCredentialOpsRoutes(r.Group("/api/admin"))
	t.Cleanup(func() { db.DrainBackgroundTasks(2 * time.Second) })
	return &reloginFixture{db: db, cache: tc, store: store, old: old, task: task, router: r, body: body, handler: h}
}

// All credentials and probes are synthetic; no external network or production data.
func TestCredentialOpsReloginMustClearCachedUnauthorized(t *testing.T) {
	f := newReloginFixture(t)
	db, tc, store, task := f.db, f.cache, f.store, f.task
	ctx, id := context.Background(), f.old.ID()
	cacheKey := strconv.FormatInt(id, 10)
	w := f.complete()
	if w.Code != 200 {
		t.Logf("retry reconciliation error: %v", f.handler.reconcileCredentialOpsLogin(ctx, task.ID, id))
		t.Fatalf("completion HTTP %d: %s", w.Code, w.Body.String())
	}
	committed, err := db.GetCredentialOpsTask(ctx, task.ID)
	if err != nil || committed.Status != "succeeded" {
		t.Fatalf("task not committed: %v", err)
	}
	row, err := db.GetAccountByID(ctx, id)
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

func TestCredentialOpsReloginLateOld401CannotRebanNewCredentials(t *testing.T) {
	f := newReloginFixture(t)
	if w := f.complete(); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	f.store.MarkCooldownWithError(f.old, time.Hour, "unauthorized", "late old credential failure")
	if _, present := f.cacheRecord(t); present {
		t.Error("late old 401 recreated the cooldown cache")
	}
	row, err := f.db.GetAccountByID(context.Background(), f.old.ID())
	if err != nil {
		t.Fatal(err)
	}
	if row.CooldownReason != "" || row.ErrorMessage != "" {
		t.Errorf("late old 401 rewrote current SQL state: %q %q", row.CooldownReason, row.ErrorMessage)
	}
	current := f.store.FindByID(f.old.ID())
	if current == nil || current.RuntimeStatus() == "unauthorized" {
		t.Fatal("current runtime was invalidated")
	}
}

func TestCredentialOpsReloginCacheFailureCanRecoverWithoutAnotherLogin(t *testing.T) {
	for _, failure := range []string{"read", "delete"} {
		t.Run(failure, func(t *testing.T) {
			f := newReloginFixture(t)
			if failure == "read" {
				f.cache.failRead.Store(true)
			} else {
				f.cache.failDelete.Store(true)
			}
			if w := f.complete(); w.Code != 503 {
				t.Errorf("cache failure HTTP=%d, want recoverable 503", w.Code)
			}
			saved, err := f.db.GetCredentialOpsTask(context.Background(), f.task.ID)
			if err != nil || saved.Status != "succeeded" {
				t.Fatalf("durable task must remain succeeded: %v", err)
			}
			f.cache.failRead.Store(false)
			f.cache.failDelete.Store(false)
			if w := f.complete(); w.Code != 200 {
				t.Fatalf("completion replay: %d %s", w.Code, w.Body.String())
			}
			if _, present := f.cacheRecord(t); present {
				t.Error("completion replay retained old cache")
			}
			current := f.store.FindByID(f.old.ID())
			if selected := f.store.TakePreferredAccountWithFilter(f.old.ID(), 0, nil, nil); selected == nil {
				t.Error("recovered account still unavailable")
			} else {
				f.store.Release(selected)
			}
			if current == nil || current.RuntimeStatus() == "unauthorized" {
				t.Error("runtime recovery did not finish")
			}
		})
	}
}

func TestCredentialOpsReloginRetryReconcilesSucceededTask(t *testing.T) {
	f := newReloginFixture(t)
	f.cache.failDelete.Store(true)
	f.complete()
	f.cache.failDelete.Store(false)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, httptest.NewRequest("POST", fmt.Sprintf("/api/admin/credential-ops/login/%d/retry", f.task.ID), nil))
	if w.Code != 202 {
		t.Fatalf("retry HTTP=%d %s", w.Code, w.Body.String())
	}
	if _, present := f.cacheRecord(t); present {
		t.Error("successful-task retry retained stale unauthorized cache")
	}
	current := f.store.FindByID(f.old.ID())
	if current == nil || current.RuntimeStatus() == "unauthorized" {
		t.Error("retry did not recover runtime")
	}
}

func TestCredentialOpsReloginReplayPreservesNewFailure(t *testing.T) {
	f := newReloginFixture(t)
	if w := f.complete(); w.Code != 200 {
		t.Fatal(w.Code)
	}
	current := f.store.FindByID(f.old.ID())
	f.store.MarkCooldownWithError(current, time.Hour, "unauthorized", "new credential really rejected")
	for _, via := range []string{"complete", "retry"} {
		if via == "complete" {
			if w := f.complete(); w.Code != 200 {
				t.Fatal(w.Code)
			}
		} else {
			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, httptest.NewRequest("POST", fmt.Sprintf("/api/admin/credential-ops/login/%d/retry", f.task.ID), nil))
			if w.Code != 202 {
				t.Fatal(w.Code)
			}
		}
		if f.store.FindByID(f.old.ID()) != current {
			t.Errorf("%s unnecessarily replaced current runtime", via)
		}
		if _, present := f.cacheRecord(t); !present {
			t.Errorf("%s erased the newer real auth failure", via)
		}
		if current.RuntimeStatus() != "unauthorized" {
			t.Error("newer failure was reset")
		}
	}
}

func TestCredentialOpsReloginPreservesQuotaCooldownAndManualPause(t *testing.T) {
	f := newReloginFixtureOptions(t, "codex", true, true)
	if w := f.complete(); w.Code != 200 {
		t.Fatalf("completion HTTP=%d %s", w.Code, w.Body.String())
	}
	row, err := f.db.GetAccountByID(context.Background(), f.old.ID())
	if err != nil {
		t.Fatal(err)
	}
	if row.Enabled {
		t.Error("successful relogin changed the manually paused state")
	}
	if record, present := f.cacheRecord(t); !present || record.Reason != "rate_limited_5h" {
		t.Errorf("successful relogin cleared the quota cache: %+v present=%v", record, present)
	}
	if row.CooldownReason != "rate_limited_5h" || !row.CooldownUntil.Valid {
		t.Errorf("successful relogin cleared the quota cooldown: reason=%q deadline=%v", row.CooldownReason, row.CooldownUntil)
	}
}

func TestCredentialOpsExcelGrantDoesNotClearCodexAuthFailure(t *testing.T) {
	f := newReloginFixtureOptions(t, "excel", false)
	if w := f.complete(); w.Code != 200 {
		t.Fatalf("Excel completion: %d %s", w.Code, w.Body.String())
	}
	if f.store.FindByID(f.old.ID()) != f.old {
		t.Error("Excel grant replaced the independent Codex runtime")
	}
	if _, present := f.cacheRecord(t); !present {
		t.Error("Excel grant cleared the independent Codex authorization failure")
	}
	if f.old.RuntimeStatus() != "unauthorized" {
		t.Error("Excel grant changed Codex runtime auth status")
	}
}

func TestCredentialOpsReloginLegacyCacheAndConcurrentReplacement(t *testing.T) {
	for _, tc := range []struct {
		name        string
		generation  int64
		updated     time.Duration
		replace     bool
		wantPresent bool
	}{
		{"old legacy cache", 0, -time.Hour, false, false},
		{"new legacy cache", 0, time.Hour, false, true},
		{"same generation ignores clock skew", 2, -time.Hour, false, true},
		{"compare delete preserves replacement", 1, -time.Hour, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReloginFixture(t)
			key := strconv.FormatInt(f.old.ID(), 10)
			record := cache.RuntimeCooldown{Reason: "unauthorized", Generation: tc.generation, ResetAt: time.Now().Add(time.Hour), UpdatedAt: time.Now().Add(tc.updated)}
			raw, _ := json.Marshal(record)
			if err := f.cache.SetRuntime(context.Background(), "account-cooldown", key, raw, time.Hour); err != nil {
				t.Fatal(err)
			}
			if tc.replace {
				f.cache.beforeDelete = func() {
					record.Generation = 2
					raw, _ := json.Marshal(record)
					if err := f.cache.SetRuntime(context.Background(), "account-cooldown", key, raw, time.Hour); err != nil {
						t.Error(err)
					}
				}
			}
			if w := f.complete(); w.Code != 200 {
				t.Fatalf("complete: %d %s", w.Code, w.Body.String())
			}
			if got, present := f.cacheRecord(t); present != tc.wantPresent || tc.replace && got.Generation != 2 {
				t.Fatalf("cache: %+v present=%v want=%v", got, present, tc.wantPresent)
			}
		})
	}
}

func TestCredentialOpsReloginPreservesMonitorSwitches(t *testing.T) {
	f := newReloginFixture(t)
	if err := f.db.SaveCredentialOpsMonitor(context.Background(), database.CredentialOpsMonitorRow{AccountID: f.old.ID(), Enabled: false, AutoRelogin: false, IntervalSeconds: 600, FailureThreshold: 3, CooldownSeconds: 900}); err != nil {
		t.Fatal(err)
	}
	if w := f.complete(); w.Code != 200 {
		t.Fatalf("complete: %d %s", w.Code, w.Body.String())
	}
	monitors, err := f.db.ListCredentialOpsMonitors(context.Background())
	if err != nil || len(monitors) != 1 {
		t.Fatalf("monitors: %+v %v", monitors, err)
	}
	m := monitors[0]
	if m.Enabled || m.AutoRelogin || m.IntervalSeconds != 600 || m.FailureThreshold != 3 || m.CooldownSeconds != 900 {
		t.Fatalf("relogin changed monitor settings: %+v", m)
	}
}

func TestCredentialOpsReloginRetriesRuntimeReloadAfterCommit(t *testing.T) {
	f := newReloginFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.cache.beforeDelete = cancel
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", fmt.Sprintf("/complete/%d", f.task.ID), bytes.NewReader(f.body)).WithContext(ctx)
	f.router.ServeHTTP(w, req)
	if w.Code != 503 {
		t.Fatalf("reload failure: %d %s", w.Code, w.Body.String())
	}
	f.cache.beforeDelete = nil
	if w := f.complete(); w.Code != 200 {
		t.Fatalf("replay: %d %s", w.Code, w.Body.String())
	}
	if acc := f.store.FindByID(f.old.ID()); acc == nil || acc.GetCredentialGeneration() != 2 || acc.RuntimeStatus() == "unauthorized" {
		t.Fatalf("runtime not recovered after replay: %v", acc)
	}
}
