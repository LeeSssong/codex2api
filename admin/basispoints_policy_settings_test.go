package admin

import (
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
