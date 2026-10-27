package admin

import (
	"context"
	"encoding/json"
	"github.com/codex2api/database"
	"github.com/codex2api/fixedegress"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fixedAdminTest(t *testing.T) (*Handler, *gin.Engine) {
	t.Helper()
	t.Setenv("CODEX_FIXED_EGRESS_KEY", "fixed-admin-key")
	db := newAdminProxyTestDB(t)
	s := newAdminProxyTestStore(t, db)
	h := &Handler{db: db, store: s, fixedEgress: fixedegress.NewManager(db, nil)}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h.registerFixedEgressRoutes(r.Group("/api/admin"))
	return h, r
}
func TestFixedEgressAdminSnapshotRedactionAndProviderBlankKey(t *testing.T) {
	h, r := fixedAdminTest(t)
	ctx := context.Background()
	if e := h.db.SaveFixedEgressPool(ctx, database.FixedEgressPool{ID: "pool", Name: "pool", Strategy: "stable"}); e != nil {
		t.Fatal(e)
	}
	h.db.SaveFixedEgressResource(ctx, database.FixedEgressResource{ID: "res", PoolID: "pool", ProxyURL: "http://secret-user:secret-pass@203.0.113.1:10443", State: "pending"})
	p := database.FixedEgressProvider{ID: "provider", Name: "provider", APIKey: "secret-api-key", BaseURL: "https://ipapi.cliproxy.com", Protocol: "http", AuthFormat: "3", PortStart: 10443, PortEnd: 10450, Capacity: 5, RefillIntervalSeconds: 30}
	h.db.SaveFixedEgressProvider(ctx, p)
	req := httptest.NewRequest("GET", "/api/admin/fixed-egress", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, secret := range []string{"secret-user", "secret-pass", "secret-api-key"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("secret leaked")
		}
	}
	var value map[string]any
	json.Unmarshal(w.Body.Bytes(), &value)
	for _, key := range []string{"pools", "providers", "resources", "bindings", "enrollments", "events", "tasks", "accounts"} {
		if _, ok := value[key].([]any); !ok {
			t.Fatal("missing array", key)
		}
	}
	body := `{"name":"edited","base_url":"https://ipapi.cliproxy.com","protocol":"http","auth_format":"3","api_key":"","port_start":10443,"port_end":10450,"capacity":5,"refill_interval_seconds":30}`
	req = httptest.NewRequest(http.MethodPut, "/api/admin/fixed-egress/providers/provider", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	got, e := h.db.GetFixedEgressProvider(ctx, "provider")
	if e != nil || got.APIKey != p.APIKey {
		t.Fatal("blank key lost", e)
	}
}
