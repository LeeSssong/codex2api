package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/codex2api/database"
	"github.com/codex2api/tokenguard"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestTokenGuardHTTPRequiresAdminAndMasksConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, e := database.New("sqlite", filepath.Join(t.TempDir(), "http.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	h := &Handler{db: db, adminSecretEnv: "guard-admin-secret"}
	h.tokenGuard = tokenguard.NewService(db, nil, nil)
	r := gin.New()
	g := r.Group("/api/admin")
	g.Use(h.adminAuthMiddleware())
	h.RegisterTokenGuardRoutes(g)
	for _, path := range []string{"/status", "/config", "/events"} {
		req := httptest.NewRequest("GET", "/api/admin/account-ops/token-guard"+path, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s: %d", path, w.Code)
		}
	}
	cfg := tokenguard.DefaultConfig()
	cfg.BarkKey = "SECRET-BARK"
	cfg.ProbeHeaders = map[string]string{"Authorization": "SECRET-HEADER"}
	if _, e := h.tokenGuard.SaveConfig(context.Background(), cfg); e != nil {
		t.Fatal(e)
	}
	req := httptest.NewRequest("GET", "/api/admin/account-ops/token-guard/config", nil)
	req.Header.Set("Authorization", "Bearer guard-admin-secret")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 || strings.Contains(w.Body.String(), "SECRET-") || !strings.Contains(w.Body.String(), "********") {
		t.Fatalf("config secret exposure/status %d", w.Code)
	}
	raw, _ := json.Marshal(cfg)
	req = httptest.NewRequest("PUT", "/api/admin/account-ops/token-guard/config", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer guard-admin-secret")
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 || strings.Contains(w.Body.String(), "SECRET-") {
		t.Fatalf("save exposure/status %d", w.Code)
	}
	req = httptest.NewRequest("POST", "/api/admin/account-ops/token-guard/run", nil)
	req.Header.Set("Authorization", "Bearer guard-admin-secret")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 400 && w.Code != 409 {
		t.Fatalf("invalid/disabled run admitted: %d", w.Code)
	}
}
