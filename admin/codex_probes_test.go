package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/codex2api/proxy"
	"github.com/gin-gonic/gin"
)

func newCodexProbeTestHandler(t *testing.T, count int) (*Handler, []*auth.Account) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := newTestAdminDB(t)
	store := auth.NewStore(db, nil, nil)
	accounts := make([]*auth.Account, 0, count)
	for i := range count {
		id, err := db.InsertAccountWithCredentials(context.Background(), fmt.Sprintf("probe-%d", i), map[string]interface{}{
			"access_token": "probe-secret-token", "account_id": "probe-workspace", "plan_type": "plus",
		}, "")
		if err != nil {
			t.Fatal(err)
		}
		row, err := db.GetAccountByID(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		account := &auth.Account{DBID: id, AccessToken: "probe-secret-token", AccountID: "probe-workspace", PlanType: "plus", Status: auth.StatusReady, HealthTier: auth.HealthTierHealthy, CredentialGeneration: row.CredentialGeneration}
		store.AddAccount(account)
		accounts = append(accounts, account)
	}
	return &Handler{db: db, store: store, adminSecretEnv: "probe-admin-secret"}, accounts
}

func codexProbeTestRequest(handler *Handler, ctx context.Context, body, query string) *httptest.ResponseRecorder {
	writer := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(writer)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/admin/accounts/codex/probe"+query, strings.NewReader(body)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	handler.ProbeCodexAccounts(c)
	return writer
}

func codexProbeTestBody(accounts []*auth.Account) string {
	ids := make([]int64, 0, len(accounts))
	for _, account := range accounts {
		ids = append(ids, account.ID())
	}
	body, _ := json.Marshal(codexProbeRequest{IDs: ids, Model: "gpt-6-astra", Level: "basic"})
	return string(body)
}

func readCodexProbeTestResults(t *testing.T, writer *httptest.ResponseRecorder) []proxy.CodexProbeResult {
	t.Helper()
	if writer.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", writer.Code, writer.Body.String())
	}
	var response struct {
		Results []proxy.CodexProbeResult `json:"results"`
	}
	if err := json.Unmarshal(writer.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response.Results
}

func TestCodexProbeValidatesBoundedExactRequests(t *testing.T) {
	h, accounts := newCodexProbeTestHandler(t, 1)
	var called atomic.Int32
	h.codexCapabilityProbe = func(context.Context, *auth.Account, proxy.CodexCapabilityProbeOptions) proxy.CodexProbeResult {
		called.Add(1)
		return proxy.CodexProbeResult{}
	}
	tooMany := make([]int64, 101)
	for i := range tooMany {
		tooMany[i] = int64(i + 1)
	}
	oversized, _ := json.Marshal(codexProbeRequest{IDs: tooMany, Model: "gpt-6-astra", Level: "basic"})
	for _, body := range []string{
		`{}`, `{"ids":[1],"model":"gpt-6-astra"}`, `{"ids":[],"model":"gpt-6-astra","level":"basic"}`,
		`{"ids":[-1],"model":"gpt-6-astra","level":"basic"}`, `{"ids":[1],"model":"gpt-6-astra","level":"full"}`,
		`{"ids":[1],"model":"gpt 6","level":"basic"}`, `{"ids":[1],"model":"gpt-6-astra","level":"basic","fallback":true}`,
		codexProbeTestBody(accounts) + `{}`, string(oversized), strings.Repeat("x", codexProbeMaxBody+1),
	} {
		if writer := codexProbeTestRequest(h, context.Background(), body, ""); writer.Code != 400 {
			t.Fatalf("accepted invalid request %s: %s", body, writer.Body.String())
		}
	}
	if called.Load() != 0 {
		t.Fatal("invalid request reached the upstream")
	}
}

func TestCodexProbeRequiresAdminAuthentication(t *testing.T) {
	h, accounts := newCodexProbeTestHandler(t, 1)
	var called atomic.Int32
	h.codexCapabilityProbe = func(_ context.Context, account *auth.Account, options proxy.CodexCapabilityProbeOptions) proxy.CodexProbeResult {
		called.Add(1)
		return newCodexProbeLocalResult(account.ID(), codexProbeRequest{Model: options.Model, Level: options.Level}, "supported", "", "complete")
	}
	router := gin.New()
	group := router.Group("/api/admin")
	group.Use(h.adminAuthMiddleware())
	group.POST("/accounts/codex/probe", h.ProbeCodexAccounts)
	group.GET("/accounts/:id/codex-probes", h.GetCodexProbes)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		path := "/api/admin/accounts/codex/probe"
		if method == http.MethodGet {
			path = fmt.Sprintf("/api/admin/accounts/%d/codex-probes", accounts[0].ID())
		}
		writer := httptest.NewRecorder()
		router.ServeHTTP(writer, httptest.NewRequest(method, path, strings.NewReader(codexProbeTestBody(accounts))))
		if writer.Code != 401 {
			t.Fatalf("unauthenticated status=%d", writer.Code)
		}
	}
	if called.Load() != 0 {
		t.Fatal("unauthenticated request reached upstream")
	}
	writer := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/admin/accounts/codex/probe", strings.NewReader(codexProbeTestBody(accounts)))
	request.Header.Set("X-Admin-Key", "probe-admin-secret")
	router.ServeHTTP(writer, request)
	if writer.Code != 200 || called.Load() != 1 {
		t.Fatalf("authenticated request failed: %s", writer.Body.String())
	}
	if strings.Contains(writer.Body.String(), "probe-secret") {
		t.Fatal("credential leaked")
	}
}

func TestCodexProbeAllowsUnknownAndUnsupportedWithoutAccountMutation(t *testing.T) {
	h, accounts := newCodexProbeTestHandler(t, 2)
	_, err := h.db.ObserveCodexCapability(context.Background(), accounts[1].ID(), database.CodexCapability{Upstream: "basispoints", Model: "gpt-6-astra", Capability: "unsupported", Source: "prior", ObservedAt: time.Now().UnixNano()})
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts[1].ReloadCodexRoutes(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := []auth.AccountListRuntimeSnapshot{accounts[0].GetAccountListRuntimeSnapshot(), accounts[1].GetAccountListRuntimeSnapshot()}
	var called atomic.Int32
	h.codexCapabilityProbe = func(ctx context.Context, account *auth.Account, options proxy.CodexCapabilityProbeOptions) proxy.CodexProbeResult {
		called.Add(1)
		if options.Model != "gpt-6-astra" || options.Level != "tools" || ctx.Err() != nil {
			t.Errorf("incorrect options/context: %+v", options)
		}
		return newCodexProbeLocalResult(account.ID(), codexProbeRequest{Model: options.Model, Level: options.Level}, "protocol_error", "invalid_encrypted_content", "Tool format rejected")
	}
	body := fmt.Sprintf(`{"ids":[%d,%d,%d],"model":" GPT-6-ASTRA ","level":"tools"}`, accounts[0].ID(), accounts[1].ID(), accounts[0].ID())
	results := readCodexProbeTestResults(t, codexProbeTestRequest(h, context.Background(), body, ""))
	if len(results) != 2 || called.Load() != 2 {
		t.Fatalf("count=%d calls=%d", len(results), called.Load())
	}
	for i, account := range accounts {
		if !reflect.DeepEqual(before[i], account.GetAccountListRuntimeSnapshot()) {
			t.Fatal("probe changed account-wide runtime health")
		}
		row, err := h.db.GetAccountByID(context.Background(), account.ID())
		if err != nil || !row.Enabled || row.ErrorMessage != "" {
			t.Fatalf("probe changed account database status: %+v error=%v", row, err)
		}
	}
}

func TestCodexProbeHonorsLocalGatesWithoutDispatch(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(*testing.T, *Handler, *auth.Account)
		outcome string
	}{
		{"administrator_disabled", func(t *testing.T, h *Handler, a *auth.Account) {
			if err := h.db.SetAccountEnabled(context.Background(), a.ID(), false); err != nil {
				t.Fatal(err)
			}
		}, "blocked"},
		{"dispatch_paused", func(_ *testing.T, _ *Handler, a *auth.Account) { atomic.StoreInt32(&a.DispatchPaused, 1) }, "blocked"},
		{"unauthorized", func(_ *testing.T, _ *Handler, a *auth.Account) { atomic.StoreInt32(&a.Disabled, 1) }, "unauthorized"},
		{"missing_token", func(_ *testing.T, _ *Handler, a *auth.Account) { a.AccessToken = "" }, "unauthorized"},
		{"rate_limited", func(_ *testing.T, _ *Handler, a *auth.Account) {
			a.Status = auth.StatusCooldown
			a.CooldownReason = "rate_limited"
			a.CooldownUtil = time.Now().Add(time.Hour)
		}, "rate_limited"},
		{"responses_quota_exhausted", func(_ *testing.T, h *Handler, a *auth.Account) {
			h.store.MarkResponsesRateLimited(a, time.Hour)
		}, "rate_limited"},
		{"workspace", func(_ *testing.T, _ *Handler, a *auth.Account) {
			a.Status = auth.StatusError
			a.ErrorMsg = "deactivated_workspace"
		}, "workspace_deactivated"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			h, accounts := newCodexProbeTestHandler(t, 1)
			test.setup(t, h, accounts[0])
			before := accounts[0].GetAccountListRuntimeSnapshot()
			h.codexCapabilityProbe = func(context.Context, *auth.Account, proxy.CodexCapabilityProbeOptions) proxy.CodexProbeResult {
				t.Error("blocked account reached upstream")
				return proxy.CodexProbeResult{}
			}
			results := readCodexProbeTestResults(t, codexProbeTestRequest(h, context.Background(), codexProbeTestBody(accounts), ""))
			if len(results) != 1 || results[0].Outcome != test.outcome || results[0].Attempts != 0 || results[0].BasicOutcome != "not_run" || results[0].ToolsOutcome != "not_run" {
				t.Fatalf("result=%+v", results)
			}
			if !reflect.DeepEqual(before, accounts[0].GetAccountListRuntimeSnapshot()) {
				t.Fatal("blocked probe changed runtime status")
			}
		})
	}
}

func TestCodexProbeConcurrencySharedAcrossJobsAndAccountOverlapSkipped(t *testing.T) {
	h, accounts := newCodexProbeTestHandler(t, 8)
	entered := make(chan int64, len(accounts))
	release := make(chan struct{})
	var active, maximum atomic.Int32
	h.codexCapabilityProbe = func(_ context.Context, account *auth.Account, options proxy.CodexCapabilityProbeOptions) proxy.CodexProbeResult {
		now := active.Add(1)
		for {
			old := maximum.Load()
			if now <= old || maximum.CompareAndSwap(old, now) {
				break
			}
		}
		entered <- account.ID()
		<-release
		active.Add(-1)
		return newCodexProbeLocalResult(account.ID(), codexProbeRequest{Model: options.Model, Level: options.Level}, "supported", "", "complete")
	}
	done := make(chan *httptest.ResponseRecorder, 2)
	go func() { done <- codexProbeTestRequest(h, context.Background(), codexProbeTestBody(accounts[:4]), "") }()
	go func() { done <- codexProbeTestRequest(h, context.Background(), codexProbeTestBody(accounts[4:]), "") }()
	var first int64
	for range 3 {
		select {
		case first = <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("workers did not start")
		}
	}
	body := fmt.Sprintf(`{"ids":[%d],"model":"gpt-6-astra","level":"basic"}`, first)
	duplicate := readCodexProbeTestResults(t, codexProbeTestRequest(h, context.Background(), body, ""))
	if len(duplicate) != 1 || duplicate[0].ErrorCode != "probe_in_progress" {
		t.Fatalf("duplicate=%+v", duplicate)
	}
	close(release)
	for range 2 {
		select {
		case result := <-done:
			if len(readCodexProbeTestResults(t, result)) != 4 {
				t.Fatal("missing results")
			}
		case <-time.After(10 * time.Second):
			t.Fatal("batch did not finish")
		}
	}
	if maximum.Load() != 3 {
		t.Fatalf("maximum=%d", maximum.Load())
	}
}

func TestCodexProbeDisconnectCancelsActiveAndQueuedWork(t *testing.T) {
	h, accounts := newCodexProbeTestHandler(t, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{}, 3)
	var calls atomic.Int32
	h.codexCapabilityProbe = func(ctx context.Context, account *auth.Account, options proxy.CodexCapabilityProbeOptions) proxy.CodexProbeResult {
		calls.Add(1)
		started <- struct{}{}
		<-ctx.Done()
		return newCodexProbeLocalResult(account.ID(), codexProbeRequest{Model: options.Model, Level: options.Level}, "canceled", "canceled", "canceled")
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- codexProbeTestRequest(h, ctx, codexProbeTestBody(accounts), "?stream=true") }()
	for range 3 {
		select {
		case <-started:
		case <-time.After(10 * time.Second):
			t.Fatal("workers did not start")
		}
	}
	cancel()
	select {
	case writer := <-done:
		if strings.Contains(writer.Body.String(), `"type":"done"`) {
			t.Fatal("canceled stream reported completion")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("disconnect did not stop workers")
	}
	if calls.Load() != 3 {
		t.Fatalf("canceled job dispatched queued accounts: %d", calls.Load())
	}
	h.codexProbeMu.Lock()
	defer h.codexProbeMu.Unlock()
	if len(h.codexProbeRunning) != 0 || len(h.codexProbeSlots) != 0 {
		t.Fatal("cancellation leaked probe reservations")
	}
}

func TestCodexProbeStreamAndPersistedResultsContract(t *testing.T) {
	h, accounts := newCodexProbeTestHandler(t, 1)
	h.codexCapabilityProbe = func(_ context.Context, account *auth.Account, options proxy.CodexCapabilityProbeOptions) proxy.CodexProbeResult {
		return newCodexProbeLocalResult(account.ID(), codexProbeRequest{Model: options.Model, Level: options.Level}, "supported", "", "complete")
	}
	writer := codexProbeTestRequest(h, context.Background(), codexProbeTestBody(accounts), "?stream=true")
	var kinds []string
	for _, line := range strings.Split(writer.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var event codexProbeEvent
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, event.Type)
		if event.Type == "done" && (event.Total != 1 || event.Completed != 1 || len(event.Results) != 1) {
			t.Fatalf("done=%+v", event)
		}
	}
	if !reflect.DeepEqual(kinds, []string{"start", "testing", "result", "done"}) {
		t.Fatalf("events=%v body=%s", kinds, writer.Body.String())
	}
	for _, level := range []string{"basic", "tools"} {
		result := newCodexProbeLocalResult(accounts[0].ID(), codexProbeRequest{Model: "gpt-6-astra", Level: level}, "supported", "", "complete")
		if _, err := h.db.SaveCodexProbeResult(context.Background(), accounts[0].GetCredentialGeneration(), result, nil); err != nil {
			t.Fatal(err)
		}
	}
	router := gin.New()
	router.GET("/accounts/:id/codex-probes", h.GetCodexProbes)
	router.GET("/accounts/:id/codex-routes", h.GetCodexRoutes)
	for _, suffix := range []string{"codex-probes", "codex-routes"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/accounts/%d/%s?model=gpt-6-astra", accounts[0].ID(), suffix), nil))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"level":"basic"`) || !strings.Contains(w.Body.String(), `"level":"tools"`) {
			t.Fatalf("persisted results missing: %s", w.Body.String())
		}
	}
	results, err := h.codexProbeResults(context.Background(), accounts[0].ID(), "gpt-6-sol")
	if err != nil || len(results) != 0 {
		t.Fatalf("cross-model results: %+v %v", results, err)
	}
}
