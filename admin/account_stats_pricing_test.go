package admin

import (
	"context"
	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestAccountStatsPricingAPI(t *testing.T) {
	db := newTestAdminDB(t)
	id, e := db.InsertAccountWithCredentials(context.Background(), "stats", map[string]any{"refresh_token": "stats-api-rt"}, "")
	if e != nil {
		t.Fatal(e)
	}
	s := auth.NewStore(db, nil, &database.SystemSettings{MaxConcurrency: 1, TestConcurrency: 1})
	defer s.Stop()
	if e := s.LoadAccountByID(context.Background(), id); e != nil {
		t.Fatal(e)
	}
	h := &Handler{db: db, store: s}
	r := gin.New()
	r.PUT("/accounts/:id/stats-pricing", h.UpdateAccountStatsPricing)
	r.GET("/accounts/:id/stats-pricing", h.GetAccountStatsPricing)
	for _, tc := range []struct {
		body   string
		status int
	}{{`{"account_rate_multiplier":0,"apply_sale_pricing":true}`, 200}, {`{"account_rate_multiplier":-1}`, 400}, {`{"unknown":1}`, 400}} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/accounts/"+strconv.FormatInt(id, 10)+"/stats-pricing", strings.NewReader(tc.body)))
		if w.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.body, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/accounts/"+strconv.FormatInt(id, 10)+"/stats-pricing", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"account_rate_multiplier":0`) {
		t.Fatalf("GET %d %s", w.Code, w.Body.String())
	}
}
