package admin

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/codex2api/database"
	"github.com/codex2api/ipv6state"
	"github.com/codex2api/statepool"
	"github.com/gin-gonic/gin"
)

func TestSupplySignalReplenishesBeforeExpiryAndSubtractsPending(t *testing.T) {
	now := time.Date(2026, 9, 20, 8, 0, 0, 0, time.UTC)
	p := supplyPolicy{AccountBudget: 400, Throughput: 410, ProductiveMinutes: 50, LeadMinutes: 10, BufferMinutes: 5, Pending: 1}
	accounts := []supplyAccount{}
	for i := range 8 {
		expires := now.Add(30 * time.Minute)
		if i < 3 {
			expires = now.Add(15 * time.Minute)
		}
		accounts = append(accounts, supplyAccount{ID: int64(i + 1), Ready: true, ExpiresAt: &expires, Models: []supplyModel{{Valid: true}}})
	}
	activity := []database.SupplyActivity{{Billed60m: 2978.19, Billed15m: 715, LastAttemptAt: &now}}
	r := buildSupplySignal(now, []string{"gpt-6-astra"}, p, accounts, activity)
	if r.Recommendation.TargetReady != 8 || r.Recommendation.AddNow != 2 || r.Summary.ReadyAtHorizon != 5 || r.Summary.Expiring != 3 {
		t.Fatalf("forecast=%+v", r)
	}
	if r.Recommendation.ReadyPerHour < 8.7 || r.Recommendation.ReadyPerHour > 8.8 {
		t.Fatalf("hourly=%v", r.Recommendation.ReadyPerHour)
	}
	p.Pending = 3
	r = buildSupplySignal(now, []string{"gpt-6-astra"}, p, accounts, activity)
	if r.Recommendation.AddNow != 0 {
		t.Fatal("pending logins were duplicated")
	}
}

func TestSupplySignalHistorical401DoesNotRequestRelogin(t *testing.T) {
	now := time.Date(2026, 9, 20, 8, 0, 0, 0, time.UTC)
	p := supplyPolicy{AccountBudget: 400, Throughput: 410, ProductiveMinutes: 50, LeadMinutes: 10, BufferMinutes: 5}
	accounts := []supplyAccount{{ID: 1, LastUnauthorizedAt: &now}, {ID: 2, Enabled: true, NeedsRelogin: true, ReloginReason: "token_revoked"}, {ID: 3, Enabled: false, ReloginReason: "unauthorized"}}
	r := buildSupplySignal(now, nil, p, accounts, []database.SupplyActivity{{AccountID: 1, Unauthorized: 1, LastAttemptAt: &now}})
	if len(r.Recommendation.ReloginIDs) != 1 || r.Recommendation.ReloginIDs[0] != 2 || r.Summary.Unauthorized != 2 {
		t.Fatalf("relogin=%+v", r)
	}
	if r.Recommendation.Action != "relogin" {
		t.Fatal("missing relogin action")
	}
}

func TestSupplySignalExpiryBoundaryAndNoDemand(t *testing.T) {
	now := time.Date(2026, 9, 20, 8, 0, 0, 0, time.UTC)
	p := supplyPolicy{AccountBudget: 400, Throughput: 410, ProductiveMinutes: 50, LeadMinutes: 10, BufferMinutes: 5, MinReady: 1}
	accounts := []supplyAccount{{ID: 1, Ready: true, ExpiresAt: &now, Models: []supplyModel{{Valid: true}}}}
	r := buildSupplySignal(now, nil, p, accounts, nil)
	if r.Summary.Ready != 0 || r.Recommendation.AddNow != 1 {
		t.Fatal("expired State counted as supply")
	}
	p.MinReady = 0
	r = buildSupplySignal(now, nil, p, accounts, nil)
	if r.Recommendation.AddNow != 0 || r.Recommendation.Action != "observe" {
		t.Fatal("no traffic must not open new account windows")
	}
}

func TestSupplyPolicyRejectsInvalidInput(t *testing.T) {
	for _, query := range []string{"account_budget_usd=NaN", "sustainable_usd_per_hour=Inf", "productive_minutes=0", "pending_accounts=-1", "min_ready_accounts=1.2", "demand_usd_per_hour=NaN", "buffer_minutes=16"} {
		t.Run(query, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("GET", "/?"+query, nil)
			if _, err := parseSupplyPolicy(c); err == nil {
				t.Fatal("invalid policy accepted")
			}
		})
	}
}

func TestSupplyEndpointUsesAdminAuthenticationAndDoesNotLeakSecrets(t *testing.T) {
	h, ids, _ := newPagedAccountsHandler(t)
	h.ipv6State = ipv6state.New(h.db, h.store, nil, nil, nil)
	if err := h.ipv6State.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.ipv6State.Stop)
	h.adminSecretEnv = "supply-admin-secret"
	router := gin.New()
	group := router.Group("/api/admin")
	group.Use(h.adminAuthMiddleware())
	group.GET("/state-pool/supply-signal", h.GetSupplySignal)
	request := func(key, query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/state-pool/supply-signal"+query, nil)
		if key != "" {
			req.Header.Set("X-Admin-Key", key)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	if got := request("", ""); got.Code != 401 {
		t.Fatalf("no auth status=%d", got.Code)
	}
	w := request("supply-admin-secret", "")
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var response supplyResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Summary.Total != len(ids) || response.Recommendation.AddNow != 0 || response.DataQuality != "unavailable" {
		t.Fatalf("response=%+v", response)
	}
	for _, secret := range []string{"secret-codex", "refresh_token", "access_token", "credential_hash", "supply-admin-secret"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("leaked %s", secret)
		}
	}
	if got := request("supply-admin-secret", "?models=unconfigured-model"); got.Code != 400 {
		t.Fatal("unsupported model accepted")
	}
}

func TestSupplyEndpointCountsReadyAccountsByExactModelAndCurrentAuthorization(t *testing.T) {
	h, ids, _ := newPagedAccountsHandler(t)
	ctx := context.Background()
	if err := h.db.InitIPv6State(ctx); err != nil {
		t.Fatal(err)
	}
	h.ipv6State = ipv6state.New(h.db, h.store, nil, nil, nil)
	config := h.ipv6State.Status().Config
	config.Enabled, config.Models = true, []string{"gpt-5.6-sol", "gpt-6-astra"}
	if err := h.ipv6State.Configure(ctx, config); err != nil {
		t.Fatal(err)
	}
	account := h.store.FindByID(ids[0])
	account.Mu().Lock()
	account.AccessToken = "h." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"supply-member"}`)) + ".sig"
	account.AccountID = "supply-workspace"
	account.Mu().Unlock()
	identity, err := statepool.Snapshot(account, "")
	if err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 217)
	raw[0] = 0x80
	issued := time.Now().Unix() - 60
	binary.BigEndian.PutUint64(raw[1:9], uint64(issued))
	value := base64.URLEncoding.EncodeToString(raw)
	if err := h.ipv6State.Import(ctx, ipv6state.Portable{Format: "codex2api-ipv6-292-v1", MemberHash: identity.MemberHash, WorkspaceHash: identity.WorkspaceHash, Model: "gpt-6-astra", Value: value}); err != nil {
		t.Fatal(err)
	}
	request := func(query string) supplyResponse {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", "/?"+query, nil)
		h.GetSupplySignal(c)
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), value) {
			t.Fatal("State leaked")
		}
		var r supplyResponse
		if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := request(""); r.Summary.Ready != 0 {
		t.Fatal("partial model coverage counted as full supply")
	}
	r := request("models=gpt-6-astra")
	if r.Summary.Ready != 1 || r.Accounts[0].OnlineAt == nil || r.Accounts[0].ExpiresAt.Unix() != issued+3600 {
		t.Fatalf("ready projection=%+v", r)
	}
	h.store.MarkCooldownWithError(account, time.Minute, "unauthorized", "token_revoked")
	r = request("models=gpt-6-astra")
	if r.Summary.Ready != 0 || r.Summary.Valid != 1 || r.Summary.Relogin != 1 || !r.Accounts[0].NeedsRelogin {
		t.Fatal("current 401 was counted as supply")
	}
	h.store.ClearCooldown(account)
	r = request("models=gpt-6-astra")
	if r.Summary.Ready != 1 || r.Summary.Relogin != 0 {
		t.Fatal("historical 401 triggered repeated relogin")
	}
}
