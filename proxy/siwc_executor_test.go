package proxy

import (
	"github.com/codex2api/auth"
	"github.com/codex2api/config"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSIWCStandaloneSelectionDoesNotRequireNativeOrBPSGrant(t *testing.T) {
	enableBasispointsForTest(t)
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 1, TestConcurrency: 1, TestModel: "gpt-6-astra", CodexBasispointsEnabled: true})
	t.Cleanup(store.Stop)
	h := NewHandler(store, nil, &config.Config{}, nil)
	account := &auth.Account{DBID: 1, SIWCEnabled: true}
	for _, explicit := range []bool{false, true} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		if explicit {
			c.Set(contextAPIKeyRow, &database.APIKeyRow{ID: 9, Limits: database.APIKeyLimits{CodexRoutePolicy: "basispoints_only"}})
		}
		f := h.withCodexRouteFilter(c, "gpt-6-astra", "gpt-6-astra", []byte(`{"input":"hello"}`), nil)
		if f(account) == explicit {
			t.Fatalf("SIWC/default or explicit BPS constraint mishandled: explicit=%v", explicit)
		}
	}
}
