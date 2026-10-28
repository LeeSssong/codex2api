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
	"github.com/codex2api/database"
	"github.com/codex2api/egressipv6"
	"github.com/gin-gonic/gin"
)

func TestAccountIPv6ManualSelectionAPI(t *testing.T) {
	h, _, db := newImportEndToEndHandler(t)
	ctx := context.Background()
	id, err := db.InsertAccountWithCredentials(ctx, "manual", map[string]interface{}{auth.IPv6EgressCredentialKey: true, "timezone": "Asia/Tokyo"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.BatchUpdateAccountMetadata(ctx, []int64{id}, database.BatchAccountMetadataUpdate{Enabled: database.OptionalBool{Set: true, Value: false}}); err != nil {
		t.Fatal(err)
	}
	cfg, err := db.IPv6EgressConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Enabled = true
	cfg.SourceIPs = []string{"2604::1", "2604::2"}
	if err = db.SaveIPv6EgressConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	manager := egressipv6.New(db)
	manager.LocalIPs = func() ([]string, error) { return []string{"2604::1", "2604::2", "2604::3"}, nil }
	previous := egressipv6.Current()
	egressipv6.Install(manager)
	t.Cleanup(func() { egressipv6.Install(previous) })
	h.adminSecretEnv = "manual-ipv6-test"
	router := gin.New()
	h.RegisterRoutes(router)
	post := func(body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/admin/accounts/%d/ipv6-egress", id), strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Admin-Key", h.adminSecretEnv)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s: got %d want %d: %s", body, w.Code, want, w.Body.String())
		}
		return w
	}
	for _, body := range []string{`{"ip":"2604::1"}`, `{"ip":"","expected_ip":""}`, `{"ip":"2604::1","expected_ip":"2604:0::2"}`, `{"ip":"2604:0::1","expected_ip":""}`, `{"ip":"127.0.0.1","expected_ip":""}`, `{"ip":"2604::9","expected_ip":""}`, `{"ip":"2604::3","expected_ip":""}`} {
		post(body, http.StatusBadRequest)
	}
	w := post(`{"ip":"2604::1","expected_ip":""}`, http.StatusOK)
	var result struct {
		IP string `json:"ip"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.IP != "2604::1" {
		t.Fatalf("response: %s %v", w.Body.String(), err)
	}
	row, err := db.GetAccountByID(ctx, id)
	if err != nil || row.Enabled || row.GetCredential("timezone") != "Asia/Tokyo" || h.accountIPv6IP(row) != result.IP {
		t.Fatalf("selection changed preferences or details missing actual IP: %+v %v", row, err)
	}
	post(`{"ip":"2604::2","expected_ip":""}`, http.StatusConflict)
	post(`{"ip":"2604::1","expected_ip":"2604::1"}`, http.StatusOK)
	post(`{"ip":"2604::2","expected_ip":"2604::1"}`, http.StatusOK)
	if err = db.UpdateCredentials(ctx, id, map[string]interface{}{auth.IPv6EgressCredentialKey: false}); err != nil {
		t.Fatal(err)
	}
	post(`{"ip":"2604::1","expected_ip":"2604::2"}`, http.StatusConflict)
	if err = db.UpdateCredentials(ctx, id, map[string]interface{}{auth.IPv6EgressCredentialKey: true}); err != nil {
		t.Fatal(err)
	}
	cfg, err = db.IPv6EgressConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Enabled = false
	if err = db.SaveIPv6EgressConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	post(`{"ip":"2604::2","expected_ip":"2604::2"}`, http.StatusServiceUnavailable)
}
