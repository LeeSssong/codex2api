package admin

import (
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRetiredOperationsAreNotRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	(&Handler{}).RegisterRoutes(r)

	paths := make(map[string]bool)
	for _, route := range r.Routes() {
		paths[route.Method+" "+route.Path] = true
	}
	for _, retired := range []string{
		"GET /api/admin/plugins",
		"PUT /api/admin/plugins/:id",
		"GET /api/admin/smart-ops",
		"GET /api/admin/smart-ops/concurrency-progress",
		"GET /api/admin/credential-ops",
		"POST /api/admin/credential-ops/import",
		"GET /api/admin/account-ops/module",
		"GET /api/admin/account-ops/token-guard/config",
	} {
		if paths[retired] {
			t.Fatalf("retired route still registered: %s", retired)
		}
	}
	for _, retained := range []string{
		"GET /api/admin/accounts",
		"GET /api/admin/model-quality",
		"GET /api/admin/ipv6-egress",
		"GET /api/admin/quality-tests",
	} {
		if !paths[retained] {
			t.Fatalf("retained route missing: %s", retained)
		}
	}
}
