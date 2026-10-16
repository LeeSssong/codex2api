package proxy

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/codex2api/config"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/codex2api/accountops"
	"github.com/codex2api/auth"
	"github.com/codex2api/database"
)

func TestQualityBPSPolicyChangesRealTransportAndPreservesKeyConstraints(t *testing.T) {
	settings := CurrentRuntimeSettings()
	t.Cleanup(func() { ApplyRuntimeSettings(settings) })
	next := settings
	next.CodexBasispointsEnabled = false
	next.CodexForceWebsocket = false
	next.CodexRequestCompression = false
	ApplyRuntimeSettings(next)
	ctx := context.Background()
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "quality-bps.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	id, err := db.InsertAccountWithCredentials(ctx, "quality-transport", map[string]any{"refresh_token": "test-rt", "access_token": "test-at", "account_id": "test-workspace", "plan_type": "pro"}, "")
	if err != nil {
		t.Fatal(err)
	}
	policy, _ := json.Marshal(accountops.QualityBPSPolicy{Models: []string{"gpt-6-astra"}, FailureThreshold: 1})
	if err := db.WithAccountControlTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO account_codex_paths(account_id,upstream,allowed,quality_bps) VALUES($1,'basispoints',1,$2)`, id, string(policy))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	store := auth.NewStore(db, nil, &database.SystemSettings{MaxConcurrency: 2})
	t.Cleanup(store.Stop)
	if err := store.LoadAccountByID(ctx, id); err != nil {
		t.Fatal(err)
	}
	account := store.FindByID(id)
	if account == nil {
		t.Fatal("fixture account unavailable")
	}

	oldProvider := ipv6StateProvider.Load()
	t.Cleanup(func() { SetIPv6StateProvider(oldProvider) })
	SetIPv6StateProvider(&IPv6StateProvider{EligibleAccounts: func(string) (bool, map[int64]bool) { return true, map[int64]bool{} }})
	h := NewHandler(store, db, &config.Config{}, nil)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	filter := h.withCodexRouteFilter(c, "gpt-6-astra", "gpt-6-astra", nil, nil)
	filter = h.withModelCooldownFilter(c.Request.Context(), "gpt-6-astra", filter)
	if !filter(account) {
		t.Fatalf("qualityBPS=%v paths=%v snapshot=%+v", account.QualityBPSPreferred("gpt-6-astra"), codexRouteFromContext(c.Request.Context()).eligibleAccountPaths(account, "gpt-6-astra"), account.CodexPathSnapshot("basispoints", "gpt-6-astra", time.Now()))
	}
	SetIPv6StateProvider(oldProvider)
	var bpsCalls, nativeCalls int
	installBasispointsTransport(t, account, func(*http.Request) (*http.Response, error) {
		bpsCalls++
		return basispointsTestCompleted("resp-quality", nil), nil
	})
	installClaudeBoundaryTransport(t, account, func(*http.Request) (*http.Response, error) {
		nativeCalls++
		return basispointsTestCompleted("resp-native", nil), nil
	})

	run := func(ctx context.Context, model string) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"model": model, "input": "test", "stream": true})
		resp, err := ExecuteRequest(ctx, account, body, "", "", "", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	run(ctx, "gpt-6-astra")
	if bpsCalls != 1 || nativeCalls != 0 {
		t.Fatalf("quality BPS not used: bps=%d native=%d", bpsCalls, nativeCalls)
	}
	run(ctx, "gpt-5.6-sol")
	if bpsCalls != 1 || nativeCalls != 1 {
		t.Fatalf("model scope escaped: bps=%d native=%d", bpsCalls, nativeCalls)
	}
	run(WithNativeQualityProbe(ctx), "gpt-6-astra")
	if bpsCalls != 1 || nativeCalls != 2 {
		t.Fatal("direct quality recovery probe entered BPS")
	}
	if err := db.SetCodexPathAllowed(ctx, id, "basispoints", false); err != nil {
		t.Fatal(err)
	}
	if err := account.ReloadCodexRoutes(ctx); err != nil {
		t.Fatal(err)
	}
	run(ctx, "gpt-6-astra")
	if bpsCalls != 1 || nativeCalls != 3 {
		t.Fatal("manually disabled BPS path was used")
	}
}
