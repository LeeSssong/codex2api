package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/codex2api/egressipv6"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAccountIPv6NewImportDefaultsAndExplicitChoices(t *testing.T) {
	for _, tt := range []struct {
		name     string
		settings importSettings
		token    importToken
		enabled  bool
		timezone string
	}{
		{name: "absent", enabled: true, timezone: "America/New_York"},
		{name: "blank", settings: importSettings{proxySpecified: true}, timezone: "America/New_York"},
		{name: "proxy", settings: importSettings{defaultProxyURL: "http://127.0.0.1:8080"}, timezone: "America/New_York"},
		{name: "false", settings: importSettings{ipv6Enabled: boolPtrIPv6(false)}, timezone: "America/New_York"},
		{name: "no timezone default", settings: importSettings{defaultTimezone: stringPtrIPv6("")}, enabled: true},
		{name: "source blank beats form default", settings: importSettings{ipv6Enabled: boolPtrIPv6(true)}, token: importToken{proxySpecified: true}, timezone: "America/New_York"},
		{name: "source false beats form default", settings: importSettings{ipv6Enabled: boolPtrIPv6(true)}, token: importToken{ipv6Enabled: boolPtrIPv6(false)}, timezone: "America/New_York"},
		{name: "source timezone", token: importToken{timezone: stringPtrIPv6("Asia/Tokyo")}, enabled: true, timezone: "Asia/Tokyo"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h, store, db := newImportEndToEndHandler(t)
			store.SetLazyMode(true)
			tt.token.refreshToken = "rt-ipv6-" + tt.name
			runImportAccountsCommon(t, h, []importToken{tt.token}, tt.settings)
			row := accountRowByRefreshToken(t, db, tt.token.refreshToken)
			if got := row.GetCredentialBool(auth.IPv6EgressCredentialKey); got != tt.enabled {
				t.Fatalf("membership=%v want %v", got, tt.enabled)
			}
			if got := row.GetCredential("timezone"); got != tt.timezone {
				t.Fatalf("timezone=%q want %q", got, tt.timezone)
			}
		})
	}
}
func boolPtrIPv6(b bool) *bool       { return &b }
func stringPtrIPv6(s string) *string { return &s }
func TestSchedulerIPv6SelectionParsing(t *testing.T) {
	for _, tt := range []struct {
		body    string
		enabled bool
		proxy   string
		bad     bool
	}{
		{body: `{"ipv6_egress_enabled":true}`, enabled: true},
		{body: `{"proxy_url":""}`},
		{body: `{"proxy_url":"http://127.0.0.1:8080"}`, proxy: "http://127.0.0.1:8080"},
		{body: `{"ipv6_egress_enabled":true,"proxy_url":"http://127.0.0.1:8080"}`, bad: true},
	} {
		var req updateAccountSchedulerReq
		if err := json.Unmarshal([]byte(tt.body), &req); err != nil {
			t.Fatal(err)
		}
		u, err := parseAccountSchedulerUpdate(req)
		if tt.bad {
			if err == nil {
				t.Fatal("accepted conflicting routes")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if u.CredentialUpdates[auth.IPv6EgressCredentialKey] != tt.enabled {
			t.Fatalf("%s: %v", tt.body, u.CredentialUpdates)
		}
	}
}

func TestAccountIPv6DuplicateImportPreservesAndExplicitlyOverrides(t *testing.T) {
	h, store, db := newImportEndToEndHandler(t)
	store.SetLazyMode(true)
	token := importToken{refreshToken: "duplicate-ipv6-token", accessToken: "fixture-at"}
	runImportAccountsCommon(t, h, []importToken{token}, importSettings{proxySpecified: true, defaultProxyURL: "http://127.0.0.1:8080", defaultTimezone: stringPtrIPv6("Asia/Tokyo")})
	row := accountRowByRefreshToken(t, db, token.refreshToken)
	if _, err := db.BatchUpdateAccountMetadata(context.Background(), []int64{row.ID}, database.BatchAccountMetadataUpdate{Enabled: database.OptionalBool{Set: true, Value: false}}); err != nil {
		t.Fatal(err)
	}
	runImportAccountsCommon(t, h, []importToken{token}, importSettings{})
	row = accountRowByRefreshToken(t, db, token.refreshToken)
	if row.ProxyURL != "http://127.0.0.1:8080" || row.GetCredential("timezone") != "Asia/Tokyo" || row.Enabled {
		t.Fatalf("duplicate changed preferences/enablement: %+v", row)
	}
	runImportAccountsCommon(t, h, []importToken{token}, importSettings{ipv6Enabled: boolPtrIPv6(true)})
	row = accountRowByRefreshToken(t, db, token.refreshToken)
	if row.ProxyURL != "" || !row.GetCredentialBool(auth.IPv6EgressCredentialKey) || row.GetCredential("timezone") != "Asia/Tokyo" || row.Enabled {
		t.Fatal("explicit IPv6 override or preservation failed")
	}
}

func TestAccountIPv6JSONSourcePreferences(t *testing.T) {
	for _, raw := range []string{
		`[{"refresh_token":"r1","proxy_url":"","timezone":"Asia/Tokyo"}]`,
		`{"accounts":[{"proxy_url":"","credentials":{"refresh_token":"r1","timezone":"Asia/Tokyo"}}]}`,
	} {
		tokens, err := parseImportJSONTokensStream(strings.NewReader(raw))
		if err != nil || len(tokens) != 1 {
			t.Fatalf("%s: %+v %v", raw, tokens, err)
		}
		pref := (importSettings{}).applyPreferences(tokenCredentialSeed{}, tokens[0])
		if *pref.newIPv6Enabled || pref.timezone == nil || *pref.timezone != "Asia/Tokyo" {
			t.Fatalf("source preferences lost: %+v", pref)
		}
	}
}

func TestAccountIPv6DetailsShowOnlyCurrentBindingWithoutClaim(t *testing.T) {
	h, store, db := newImportEndToEndHandler(t)
	store.SetLazyMode(true)
	runImportAccountsCommon(t, h, []importToken{{refreshToken: "pending-ipv6"}}, importSettings{})
	row := accountRowByRefreshToken(t, db, "pending-ipv6")
	if got := h.accountIPv6IP(row); got != "" {
		t.Fatalf("pending IP=%q", got)
	}
	ctx := context.Background()
	cfg, _ := db.IPv6EgressConfig(ctx)
	cfg.Enabled = true
	if err := db.SaveIPv6EgressConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	manager := egressipv6.New(db)
	manager.LocalIPs = func() ([]string, error) { return []string{"2604::1"}, nil }
	route, release, err := manager.AcquireAccount(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	resp := h.buildAccountResponse(row, nil, nil, nil, nil, true)
	if !resp.IPv6EgressEnabled || resp.IPv6EgressIP != route.Binding.IP {
		t.Fatalf("details: enabled=%v IP=%q", resp.IPv6EgressEnabled, resp.IPv6EgressIP)
	}
	projection, err := db.ListAccountListProjection(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(projection) != 1 || !projection[0].GetCredentialBool(auth.IPv6EgressCredentialKey) {
		t.Fatalf("list lost membership: %+v", projection)
	}
}

func TestAccountIPv6SchedulerOptOutRetiresAndPreservesDisabled(t *testing.T) {
	h, store, db := newImportEndToEndHandler(t)
	store.SetLazyMode(true)
	runImportAccountsCommon(t, h, []importToken{{refreshToken: "scheduler-ipv6"}}, importSettings{})
	row := accountRowByRefreshToken(t, db, "scheduler-ipv6")
	ctx := context.Background()
	db.BatchUpdateAccountMetadata(ctx, []int64{row.ID}, database.BatchAccountMetadataUpdate{Enabled: database.OptionalBool{Set: true, Value: false}})
	cfg, _ := db.IPv6EgressConfig(ctx)
	cfg.Enabled = true
	db.SaveIPv6EgressConfig(ctx, cfg)
	manager := egressipv6.New(db)
	manager.LocalIPs = func() ([]string, error) { return []string{"2604::1"}, nil }
	previous := egressipv6.Current()
	egressipv6.Install(manager)
	defer egressipv6.Install(previous)
	_, release, err := manager.AcquireAccount(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	response := patchAccountScheduler(t, h, row.ID, `{"proxy_url":""}`)
	if response.Code != 200 {
		t.Fatalf("scheduler: %d %s", response.Code, response.Body.String())
	}
	row = accountRowByRefreshToken(t, db, "scheduler-ipv6")
	if row.Enabled || row.GetCredentialBool(auth.IPv6EgressCredentialKey) {
		t.Fatal("opt-out altered enablement or failed")
	}
	if runtime := store.FindByID(row.ID); runtime != nil {
		_, enabled := runtime.IPv6EgressSelection()
		if enabled {
			t.Fatal("runtime membership not updated")
		}
	}
	if got := h.accountIPv6IP(row); got != "" {
		t.Fatal("retired IP exposed")
	}
	bindings, cooldowns, _ := db.IPv6EgressStatus(ctx, time.Now().Unix())
	if len(bindings) != 1 || len(cooldowns) != 0 {
		t.Fatal("retired while HTTP lease active")
	}
	release()
	bindings, cooldowns, _ = db.IPv6EgressStatus(ctx, time.Now().Unix())
	if len(bindings) != 0 || len(cooldowns) != 1 {
		t.Fatal("retirement did not drain")
	}
	var req updateAccountSchedulerReq
	json.Unmarshal([]byte(`{"ipv6_egress_enabled":false}`), &req)
	u, err := parseAccountSchedulerUpdate(req)
	if err != nil || !u.hasChanges() {
		t.Fatal("batch false-only request ignored")
	}
}

func TestAccountIPv6SpecialEntryPreferences(t *testing.T) {
	for _, raw := range []string{`{}`, `{"ipv6_egress_enabled":true,"default_timezone":""}`, `{"proxy_url":""}`, `{"proxy_url":"http://127.0.0.1:8080"}`, `{"ipv6_egress_enabled":false}`} {
		var req nativeJSONPreferences
		if err := json.Unmarshal([]byte(raw), &req); err != nil {
			t.Fatal(err)
		}
		settings, err := req.settings()
		if err != nil {
			t.Fatal(err)
		}
		seed := settings.applyPreferences(tokenCredentialSeed{}, importToken{})
		enabled := raw == `{}` || strings.Contains(raw, `"ipv6_egress_enabled":true`)
		if *seed.newIPv6Enabled != enabled {
			t.Fatalf("%s membership=%v", raw, *seed.newIPv6Enabled)
		}
		timezone := "America/New_York"
		if strings.Contains(raw, `"default_timezone":""`) {
			timezone = ""
		}
		if *seed.newTimezone != timezone {
			t.Fatalf("%s timezone=%s", raw, *seed.newTimezone)
		}
	}
	var req nativeJSONPreferences
	json.Unmarshal([]byte(`{"ipv6_egress_enabled":true,"proxy_url":"http://127.0.0.1:8080"}`), &req)
	if _, err := req.settings(); err == nil {
		t.Fatal("accepted conflicting routes")
	}
}

func TestAccountIPv6RemoteSourcePreferences(t *testing.T) {
	token, ok := sub2apiAccountToImportToken(sub2apiAccountInternal{Credentials: map[string]interface{}{"refresh_token": "r1", "timezone": "Asia/Tokyo", "proxy_url": "", "ipv6_egress_enabled": false}})
	if !ok || token.timezone == nil || *token.timezone != "Asia/Tokyo" || !token.proxySpecified || token.ipv6Enabled == nil || *token.ipv6Enabled {
		t.Fatalf("lost source preferences: %+v", token)
	}
}

func TestAccountIPv6AgentDedicatedImport(t *testing.T) {
	for _, batch := range []bool{false, true} {
		h, store, db := newImportEndToEndHandler(t)
		store.SetLazyMode(true)
		raw, _ := json.Marshal(map[string]interface{}{"auth_mode": "agentIdentity", "agent_runtime_id": "agent-dedicated", "agent_private_key": newTestAgentPrivateKey(t), "account_id": "account", "chatgpt_user_id": "user", "timezone": "Asia/Tokyo"})
		body := map[string]interface{}{"ipv6_egress_enabled": true, "default_timezone": ""}
		if batch {
			body["files"] = []string{string(raw)}
		} else {
			body["auth_json"] = string(raw)
		}
		encoded, _ := json.Marshal(body)
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(encoded)))
		c.Request.Header.Set("Content-Type", "application/json")
		if batch {
			h.BatchImportCodexAgentIdentity(c)
		} else {
			h.ImportCodexAgentIdentity(c)
		}
		if recorder.Code != 200 {
			t.Fatalf("batch=%v: %d %s", batch, recorder.Code, recorder.Body.String())
		}
		rows, err := db.ListActive(context.Background())
		if err != nil || len(rows) != 1 || !rows[0].GetCredentialBool(auth.IPv6EgressCredentialKey) || rows[0].GetCredential("timezone") != "Asia/Tokyo" {
			t.Fatalf("batch=%v rows=%+v err=%v", batch, rows, err)
		}
	}
}

func TestAccountIPv6OAuthSessionDefaultAndPoolOff(t *testing.T) {
	h, store, db := newImportEndToEndHandler(t)
	store.SetLazyMode(true)
	m := egressipv6.New(db)
	m.LocalIPs = func() ([]string, error) { return []string{"2606:4700::1234"}, nil }
	old := egressipv6.Current()
	egressipv6.Install(m)
	t.Cleanup(func() { egressipv6.Install(old) })
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
	c.Request.Header.Set("Content-Type", "application/json")
	h.GenerateOAuthURL(c)
	if recorder.Code != 200 {
		t.Fatalf("generate: %d %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	sess, ok := globalOAuthStore.get(response.SessionID)
	if !ok || sess.Preferences == nil {
		t.Fatal("missing session selection")
	}
	pref := sess.Preferences.applyPreferences(tokenCredentialSeed{}, importToken{})
	if !*pref.newIPv6Enabled || pref.newTimezone == nil || *pref.newTimezone != "" {
		t.Fatal("OAuth add defaults must enroll without import timezone")
	}
	body, _ := json.Marshal(map[string]string{"session_id": response.SessionID, "state": sess.State, "code": "synthetic-code"})
	recorder = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	h.ExchangeOAuthCode(c)
	if recorder.Code != http.StatusBadGateway || !strings.Contains(recorder.Body.String(), "IPv6") {
		t.Fatalf("pool off: %d %s", recorder.Code, recorder.Body.String())
	}
	rows, err := db.ListActive(context.Background())
	if err != nil || len(rows) != 0 {
		t.Fatalf("failed OAuth created an account: %v %v", len(rows), err)
	}
	globalOAuthStore.delete(response.SessionID)
}

type accountIPv6RoundTrip func(*http.Request) (*http.Response, error)

func (f accountIPv6RoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAccountIPv6OAuthReauthSwitchRetiresAndPreservesDisabled(t *testing.T) {
	h, store, db := newImportEndToEndHandler(t)
	store.SetLazyMode(true)
	ctx := context.Background()
	id, err := db.InsertAccountWithCredentials(ctx, "reauth", map[string]interface{}{"refresh_token": "old", "ipv6_egress_enabled": true, "timezone": "Asia/Tokyo"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.BatchUpdateAccountMetadata(ctx, []int64{id}, database.BatchAccountMetadataUpdate{Enabled: database.OptionalBool{Set: true, Value: false}}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := db.IPv6EgressConfig(ctx)
	cfg.Enabled = true
	db.SaveIPv6EgressConfig(ctx, cfg)
	manager := egressipv6.New(db)
	manager.LocalIPs = func() ([]string, error) { return []string{"2606:4700::1234"}, nil }
	oldManager := egressipv6.Current()
	egressipv6.Install(manager)
	t.Cleanup(func() { egressipv6.Install(oldManager) })
	_, release, err := manager.AcquireAccount(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	const proxyURL = "http://reauth-proxy.invalid:8080"
	client := auth.BuildHTTPClient(proxyURL)
	oldTransport := client.Transport
	client.Transport = accountIPv6RoundTrip(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != oauthTokenURL || req.Header.Get("X-Resin-Account") != "" {
			t.Fatalf("wrong exchange route: %s", req.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"access_token":"new","refresh_token":"new-refresh","expires_in":3600}`))}, nil
	})
	t.Cleanup(func() { client.Transport = oldTransport })
	sessionID := "ipv6-reauth-switch"
	globalOAuthStore.set(sessionID, &oauthSession{State: "state", CodeVerifier: "verifier", RedirectURI: oauthDefaultRedirectURI, CreatedAt: time.Now()})
	t.Cleanup(func() { globalOAuthStore.delete(sessionID) })
	body, _ := json.Marshal(map[string]interface{}{"session_id": sessionID, "state": "state", "code": "code", "ipv6_egress_enabled": false, "proxy_url": proxyURL})
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(id, 10)}}
	c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	h.UpdateOAuthAccountCode(c)
	if rec.Code != 200 {
		t.Fatalf("reauth: %d %s", rec.Code, rec.Body.String())
	}
	row, err := db.GetAccountByID(ctx, id)
	if err != nil || row.Enabled || row.GetCredentialBool(auth.IPv6EgressCredentialKey) || row.ProxyURL != proxyURL || row.GetCredential("timezone") != "Asia/Tokyo" {
		t.Fatalf("preferences/enablement: %+v %v", row, err)
	}
	bindings, cooldowns, _ := db.IPv6EgressStatus(ctx, time.Now().Unix())
	if len(bindings) != 1 || len(cooldowns) != 0 || bindings[0].Reason != "retired" {
		t.Fatalf("retired before drain: %+v %+v", bindings, cooldowns)
	}
	release()
	bindings, cooldowns, _ = db.IPv6EgressStatus(ctx, time.Now().Unix())
	if len(bindings) != 0 || len(cooldowns) != 1 {
		t.Fatal("reauth failed to drain retirement")
	}
}

func TestAccountIPv6OAuthReauthOmittedPreservesLegacyPreferences(t *testing.T) {
	h, store, db := newImportEndToEndHandler(t)
	store.SetLazyMode(true)
	// Keep this fixture local: the exchange server below is the intended Resin
	// test route, and a configured proxy now correctly takes precedence over it.
	newOAuthExchangeTestServer(t)
	ctx := context.Background()
	id, err := db.InsertAccountWithCredentials(ctx, "legacy-reauth", map[string]interface{}{"refresh_token": "old", "timezone": "Asia/Tokyo"}, "")
	if err != nil {
		t.Fatal(err)
	}
	db.BatchUpdateAccountMetadata(ctx, []int64{id}, database.BatchAccountMetadataUpdate{Enabled: database.OptionalBool{Set: true, Value: false}})
	sessionID := "ipv6-reauth-omitted"
	globalOAuthStore.set(sessionID, &oauthSession{State: "state", CodeVerifier: "verifier", RedirectURI: oauthDefaultRedirectURI, CreatedAt: time.Now(), Preferences: &importSettings{ipv6Enabled: boolPtrIPv6(true)}})
	t.Cleanup(func() { globalOAuthStore.delete(sessionID) })
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(id, 10)}}
	c.Request = newOAuthEditRequest(sessionID, "code", "state", "")
	h.UpdateOAuthAccountCode(c)
	if rec.Code != 200 {
		t.Fatalf("reauth: %d %s", rec.Code, rec.Body.String())
	}
	row, err := db.GetAccountByID(ctx, id)
	if err != nil || row.Enabled || row.GetCredentialOptionalBool(auth.IPv6EgressCredentialKey) != nil || row.ProxyURL != "" || row.GetCredential("timezone") != "Asia/Tokyo" {
		t.Fatalf("omitted preferences changed: %+v %v", row, err)
	}
}

func TestAccountIPv6DuplicateImportSourceTimezoneOnlyAppliesOnCreation(t *testing.T) {
	for _, changedCredentials := range []bool{false, true} {
		for _, sourceTimezone := range []string{"America/New_York", ""} {
			t.Run(fmt.Sprintf("changed_credentials=%v/source_timezone=%q", changedCredentials, sourceTimezone), func(t *testing.T) {
				h, store, db := newImportEndToEndHandler(t)
				store.SetLazyMode(true)
				token := importToken{
					refreshToken: "timezone-original-rt",
					accessToken:  "timezone-original-at",
					idToken:      makeOAuthTestIDToken("timezone@example.test", "timezone-workspace", "team"),
					timezone:     stringPtrIPv6("Asia/Tokyo"),
				}
				runImportAccountsCommon(t, h, []importToken{token}, importSettings{})
				row := accountRowByRefreshToken(t, db, token.refreshToken)
				if got := row.GetCredential("timezone"); got != "Asia/Tokyo" {
					t.Fatalf("new account lost source timezone: %q", got)
				}
				// A user's explicit scheduler choice remains authoritative on reimport.
				response := patchAccountScheduler(t, h, row.ID, `{"timezone":"Europe/Paris"}`)
				if response.Code != http.StatusOK {
					t.Fatalf("explicit timezone update: %d %s", response.Code, response.Body.String())
				}
				if _, err := db.BatchUpdateAccountMetadata(context.Background(), []int64{row.ID}, database.BatchAccountMetadataUpdate{Enabled: database.OptionalBool{Set: true, Value: false}}); err != nil {
					t.Fatal(err)
				}
				if changedCredentials {
					token.refreshToken = "timezone-rotated-rt"
					token.accessToken = "timezone-rotated-at"
				}
				token.timezone = &sourceTimezone
				runImportAccountsCommon(t, h, []importToken{token}, importSettings{defaultTimezone: stringPtrIPv6("America/New_York")})
				row = accountRowByRefreshToken(t, db, token.refreshToken)
				if row.GetCredential("timezone") != "Europe/Paris" || row.Enabled {
					t.Fatalf("duplicate import changed existing timezone or disabled state: timezone=%q enabled=%v", row.GetCredential("timezone"), row.Enabled)
				}
				account := store.FindByID(row.ID)
				if account == nil {
					t.Fatal("missing runtime account")
				}
				account.Mu().RLock()
				runtimeTimezone := account.Timezone
				account.Mu().RUnlock()
				if runtimeTimezone != "Europe/Paris" {
					t.Fatalf("runtime timezone changed: %q", runtimeTimezone)
				}
			})
		}
	}
}

func TestAccountIPv6ImportExplicitSourceBlankProxyBeatsFormDefault(t *testing.T) {
	h, store, db := newImportEndToEndHandler(t)
	store.SetLazyMode(true)
	token := importToken{refreshToken: "source-blank-proxy-rt", accessToken: "source-blank-proxy-at", proxySpecified: true, ipv6Enabled: boolPtrIPv6(false)}
	settings := importSettings{importProxies: true, proxySpecified: true, defaultProxyURL: "http://form-proxy.invalid:8080"}
	runImportAccountsCommon(t, h, []importToken{token}, settings)
	row := accountRowByRefreshToken(t, db, token.refreshToken)
	if row.ProxyURL != "" || row.GetCredentialBool(auth.IPv6EgressCredentialKey) {
		t.Fatalf("source opt-out inherited form proxy: proxy=%q ipv6=%v", row.ProxyURL, row.GetCredentialBool(auth.IPv6EgressCredentialKey))
	}
	account := store.FindByID(row.ID)
	if account == nil || !account.HostIPv4Egress() || store.ResolveProxyForAccount(account) != "" {
		t.Fatal("source blank opt-out did not use host IPv4")
	}
}
