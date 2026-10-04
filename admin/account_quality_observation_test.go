package admin

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/gin-gonic/gin"
)

func TestAccountQualityObservationsNeverChangeSchedulingControls(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, status := range []int{200, 401, 402, 403, 429} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			store := auth.NewStore(nil, nil, nil)
			defer store.Stop()
			a := newAntigravityConnectionTestAccount()
			a.Status = auth.StatusCooldown
			a.CooldownUtil = time.Now().Add(time.Hour)
			a.CooldownReason = "manual"
			atomic.StoreInt32(&a.Disabled, 1)
			store.AddAccount(a)
			h := &Handler{store: store}
			h.antigravityCapabilityProbe = func(context.Context, *auth.Account, string, []byte, bool, string) (*http.Response, error) {
				body := `{"error":{"message":"synthetic auth failure"}}`
				if status == 200 {
					body = antigravityTestSSEBody("ok")
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			}
			before := a.CooldownUtil
			router := gin.New()
			router.POST("/accounts/:id/test", func(c *gin.Context) {
				h.testConnection(c, &qualityTestRequest{Model: a.Models[0], Prompt: "q", TextOnly: true, ObservationOnly: true})
			})
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/accounts/7/test", nil))
			if !strings.Contains(w.Body.String(), `"type":"test_start"`) {
				t.Fatalf("observation never ran: %s", w.Body.String())
			}
			if a.Status != auth.StatusCooldown || a.CooldownReason != "manual" || !a.CooldownUtil.Equal(before) || atomic.LoadInt32(&a.Disabled) != 1 {
				t.Fatalf("quality observation changed account controls on status %d", status)
			}
		})
	}
}
