package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCodexCapabilityPageFiltersAndReset(t *testing.T) {
	h, ids, grok := newPagedAccountsHandler(t)
	ctx := context.Background()
	now := time.Now()
	for i, state := range []string{"supported", "unsupported"} {
		_, err := h.db.ObserveCodexCapability(ctx, ids[i], database.CodexCapability{Upstream: "basispoints", Model: "gpt-6-astra", Capability: state, Source: "test", ObservedAt: now.UnixNano()})
		if err != nil {
			t.Fatal(err)
		}
	}
	for filter, want := range map[string]int64{"bps_supported": ids[0], "bps_unsupported": ids[1], "bps_unknown": ids[2]} {
		r := invokeListAccounts(t, h, "/api/admin/accounts?view=page&channel=codex&page=2&page_size=1&capability_model=gpt-6-astra&capability="+filter)
		var page accountsPageResponse
		json.Unmarshal(r.Body.Bytes(), &page)
		if r.Code != 200 || page.Total != 1 || page.Summary.Total != 1 || page.Page != 1 || len(page.Accounts) != 1 || page.Accounts[0].ID != want {
			t.Fatalf("filter %s: %s", filter, r.Body.String())
		}
	}
	r := invokeListAccounts(t, h, "/api/admin/accounts?view=page&channel=codex&capability=bps_supported")
	if r.Code != 400 {
		t.Fatal("missing model accepted", r.Code)
	}
	r = invokeListAccounts(t, h, "/api/admin/accounts?view=page&channel=grok&capability=bps_unknown&capability_model=gpt-6-astra")
	var page accountsPageResponse
	json.Unmarshal(r.Body.Bytes(), &page)
	if page.Total != 0 {
		t.Fatal("noncodex labeled unknown")
	}
	update := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(r)
		c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		h.UpdateCodexRoutes(c)
		return r
	}
	r = update(fmt.Sprintf(`{"ids":[%d,%d],"upstream":"basispoints","allowed":false}`, ids[0], grok[0]))
	if r.Code != 400 {
		t.Fatal("mixed channel accepted")
	}
	paths, _, _ := h.db.GetCodexRoutes(ctx, ids[0])
	if len(paths) != 0 {
		t.Fatal("partially changed invalid batch")
	}
	r = update(fmt.Sprintf(`{"ids":[%d],"upstream":"basispoints","allowed":false,"reset_observations":true}`, ids[0]))
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	a := h.store.FindByID(ids[0])
	if a == nil {
		t.Fatal("runtime missing")
	}
	s := a.CodexPathSnapshot("basispoints", "gpt-6-astra", time.Now())
	if s.Allowed || s.Capability != "unknown" {
		t.Fatalf("runtime not refreshed: %+v", s)
	}
	r = invokeListAccounts(t, h, "/api/admin/accounts?view=page&channel=codex&capability=bps_supported&capability_model=gpt-6-astra")
	json.Unmarshal(r.Body.Bytes(), &page)
	if page.Total != 0 {
		t.Fatal("cached page retained old capability")
	}
}
func TestCodexRouteIDValidation(t *testing.T) {
	for _, raw := range []string{"1junk", "-1", "0", "1 2"} {
		if _, err := parseCodexRouteID(raw); err == nil {
			t.Fatal("invalid ID accepted", raw)
		}
	}
}

func TestBasispointsAccountPolicyAdminAndRuntime(t *testing.T) {
	h, ids, _ := newPagedAccountsHandler(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/", strings.NewReader(fmt.Sprintf("{\"ids\":[%d],\"upstream\":\"basispoints\",\"basispoints_policy\":{\"model_scope\":\"selected\",\"models\":[],\"auto_disable_on_403\":true,\"cache_creation_as_input\":true}}", ids[0])))
	c.Request.Header.Set("Content-Type", "application/json")
	h.UpdateCodexRoutes(c)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	a := h.store.FindByID(ids[0])
	p := a.BasispointsPolicySnapshot()
	if p.ModelScope != "selected" || !p.AutoDisableOn403 || !p.CacheCreationAsInput || p.Revision == 0 {
		t.Fatal(p)
	}
	global := database.BasispointsSettings{ModelScope: "all"}
	if p.AllowsModel("gpt-test", global) {
		t.Fatal("selected empty allows models")
	}
	ok, e := a.DisableBasispointsForHTTP403(context.Background(), a.GetCredentialGeneration(), p.Revision)
	if !ok || e != nil {
		t.Fatal(ok, e)
	}
	if a.CodexPathSnapshot("basispoints", "gpt-test", time.Now()).Allowed {
		t.Fatal("disable not visible immediately")
	}
	if !a.CodexPathSnapshot("codex", "gpt-test", time.Now()).Allowed {
		t.Fatal("native path changed")
	}
}
