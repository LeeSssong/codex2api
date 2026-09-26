package admin

import (
	"encoding/json"
	"github.com/codex2api/proxy"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBasispointsSettingsAdmin(t *testing.T) {
	h, _, _ := newPagedAccountsHandler(t)
	old := proxy.CurrentRuntimeSettings()
	defer proxy.ApplyRuntimeSettings(old)
	for _, tc := range []struct {
		body string
		want int
	}{
		{"{\"enabled\":true,\"model_scope\":\"selected\",\"models\":[],\"image_relay_enabled\":true,\"image_relay_public_origin\":\"http://unsafe.test\"}", 400},
		{"{\"enabled\":true,\"model_scope\":\"selected\",\"models\":[],\"image_relay_enabled\":false}", 200},
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("PUT", "/", strings.NewReader(tc.body))
		c.Request.Header.Set("Content-Type", "application/json")
		h.UpdateBasispointsSettings(c)
		if w.Code != tc.want {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if !proxy.CurrentRuntimeSettings().CodexBasispointsEnabled {
		t.Fatal("runtime enabled not published")
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/", nil)
	h.GetBasispointsSettings(c)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "\"models\":[]") {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestBasispointsSettingsRuntimeDiagnostics(t *testing.T) {
	h, _, _ := newPagedAccountsHandler(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/", nil)
	h.GetBasispointsSettings(c)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	runtime, ok := response["image_relay_runtime"].(map[string]any)
	if !ok || runtime["validation_status"] != "disabled" || runtime["max_image_bytes"] != float64(20<<20) || runtime["request_body_limit_bytes"] != float64(48<<20) || runtime["source"] != "environment" {
		t.Fatalf("missing truthful diagnostics: %v", runtime)
	}
	usage, ok := response["image_relay_usage"].(map[string]any)
	if !ok || usage["bytes"] != float64(0) || usage["max_assets"] != float64(512) {
		t.Fatalf("missing durable quota statistics: %v", usage)
	}
}
