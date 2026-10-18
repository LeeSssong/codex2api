package proxy

import (
	"context"
	"io"
	"net/http"
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
