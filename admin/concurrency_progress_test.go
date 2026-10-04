package admin

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/codex2api/auth"
	"github.com/gin-gonic/gin"
)

func TestSmartOpsConcurrencyProgressRejectsInvalidIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{db: newTestAdminDB(t), store: auth.NewStore(newTestAdminDB(t), nil, nil)}
	router := gin.New()
	group := router.Group("/api/admin")
	h.RegisterSmartOps(group)

	for _, ids := range []string{"0", "1,0", "1,1", "bogus"} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/admin/smart-ops/concurrency-progress?ids="+ids, nil))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("ids=%q status=%d body=%s", ids, recorder.Code, recorder.Body.String())
		}
	}
	tooMany := make([]string, maxSmartOpsConcurrencyProgressIDs+1)
	for index := range tooMany {
		tooMany[index] = strconv.Itoa(index + 1)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/admin/smart-ops/concurrency-progress?ids="+strings.Join(tooMany, ","), nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("too many IDs status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestSmartOpsConcurrencyProgressReturnsDisabledShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTestAdminDB(t)
	h := &Handler{db: db, store: auth.NewStore(db, nil, nil)}
	router := gin.New()
	group := router.Group("/api/admin")
	h.RegisterSmartOps(group)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/admin/smart-ops/concurrency-progress?ids=1", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	for _, part := range []string{`"enabled":false`, `"progress":{}`} {
		if !strings.Contains(recorder.Body.String(), part) {
			t.Fatalf("response missing %s: %s", part, recorder.Body.String())
		}
	}
}
