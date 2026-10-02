package proxy

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"testing"

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
