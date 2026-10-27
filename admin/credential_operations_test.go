package admin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/codex2api/auth"
	"github.com/codex2api/credentialops"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCredentialOperationsEncryptionImportAndRedaction(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, e := database.New("sqlite", filepath.Join(t.TempDir(), "ops.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "")
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY_FILE", filepath.Join(t.TempDir(), "secrets", "key"))
	h := &Handler{db: db}
	r := gin.New()
	h.RegisterCredentialOpsRoutes(r.Group("/api/admin"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/admin/credential-ops/encryption/initialize", nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/admin/credential-ops/import", strings.NewReader(`{"email":"test@example.com","password":"secret-password","totp_secret":"JBSWY3DPEHPK3PXP","proxy_source":"direct"}`)))
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var job database.CredentialOpsTaskRow
	if e = json.Unmarshal(w.Body.Bytes(), &job); e != nil || job.ID <= 0 {
		t.Fatal(e)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/admin/credential-ops", nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, secret := range []string{"secret-password", "JBSWY3DPEHPK3PXP", "Ciphertext"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("ordinary response exposed secret")
		}
	}
	if _, e = db.CredentialOpsTaskConfig(context.Background(), job.ID); e != nil {
		t.Fatal("no durable input", e)
	}
}

func TestCredentialOperationsRetryRefreshesFirstImportGlobalEgress(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, e := database.New("sqlite", filepath.Join(t.TempDir(), "retry-egress.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	ctx := context.Background()
	store := auth.NewStore(db, nil, &database.SystemSettings{ProxyURL: "http://global-fresh:9010", MaxConcurrency: 3})
	defer store.Stop()
	h := &Handler{db: db, store: store}
	r := gin.New()
	h.RegisterCredentialOpsRoutes(r.Group("/api/admin"))
	task, e := db.CreateCredentialOpsTaskWithConfig(ctx, 0, 0, database.CredentialOpsLoginConfigRow{LoginEmail: "route@example.com", CredentialMode: "password_totp", Engine: "local_worker", ProxySource: "global", ResolvedProxyURL: "http://global-old:9000", RouteResolved: true, PasswordCiphertext: "sealed"})
	if e != nil {
		t.Fatal(e)
	}
	if e = db.CancelCredentialOpsTask(ctx, task.ID); e != nil {
		t.Fatal(e)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", fmt.Sprintf("/api/admin/credential-ops/login/%d/retry", task.ID), nil))
	if w.Code != 202 {
		t.Fatalf("retry %d %s", w.Code, w.Body.String())
	}
	var retry database.CredentialOpsTaskRow
	if e = json.Unmarshal(w.Body.Bytes(), &retry); e != nil {
		t.Fatal(e)
	}
	cfg, e := db.CredentialOpsTaskConfig(ctx, retry.ID)
	if e != nil {
		t.Fatal(e)
	}
	_, dec := credentialops.EnvCrypto()
	proxy, e := dec(cfg.ProxyURLCiphertext)
	if e != nil || proxy != "http://global-fresh:9010" {
		t.Fatalf("retry route %q, want current explicit global route: %v", proxy, e)
	}
}

func TestCredentialOperationsCodexCompletionReplacesExcelClientMetadata(t *testing.T) {
	db, e := database.New("sqlite", filepath.Join(t.TempDir(), "codex-transition.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	id, e := db.InsertAccountWithCredentials(ctx, "Excel primary", map[string]any{"email": "transition@example.com", "workspace_id": "workspace-transition", "user_id": "user-transition", "access_token": "old-excel", "client_id": auth.ExcelClientID}, "")
	if e != nil {
		t.Fatal(e)
	}
	task, e := db.CreateCredentialOpsTaskWithConfig(ctx, 0, 0, database.CredentialOpsLoginConfigRow{LoginEmail: "transition@example.com", CredentialMode: "password_totp", Engine: "local_worker", ProxySource: "direct", PasswordCiphertext: "sealed"})
	if e != nil {
		t.Fatal(e)
	}
	claim, e := db.ClaimCredentialOpsTask(ctx, "worker", time.Minute)
	if e != nil || claim == nil {
		t.Fatalf("claim %#v: %v", claim, e)
	}
	token := func(claims map[string]any) string {
		raw, _ := json.Marshal(claims)
		return "e30." + base64.RawURLEncoding.EncodeToString(raw) + ".signature"
	}
	identity := map[string]any{"chatgpt_account_id": "workspace-transition", "user_id": "user-transition"}
	at := token(map[string]any{"client_id": auth.ClientID, "exp": time.Now().Add(time.Hour).Unix(), "https://api.openai.com/auth": identity, "https://api.openai.com/profile": map[string]any{"email": "transition@example.com"}})
	it := token(map[string]any{"aud": auth.ClientID, "email": "transition@example.com", "https://api.openai.com/auth": identity})
	body, _ := json.Marshal(map[string]any{"worker_id": "worker", "attempt": claim.Attempt, "status": "succeeded", "credential": map[string]any{"access_token": at, "refresh_token": "new-codex-refresh", "id_token": it}})
	h := &Handler{db: db}
	r := gin.New()
	r.POST("/complete/:job_id", h.CompleteCredentialOpsWorker)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", fmt.Sprintf("/complete/%d", task.ID), bytes.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("completion %d %s", w.Code, w.Body.String())
	}
	row, e := db.GetAccountByID(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	if row.GetCredential("client_id") != auth.ClientID {
		t.Fatalf("verified Codex token retained client %q", row.GetCredential("client_id"))
	}
}

func TestCredentialOperationsConcurrentFirstImportRetryPostsReuseOneJob(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, e := database.New("sqlite", filepath.Join(t.TempDir(), "retry-posts.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	source, e := db.CreateCredentialOpsTaskWithConfig(ctx, 0, 0, database.CredentialOpsLoginConfigRow{LoginEmail: "posts@example.com", CredentialMode: "password_totp", Engine: "local_worker", ProxySource: "direct", PasswordCiphertext: "sealed"})
	if e != nil {
		t.Fatal(e)
	}
	if e = db.CancelCredentialOpsTask(ctx, source.ID); e != nil {
		t.Fatal(e)
	}
	h := &Handler{db: db}
	r := gin.New()
	h.RegisterCredentialOpsRoutes(r.Group("/api/admin"))
	type result struct {
		status int
		task   database.CredentialOpsTaskRow
		err    error
	}
	const count = 6
	results := make(chan result, count)
	start := make(chan struct{})
	for i := 0; i < count; i++ {
		go func() {
			<-start
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("POST", fmt.Sprintf("/api/admin/credential-ops/login/%d/retry", source.ID), nil))
			var task database.CredentialOpsTaskRow
			err := json.Unmarshal(w.Body.Bytes(), &task)
			results <- result{w.Code, task, err}
		}()
	}
	close(start)
	ids := map[int64]bool{}
	for i := 0; i < count; i++ {
		reply := <-results
		if reply.status != 202 || reply.err != nil {
			t.Errorf("retry POST status=%d: %v", reply.status, reply.err)
		}
		ids[reply.task.ID] = true
	}
	if len(ids) != 1 || ids[0] {
		t.Fatalf("retry POSTs produced distinct jobs %v", ids)
	}
	tasks, e := db.ListCredentialOpsTasks(ctx)
	if e != nil || len(tasks) != 2 {
		t.Fatalf("durable task count=%d, want two: %v", len(tasks), e)
	}
}

func TestCredentialOperationsDirectSelectorRoundtripClearsStaticID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, e := database.New("sqlite", filepath.Join(t.TempDir(), "selector-roundtrip.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	id, e := db.InsertAccountWithCredentials(ctx, "selector", map[string]any{"email": "selector@example.test", "access_token": "native"}, "")
	if e != nil {
		t.Fatal(e)
	}
	enc, _ := credentialops.EnvCrypto()
	password, e := enc("synthetic-password")
	if e != nil {
		t.Fatal(e)
	}
	if e = db.UpsertCredentialOpsLoginConfig(ctx, database.CredentialOpsLoginConfigRow{AccountID: id, LoginEmail: "selector@example.test", CredentialMode: "password_totp", Engine: "local_worker", ProxySource: "proxy", ProxyID: 9, PasswordCiphertext: password}); e != nil {
		t.Fatal(e)
	}
	h := &Handler{db: db}
	r := gin.New()
	h.RegisterCredentialOpsRoutes(r.Group("/api/admin"))
	path := fmt.Sprintf("/api/admin/accounts/%d/credential-ops/config", id)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("PUT", path, strings.NewReader("{\"proxy_source\":\"direct\",\"proxy_id\":null,\"revision\":1}")))
	if w.Code != 200 {
		t.Fatalf("save %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	var view map[string]any
	if e = json.Unmarshal(w.Body.Bytes(), &view); e != nil {
		t.Fatal(e)
	}
	if view["proxy_source"] != "direct" || view["proxy_id"] != float64(0) {
		t.Fatalf("saved view retained stale selector: %v", view)
	}
	body, _ := json.Marshal(view)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", fmt.Sprintf("/api/admin/accounts/%d/credential-ops/login", id), bytes.NewReader(body)))
	if w.Code != 202 {
		t.Fatalf("reopened login %d %s", w.Code, w.Body.String())
	}
	var task database.CredentialOpsTaskRow
	if e = json.Unmarshal(w.Body.Bytes(), &task); e != nil {
		t.Fatal(e)
	}
	cfg, e := db.CredentialOpsTaskConfig(ctx, task.ID)
	if e != nil {
		t.Fatal(e)
	}
	if cfg.ProxySource != "direct" || cfg.ProxyID != 0 {
		t.Fatalf("reopened login changed route: %#v", cfg)
	}
}
