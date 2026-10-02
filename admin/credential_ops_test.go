package admin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/codex2api/plugins"
	"github.com/gin-gonic/gin"
)

func credentialTestRouter(db *database.DB, store *auth.Store) (*Handler, *gin.Engine) {
	if err := db.EnsureCredentialOpsSchema(context.Background()); err != nil {
		panic(err)
	}
	h := &Handler{db: db, store: store, pluginRegistry: plugins.NewRegistry(database.NewPluginStore(db)), probeUsage: func(context.Context, *auth.Account) error { return nil }}
	// Native pool publication is real; keep asynchronous external warmup probes
	// queued in this fixture so synthetic credentials never leave the process.
	h.importProbeWorkers = 1 << 20
	router := gin.New()
	api := router.Group("/api/admin", func(c *gin.Context) {
		if c.GetHeader("X-Admin-Key") != "admin-test" {
			c.AbortWithStatus(401)
			return
		}
		c.Next()
	})
	h.RegisterCredentialOpsRoutes(api)
	h.RegisterCredentialOpsWorkerRoutes(router)
	return h, router
}
func credentialHTTP(t *testing.T, r *gin.Engine, method, path, role string, body any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	if role == "admin" {
		req.Header.Set("X-Admin-Key", "admin-test")
	}
	if role == "worker" {
		req.Header.Set("X-Codex2API-Credential-Worker", strings.Repeat("w", 32))
	}
	out := httptest.NewRecorder()
	r.ServeHTTP(out, req)
	return out
}
func credentialTestJWT(email, workspace string) string {
	raw, _ := json.Marshal(map[string]any{"email": email, "exp": time.Now().Add(time.Hour).Unix(), "https://api.openai.com/profile": map[string]any{"email": email}, "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": workspace}})
	return "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(raw) + ".signature"
}
func credentialTestTokens(email string) map[string]any {
	return map[string]any{"refresh_token": "rt-test-only", "access_token": credentialTestJWT(email, "workspace-123"), "id_token": credentialTestJWT(email, "workspace-123")}
}
func TestCredentialOpsHTTPImportRestartAndNativePublication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", strings.Repeat("k", 32))
	t.Setenv("CODEX2API_CREDENTIAL_OPS_WORKER_TOKEN", strings.Repeat("w", 32))
	path := filepath.Join(t.TempDir(), "credentials.db")
	db, err := database.New("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { db.Close() }()
	_, r := credentialTestRouter(db, nil)
	input := map[string]any{"name": "Credential import test", "email": "login@example.com", "password": "secret-test-only", "totp_secret": "JBSWY3DPEHPK3PXP", "mode": "password_totp", "engine": "local_worker"}
	out := credentialHTTP(t, r, "POST", "/api/admin/credential-ops/import", "admin", input)
	if out.Code != 202 {
		t.Fatalf("import %d %s", out.Code, out.Body.String())
	}
	var job database.CredentialOpsTaskRow
	if json.Unmarshal(out.Body.Bytes(), &job) != nil || job.AccountID != 0 {
		t.Fatal("import allocated an account before verified login")
	}
	overview := credentialHTTP(t, r, "GET", "/api/admin/credential-ops", "admin", nil)
	if strings.Contains(overview.Body.String(), "secret-test-only") {
		t.Fatal("secret exposed")
	}
	if rows, err := db.ListCredentialOpsMonitors(context.Background()); err != nil || len(rows) != 0 {
		t.Fatalf("premature native account: %v %v", rows, err)
	}
	if out = credentialHTTP(t, r, "POST", "/api/internal/credential-ops/claim", "none", map[string]any{"worker_id": "worker"}); out.Code != 403 {
		t.Fatal("worker auth bypass")
	}
	db.Close()
	db, err = database.New("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	store := auth.NewStore(db, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1})
	_, r = credentialTestRouter(db, store)
	claim := credentialHTTP(t, r, "POST", "/api/internal/credential-ops/claim", "worker", map[string]any{"worker_id": "worker"})
	if claim.Code != 200 {
		t.Fatalf("claim %d %s", claim.Code, claim.Body.String())
	}
	var payload struct {
		Task  database.CredentialOpsTaskRow `json:"task"`
		Login map[string]string             `json:"login"`
	}
	if err = json.Unmarshal(claim.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Login["password"] != "secret-test-only" || payload.Login["totp_secret"] != "JBSWY3DPEHPK3PXP" {
		t.Fatal("durable encrypted snapshot unavailable after restart")
	}
	done := credentialHTTP(t, r, "POST", fmt.Sprintf("/api/internal/credential-ops/login/%d/complete", job.ID), "worker", map[string]any{"worker_id": "worker", "attempt": payload.Task.Attempt, "status": "succeeded", "credential": credentialTestTokens("login@example.com")})
	if done.Code != 200 {
		t.Fatalf("complete %d %s", done.Code, done.Body.String())
	}
	saved, err := db.GetCredentialOpsTask(context.Background(), job.ID)
	if err != nil || saved.Status != "succeeded" || saved.AccountID <= 0 {
		t.Fatalf("native task %#v %v", saved, err)
	}
	if store.FindByID(saved.AccountID) == nil {
		t.Fatal("native runtime pool not published")
	}
	cfg := credentialHTTP(t, r, "GET", fmt.Sprintf("/api/admin/accounts/%d/credential-ops/config", saved.AccountID), "admin", nil)
	if cfg.Code != 200 || strings.Contains(cfg.Body.String(), "enc:v1:") || !strings.Contains(cfg.Body.String(), `"password_configured":true`) {
		t.Fatalf("masked cfg %d %s", cfg.Code, cfg.Body.String())
	}
	row, _ := db.GetAccountByID(context.Background(), saved.AccountID)
	if row.Name != "Credential import test" {
		t.Fatal("import display name lost")
	}
	if row.Credentials["workspace_id"] != "workspace-123" {
		t.Fatal("native identity missing")
	}
	login := credentialHTTP(t, r, "POST", fmt.Sprintf("/api/admin/accounts/%d/credential-ops/login", saved.AccountID), "admin", map[string]any{})
	if login.Code != 202 {
		t.Fatalf("preserved login %d %s", login.Code, login.Body.String())
	}
	// Replay after success must never rotate credentials again.
	done = credentialHTTP(t, r, "POST", fmt.Sprintf("/api/internal/credential-ops/login/%d/complete", job.ID), "worker", map[string]any{"worker_id": "worker", "attempt": payload.Task.Attempt, "status": "succeeded", "credential": credentialTestTokens("login@example.com")})
	if done.Code != 409 {
		t.Fatalf("late callback %d", done.Code)
	}
}

func TestCredentialOpsHTTPFailClosedCancelIdentityAndDisable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := database.New("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, r := credentialTestRouter(db, nil)
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "")
	t.Setenv("CODEX2API_CREDENTIAL_OPS_WORKER_TOKEN", strings.Repeat("w", 32))
	input := map[string]any{"email": "login@example.com", "password": "secret-test-only", "totp_secret": "JBSWY3DPEHPK3PXP"}
	out := credentialHTTP(t, r, "POST", "/api/admin/credential-ops/import", "admin", input)
	if out.Code != 400 {
		t.Fatalf("encryption unavailable %d", out.Code)
	}
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", strings.Repeat("k", 32))
	out = credentialHTTP(t, r, "POST", "/api/admin/credential-ops/import", "admin", input)
	var job database.CredentialOpsTaskRow
	json.Unmarshal(out.Body.Bytes(), &job)
	claimed := credentialHTTP(t, r, "POST", "/api/internal/credential-ops/claim", "worker", map[string]any{"worker_id": "worker"})
	if claimed.Code != 200 {
		t.Fatal(claimed.Body.String())
	}
	path := fmt.Sprintf("/api/internal/credential-ops/login/%d/complete", job.ID)
	out = credentialHTTP(t, r, "POST", path, "worker", map[string]any{"worker_id": "worker", "attempt": 1, "status": "succeeded", "credential": credentialTestTokens("other@example.com")})
	if out.Code != 400 {
		t.Fatalf("identity mismatch %d %s", out.Code, out.Body.String())
	}
	out = credentialHTTP(t, r, "POST", "/api/admin/credential-ops/import", "admin", input)
	if out.Code != 202 {
		t.Fatal(out.Body.String())
	}
	json.Unmarshal(out.Body.Bytes(), &job)
	claimed = credentialHTTP(t, r, "POST", "/api/internal/credential-ops/claim", "worker", map[string]any{"worker_id": "worker"})
	if claimed.Code != 200 {
		t.Fatal(claimed.Body.String())
	}
	path = fmt.Sprintf("/api/internal/credential-ops/login/%d/complete", job.ID)
	credentialHTTP(t, r, "DELETE", fmt.Sprintf("/api/admin/credential-ops/login/%d", job.ID), "admin", nil)
	out = credentialHTTP(t, r, "POST", path, "worker", map[string]any{"worker_id": "worker", "attempt": 1, "status": "succeeded", "credential": credentialTestTokens("login@example.com")})
	if out.Code != 409 {
		t.Fatalf("cancelled callback %d", out.Code)
	}
	out = credentialHTTP(t, r, "POST", fmt.Sprintf("/api/internal/credential-ops/login/%d/renew", job.ID), "worker", map[string]any{"worker_id": "worker", "attempt": 1})
	if out.Code != 409 {
		t.Fatalf("cancelled renewal %d", out.Code)
	}
	registry := plugins.NewRegistry(database.NewPluginStore(db))
	setting, _ := registry.Get(context.Background(), "credential-ops")
	setting.Enabled = false
	if err = registry.Set(context.Background(), setting); err != nil {
		t.Fatal(err)
	}
	out = credentialHTTP(t, r, "POST", "/api/internal/credential-ops/claim", "worker", map[string]any{"worker_id": "worker"})
	if out.Code != 409 {
		t.Fatalf("disabled claim %d", out.Code)
	}
	if rows, err := db.ListCredentialOpsMonitors(context.Background()); err != nil || len(rows) != 0 {
		t.Fatalf("invalid callbacks created accounts %#v %v", rows, err)
	}
}

func TestCredentialOpsEnginesAndSchedulerShutdown(t *testing.T) {
	db, err := database.New("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, r := credentialTestRouter(db, nil)
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", strings.Repeat("k", 32))
	t.Setenv("CODEX2API_CREDENTIAL_OPS_WORKER_TOKEN", strings.Repeat("w", 32))
	input := map[string]any{"email": "email@example.com", "mode": "email_otp_url", "otp_url": "https://mail.example/test-only"}
	out := credentialHTTP(t, r, "POST", "/api/admin/credential-ops/import", "admin", input)
	if out.Code != 202 {
		t.Fatal(out.Body.String())
	}
	var job database.CredentialOpsTaskRow
	json.Unmarshal(out.Body.Bytes(), &job)
	out = credentialHTTP(t, r, "POST", "/api/internal/credential-ops/claim", "worker", map[string]any{"worker_id": "worker"})
	if out.Code != 200 || !strings.Contains(out.Body.String(), "https://mail.example/test-only") {
		t.Fatal("email engine did not receive encrypted mailbox config")
	}
	credentialHTTP(t, r, "DELETE", fmt.Sprintf("/api/admin/credential-ops/login/%d", job.ID), "admin", nil)
	input = map[string]any{"email": "studio@example.com", "mode": "password_totp", "engine": "session_studio", "proxy_source": "direct", "password": "test-only"}
	t.Setenv("CODEX2API_CREDENTIAL_OPS_SESSION_STUDIO_ENDPOINT", "")
	out = credentialHTTP(t, r, "POST", "/api/admin/credential-ops/import", "admin", input)
	if out.Code != 400 {
		t.Fatal("inferred external Session Studio destination")
	}
	t.Setenv("CODEX2API_CREDENTIAL_OPS_SESSION_STUDIO_ENDPOINT", "https://studio.example/login")
	t.Setenv("CODEX2API_CREDENTIAL_OPS_SESSION_STUDIO_HEADERS", `{"X-Test-Key":"fixture-only"}`)
	out = credentialHTTP(t, r, "POST", "/api/admin/credential-ops/import", "admin", input)
	if out.Code != 202 {
		t.Fatal(out.Body.String())
	}
	out = credentialHTTP(t, r, "POST", "/api/internal/credential-ops/claim", "worker", map[string]any{"worker_id": "worker"})
	if out.Code != 200 || !strings.Contains(out.Body.String(), "https://studio.example/login") {
		t.Fatal("explicit Session Studio destination missing")
	}
	overview := credentialHTTP(t, r, "GET", "/api/admin/credential-ops", "admin", nil)
	if strings.Contains(overview.Body.String(), "fixture-only") || strings.Contains(overview.Body.String(), "studio.example") {
		t.Fatal("external endpoint credentials exposed in list")
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.StartCredentialOpsScheduler(ctx)
	cancel()
	waitCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err = h.WaitCredentialOpsScheduler(waitCtx); err != nil {
		t.Fatal(err)
	}
}
