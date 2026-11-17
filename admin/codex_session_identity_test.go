package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/codex2api/auth"
	"github.com/gin-gonic/gin"
)

func TestSingleMachineRejectsIdentityCustomHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTestAdminDB(t)
	accountID := insertTestAccount(t, db)
	handler := &Handler{db: db, store: auth.NewStore(db, nil, nil)}

	recorder := patchAccountScheduler(t, handler, accountID, `{"codex_fingerprint_mode":"single_machine_multi_window","custom_headers":{"X-Debug":"1"}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("functional header status = %d: %s", recorder.Code, recorder.Body.String())
	}
	row, err := db.GetAccountByID(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	if row.GetCredentialStringMap("custom_headers")["X-Debug"] != "1" {
		t.Fatalf("headers = %#v", row.GetCredentialStringMap("custom_headers"))
	}

	recorder = patchAccountScheduler(t, handler, accountID, `{"custom_headers":{"User-Agent":"custom","X-Debug":"1"}}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "User-Agent") {
		t.Fatalf("error = %s", recorder.Body.String())
	}
	row, err = db.GetAccountByID(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := row.GetCredentialStringMap("custom_headers")["User-Agent"]; ok {
		t.Fatal("rejected header was saved")
	}

	recorder = patchAccountScheduler(t, handler, accountID, `{"custom_headers":{"Session-Id":""}}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("empty identity header status = %d: %s", recorder.Code, recorder.Body.String())
	}

	pinned := insertTestAccount(t, db)
	recorder = patchAccountScheduler(t, handler, pinned, `{"custom_headers":{"User-Agent":"pinned"}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("store header status = %d: %s", recorder.Code, recorder.Body.String())
	}
	recorder = patchAccountScheduler(t, handler, pinned, `{"codex_fingerprint_mode":"single_machine_multi_window"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("stored header status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := accountFingerprintCredential(t, db, pinned); got != "" {
		t.Fatalf("mode changed despite conflict: %q", got)
	}
	recorder = patchAccountScheduler(t, handler, pinned, `{"codex_fingerprint_mode":"device"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("device mode status = %d: %s", recorder.Code, recorder.Body.String())
	}

	relayID, err := db.InsertAccountWithCredentials(context.Background(), "relay", map[string]interface{}{
		"upstream_type": auth.UpstreamOpenAIResponses,
		"base_url":      "https://relay.example.test",
		"api_key":       "relay-key",
		"custom_headers": map[string]string{
			"User-Agent": "relay-ua",
		},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	recorder = patchAccountScheduler(t, handler, relayID, `{"codex_fingerprint_mode":"single_machine_multi_window","custom_headers":{"User-Agent":"relay-ua"}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("relay status = %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestBatchSingleMachineRejectsIdentityCustomHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTestAdminDB(t)
	clean := insertTestAccount(t, db)
	conflict, err := db.InsertAccountWithCredentials(context.Background(), "conflict", map[string]interface{}{
		"refresh_token": "rt-conflict",
		"custom_headers": map[string]string{
			"X-Codex-Installation-Id": "pinned",
		},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	handler := &Handler{db: db, store: auth.NewStore(db, nil, nil)}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/admin/accounts/batch-update", strings.NewReader(fmt.Sprintf(`{"ids":[%d,%d],"codex_fingerprint_mode":"single_machine_multi_window"}`, conflict, clean)))
	ctx.Request.Header.Set("Content-Type", "application/json")
	handler.BatchUpdateAccounts(ctx)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "X-Codex-Installation-Id") {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if got := accountFingerprintCredential(t, db, clean); got != "" {
		t.Fatalf("clean account mode = %q", got)
	}
	if got := accountFingerprintCredential(t, db, conflict); got != "" {
		t.Fatalf("conflict account mode = %q", got)
	}
}

func TestMigrateCodexSessionIdentityUpdatesOnlyOffOfficialAccounts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTestAdminDB(t)
	store := auth.NewStore(db, nil, nil)
	handler := &Handler{db: db, store: store}
	off := insertTestAccount(t, db)
	explicitOff, err := db.InsertAccountWithCredentials(context.Background(), "explicit-off", map[string]interface{}{
		"refresh_token":                        "rt-off",
		auth.CodexFingerprintModeCredentialKey: auth.CodexFingerprintModeOff,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	device, err := db.InsertAccountWithCredentials(context.Background(), "device", map[string]interface{}{
		"refresh_token":                        "rt-device",
		auth.CodexFingerprintModeCredentialKey: auth.CodexFingerprintModeDevice,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	conflict, err := db.InsertAccountWithCredentials(context.Background(), "conflict", map[string]interface{}{
		"refresh_token": "rt-conflict",
		"custom_headers": map[string]string{
			"User-Agent": "custom",
		},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	grok, err := db.InsertAccountWithCredentials(context.Background(), "grok", map[string]interface{}{
		"upstream_type": auth.UpstreamGrok,
		"refresh_token": "rt-grok",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	relay, err := db.InsertAccountWithCredentials(context.Background(), "relay", map[string]interface{}{
		"upstream_type": auth.UpstreamOpenAIResponses,
		"base_url":      "https://relay.example.test",
		"api_key":       "relay-key",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{off, explicitOff, device, conflict, grok, relay} {
		if err := store.LoadAccountByID(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}

	call := func() map[string]interface{} {
		t.Helper()
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/api/admin/settings/codex-fingerprint/migrate-session-identity", nil)
		handler.MigrateCodexSessionIdentity(ctx)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
		}
		var payload map[string]interface{}
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}
	first := call()
	if first["updated"] != float64(2) || first["skipped_conflict"] != float64(1) {
		t.Fatalf("first migrate = %#v", first)
	}
	for _, id := range []int64{off, explicitOff} {
		if got := accountFingerprintCredential(t, db, id); got != auth.CodexFingerprintModeSingleMachineMultiWindow {
			t.Fatalf("account %d mode = %q", id, got)
		}
		if store.FindByID(id).EffectiveCodexFingerprintMode() != auth.CodexFingerprintModeSingleMachineMultiWindow {
			t.Fatalf("account %d runtime mode not applied", id)
		}
	}
	if got := accountFingerprintCredential(t, db, device); got != auth.CodexFingerprintModeDevice {
		t.Fatalf("device mode = %q", got)
	}
	if got := accountFingerprintCredential(t, db, conflict); got != "" {
		t.Fatalf("conflict mode = %q", got)
	}
	if got := accountFingerprintCredential(t, db, grok); got != "" {
		t.Fatalf("grok mode = %q", got)
	}
	if got := accountFingerprintCredential(t, db, relay); got != "" {
		t.Fatalf("relay mode = %q", got)
	}
	second := call()
	if second["updated"] != float64(0) || second["skipped_conflict"] != float64(1) {
		t.Fatalf("second migrate = %#v", second)
	}
}
