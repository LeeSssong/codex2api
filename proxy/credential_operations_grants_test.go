package proxy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/codex2api/credentialops"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
)

func credentialGrantFixture(t *testing.T) (*database.DB, *auth.Store, *auth.Account) {
	t.Helper()
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "grants.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	id, err := db.InsertAccountWithCredentials(ctx, "dual", map[string]any{"email": "dual@example.test", "workspace_id": "workspace", "plan_type": "plus", "access_token": "codex-token", "refresh_token": "codex-rt"}, "")
	if err != nil {
		t.Fatal(err)
	}
	row, _ := db.GetAccountByID(ctx, id)
	cfg := database.CredentialOpsLoginConfigRow{AccountID: id, LoginEmail: "dual@example.test", CredentialMode: "password_totp", Engine: "local_worker", ProxySource: "direct", PasswordCiphertext: "enc:fixture"}
	if err = db.UpsertCredentialOpsLoginConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	cfg.OAuthProfile = "excel"
	job, err := db.CreateCredentialOpsTaskWithConfig(ctx, id, row.CredentialGeneration, cfg)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := db.ClaimCredentialOpsTaskWithCapabilities(ctx, "worker", time.Minute, []string{"excel_oauth_v1"})
	if err != nil || claim == nil {
		t.Fatalf("claim: %v", err)
	}
	_, err = db.CommitCredentialOpsLogin(ctx, job.ID, "worker", claim.Attempt, map[string]any{"email": "dual@example.test", "workspace_id": "workspace", "client_id": credentialExcelClientID, "access_token": "excel-token", "refresh_token": "excel-rt", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	store := auth.NewStore(db, nil, &database.SystemSettings{MaxConcurrency: 2})
	account := &auth.Account{DBID: id, AccountID: "workspace", AccessToken: "codex-token", RefreshToken: "codex-rt"}
	old := credentialGrantProvider.Load()
	t.Cleanup(func() { credentialGrantProvider.Store(old) })
	ConfigureCredentialOperations(db, store)
	return db, store, account
}

func TestCredentialOperationsBPSUsesIndependentExcelGrant(t *testing.T) {
	_, _, account := credentialGrantFixture(t)
	installBasispointsTransport(t, account, func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "Bearer excel-token" {
			t.Fatalf("wrong OAuth grant selected")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\n"))}, nil
	})
	resp, err := executeBasispointsRequest(context.Background(), account, []byte(`{"model":"gpt-6-astra","input":"hello"}`), "", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if account.GetAccessToken() != "codex-token" {
		t.Fatal("Excel request replaced primary token")
	}
}

func TestCredentialOperationsExcel401PreservesCodexAndInvalidatesOnlyUsedGeneration(t *testing.T) {
	db, _, account := credentialGrantFixture(t)
	before, _ := db.GetAccountByID(context.Background(), account.ID())
	installBasispointsTransport(t, account, func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 401, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"invalid token"}`))}, nil
	})
	resp, err := executeBasispointsRequest(context.Background(), account, []byte(`{"model":"gpt-6-astra","input":"hello"}`), "", "", "", nil)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("Excel auth rejection was not isolated as a local recoverable error")
	}
	grant, _ := db.GetCredentialOpsProfileGrant(context.Background(), account.ID(), "excel")
	after, _ := db.GetAccountByID(context.Background(), account.ID())
	if !grant.Invalidated || after.GetCredential("access_token") != "codex-token" || before.CredentialGeneration != after.CredentialGeneration {
		t.Fatal("Excel failure damaged primary Codex authorization")
	}
}

func TestCredentialOperationsLateExcel401CannotDeleteNewGrant(t *testing.T) {
	db, _, account := credentialGrantFixture(t)
	installBasispointsTransport(t, account, func(req *http.Request) (*http.Response, error) {
		grant, _ := db.GetCredentialOpsProfileGrant(context.Background(), account.ID(), "excel")
		_, err := db.UpdateCredentialOpsProfileGrantCAS(context.Background(), account.ID(), "excel", grant.Generation, map[string]any{"access_token": "new-excel-token", "refresh_token": "new-excel-rt", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)})
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 401, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"expired"}`))}, nil
	})
	resp, _ := executeBasispointsRequest(context.Background(), account, []byte(`{"model":"gpt-6-astra","input":"hello"}`), "", "", "", nil)
	if resp != nil {
		resp.Body.Close()
	}
	grant, _ := db.GetCredentialOpsProfileGrant(context.Background(), account.ID(), "excel")
	if grant.Invalidated || grant.Credentials["access_token"] != "new-excel-token" {
		t.Fatal("late rejection invalidated newer grant")
	}
}

func TestCredentialOperationsPendingExcelRefreshCannotBeDispatched(t *testing.T) {
	db, _, account := credentialGrantFixture(t)
	grant, err := db.GetCredentialOpsProfileGrant(context.Background(), account.ID(), "excel")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := db.BeginCredentialOpsProfileRefresh(context.Background(), account.ID(), "excel", grant.Generation); err != nil || !ok {
		t.Fatalf("reserve refresh: %v", err)
	}
	called := false
	installBasispointsTransport(t, account, func(*http.Request) (*http.Response, error) { called = true; return nil, nil })
	response, err := executeBasispointsRequest(context.Background(), account, []byte(`{"model":"gpt-6-astra","input":"hello"}`), "", "", "", nil)
	if response != nil {
		response.Body.Close()
	}
	if err == nil || called {
		t.Fatal("pending refresh grant was dispatched instead of requiring durable recovery")
	}
}

func TestCredentialOperationsExcelOnlyAccountCannotUseNativeCodex(t *testing.T) {
	previous := CurrentRuntimeSettings()
	t.Cleanup(func() { ApplyRuntimeSettings(previous) })
	ApplyRuntimeSettings(DefaultRuntimeSettings())
	account := &auth.Account{DBID: 953, AccountID: "workspace", AccessToken: "excel-only", OAuthClientID: auth.ExcelClientID}
	called := false
	native := func(context.Context, *auth.Account, []byte, string, string, string, *DeviceProfileConfig, http.Header) (*http.Response, error) {
		called = true
		return nil, nil
	}
	_, err := executeCodexRoute(context.Background(), account, []byte(`{"model":"gpt-6-astra","input":"hello"}`), "", "", "", nil, nil, false, native)
	if err == nil || called {
		t.Fatal("Excel-only grant entered native Codex executor")
	}
}

func TestCredentialOperationsFreeAccountKeepsNativeRouteWhenBPSEnabled(t *testing.T) {
	enableBasispointsForTest(t)
	account := &auth.Account{DBID: 954, AccountID: "workspace", AccessToken: "free-codex", PlanType: "free"}
	called := false
	native := func(context.Context, *auth.Account, []byte, string, string, string, *DeviceProfileConfig, http.Header) (*http.Response, error) {
		called = true
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	response, err := executeCodexRoute(context.Background(), account, []byte(`{"model":"gpt-6-astra","input":"hello"}`), "", "", "", nil, nil, false, native)
	if response != nil {
		response.Body.Close()
	}
	if err != nil || !called {
		t.Fatal("free account was blocked behind an ineligible Excel authorization", err)
	}
}

func TestCredentialOperationsBPSAcquisitionHonorsSavedGlobalLoginEgress(t *testing.T) {
	for _, invalidated := range []bool{false, true} {
		t.Run(fmt.Sprint("invalidated=", invalidated), func(t *testing.T) {
			t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
			db, e := database.New("sqlite", filepath.Join(t.TempDir(), "login-egress.db"))
			if e != nil {
				t.Fatal(e)
			}
			defer db.Close()
			ctx := context.Background()
			id, e := db.InsertAccountWithCredentials(ctx, "egress", map[string]any{"email": "egress@example.test", "workspace_id": "workspace", "plan_type": "plus", "access_token": "codex", "refresh_token": "rt"}, "http://account-a:9010")
			if e != nil {
				t.Fatal(e)
			}
			cfg := database.CredentialOpsLoginConfigRow{AccountID: id, LoginEmail: "egress@example.test", CredentialMode: "password_totp", Engine: "local_worker", ProxySource: "global", PasswordCiphertext: "sealed", ResolvedProxyURL: "http://global-b:9020", RouteResolved: true}
			if e = db.UpsertCredentialOpsLoginConfig(ctx, cfg); e != nil {
				t.Fatal(e)
			}
			if invalidated {
				row, _ := db.GetAccountByID(ctx, id)
				cfg.OAuthProfile = "excel"
				job, e := db.CreateCredentialOpsTaskWithConfig(ctx, id, row.CredentialGeneration, cfg)
				if e != nil {
					t.Fatal(e)
				}
				claim, e := db.ClaimCredentialOpsTaskWithCapabilities(ctx, "worker", time.Minute, []string{"excel_oauth_v1"})
				if e != nil || claim == nil {
					t.Fatal(e)
				}
				if _, e = db.CommitCredentialOpsLogin(ctx, job.ID, "worker", claim.Attempt, map[string]any{"email": "egress@example.test", "workspace_id": "workspace", "access_token": "old-excel", "refresh_token": "excel-rt"}); e != nil {
					t.Fatal(e)
				}
				grant, _ := db.GetCredentialOpsProfileGrant(ctx, id, "excel")
				if _, e = db.InvalidateCredentialOpsProfileGrantCAS(ctx, id, "excel", grant.Generation); e != nil {
					t.Fatal(e)
				}
			}
			store := auth.NewStore(db, nil, &database.SystemSettings{MaxConcurrency: 2, ProxyURL: "http://global-b:9020"})
			defer store.Stop()
			account := &auth.Account{DBID: id, AccountID: "workspace", AccessToken: "codex", ProxyURL: "http://account-a:9010"}
			previous := credentialGrantProvider.Load()
			defer credentialGrantProvider.Store(previous)
			ConfigureCredentialOperations(db, store)
			if _, _, e = resolveBasispointsGrant(ctx, account, account.ProxyURL); e == nil {
				t.Fatal("missing Excel grant unexpectedly usable")
			}
			claim, e := db.ClaimCredentialOpsTaskWithCapabilities(ctx, "worker", time.Minute, []string{"excel_oauth_v1"})
			if e != nil || claim == nil {
				t.Fatalf("acquisition not queued: %#v %v", claim, e)
			}
			input, e := db.CredentialOpsTaskConfig(ctx, claim.ID)
			if e != nil {
				t.Fatal(e)
			}
			_, dec := credentialops.EnvCrypto()
			route, e := dec(input.ProxyURLCiphertext)
			if e != nil || route != "http://global-b:9020" {
				t.Fatalf("Excel login used %q, want saved global login route: %v", route, e)
			}
		})
	}
}

func credentialExcelJWT(claims map[string]any) string {
	raw, _ := json.Marshal(claims)
	return "e30." + base64.RawURLEncoding.EncodeToString(raw) + ".signature"
}
func credentialExcelIdentityTokens(email, user, workspace, client string) (string, string) {
	authClaims := map[string]any{"chatgpt_account_id": workspace, "chatgpt_user_id": user}
	return credentialExcelJWT(map[string]any{"client_id": client, "exp": time.Now().Add(time.Hour).Unix(), "https://api.openai.com/auth": authClaims, "https://api.openai.com/profile": map[string]any{"email": email}}), credentialExcelJWT(map[string]any{"aud": client, "email": email, "https://api.openai.com/auth": authClaims})
}
func TestCredentialOperationsExcelRefreshRejectsIdentityDrift(t *testing.T) {
	for _, scenario := range []string{"wrong_client", "different_user", "different_email", "conflicting_id_user", "conflicting_id_workspace", "missing_access_user", "missing_access_workspace", "missing_id_email", "missing_id_audience", "missing_access_expiry", "valid_without_new_id", "valid_without_new_id_or_refresh"} {
		t.Run(scenario, func(t *testing.T) {
			db, store, account := credentialGrantFixture(t)
			defer store.Stop()
			ctx := context.Background()
			oldAT, oldID := credentialExcelIdentityTokens("dual@example.test", "user-dual", "workspace", auth.ExcelClientID)
			original, _ := db.GetCredentialOpsProfileGrant(ctx, account.ID(), "excel")
			if ok, e := db.UpdateCredentialOpsProfileGrantCAS(ctx, account.ID(), "excel", original.Generation, map[string]any{"client_id": auth.ExcelClientID, "access_token": oldAT, "id_token": oldID, "refresh_token": "original-single-use", "email": "dual@example.test", "workspace_id": "workspace", "chatgpt_user_id": "user-dual", "expires_at": time.Now().Add(-time.Minute).Format(time.RFC3339)}); e != nil || !ok {
				t.Fatal(e)
			}
			original, _ = db.GetCredentialOpsProfileGrant(ctx, account.ID(), "excel")
			at, id := credentialExcelIdentityTokens("dual@example.test", "user-dual", "workspace", auth.ExcelClientID)
			switch scenario {
			case "wrong_client":
				at, _ = credentialExcelIdentityTokens("dual@example.test", "user-dual", "workspace", auth.ClientID)
			case "different_user":
				at, id = credentialExcelIdentityTokens("dual@example.test", "other-user", "workspace", auth.ExcelClientID)
			case "different_email":
				at, id = credentialExcelIdentityTokens("other@example.test", "user-dual", "workspace", auth.ExcelClientID)
			case "conflicting_id_user":
				_, id = credentialExcelIdentityTokens("dual@example.test", "other-user", "workspace", auth.ExcelClientID)
			case "conflicting_id_workspace":
				_, id = credentialExcelIdentityTokens("dual@example.test", "user-dual", "other-workspace", auth.ExcelClientID)
			case "missing_access_user":
				at = credentialExcelJWT(map[string]any{"client_id": auth.ExcelClientID, "exp": time.Now().Add(time.Hour).Unix(), "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "workspace"}})
			case "missing_access_workspace":
				at = credentialExcelJWT(map[string]any{"client_id": auth.ExcelClientID, "exp": time.Now().Add(time.Hour).Unix(), "https://api.openai.com/auth": map[string]any{"chatgpt_user_id": "user-dual"}})
			case "missing_access_expiry":
				at = credentialExcelJWT(map[string]any{"client_id": auth.ExcelClientID, "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "workspace", "chatgpt_user_id": "user-dual"}})
			case "missing_id_email":
				id = credentialExcelJWT(map[string]any{"aud": auth.ExcelClientID, "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "workspace", "chatgpt_user_id": "user-dual"}})
			case "missing_id_audience":
				id = credentialExcelJWT(map[string]any{"email": "dual@example.test", "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "workspace", "chatgpt_user_id": "user-dual"}})
			case "valid_without_new_id", "valid_without_new_id_or_refresh":
				id = ""
			}
			returnedRefresh := "rotated-refresh"
			if scenario == "valid_without_new_id_or_refresh" {
				returnedRefresh = ""
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{"access_token": at, "refresh_token": returnedRefresh, "id_token": id, "expires_in": 3600})
			}))
			defer server.Close()
			oldDecorator := auth.CodexHTTPClientDecorator
			defer func() { auth.CodexHTTPClientDecorator = oldDecorator }()
			auth.CodexHTTPClientDecorator = func(_ int64, _ *http.Client, _ bool) *http.Client {
				return &http.Client{Transport: ipv6TestRoundTrip(func(req *http.Request) (*http.Response, error) {
					clone := req.Clone(req.Context())
					local, _ := http.NewRequest(req.Method, server.URL, nil)
					clone.URL = local.URL
					return http.DefaultTransport.RoundTrip(clone)
				})}
			}
			service := credentialGrantProvider.Load()
			e := service.refreshExcel(ctx, account, "", original)
			stored, readErr := db.GetCredentialOpsProfileGrant(ctx, account.ID(), "excel")
			if readErr != nil {
				t.Fatal(readErr)
			}
			if scenario == "valid_without_new_id" || scenario == "valid_without_new_id_or_refresh" {
				expectedRefresh := "rotated-refresh"
				if scenario == "valid_without_new_id_or_refresh" {
					expectedRefresh = "original-single-use"
				}
				if e != nil || stored.Invalidated || stored.RefreshPending || stored.Credentials["id_token"] != oldID || stored.Credentials["refresh_token"] != expectedRefresh {
					t.Fatalf("valid merged refresh rejected: %#v %v", stored, e)
				}
				return
			}
			if e == nil || !stored.Invalidated || stored.Generation != original.Generation || stored.Credentials["access_token"] != oldAT {
				t.Fatalf("invalid %s refresh was published: %#v %v", scenario, stored, e)
			}
		})
	}
}
