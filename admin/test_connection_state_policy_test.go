package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/ipv6state"
	"github.com/codex2api/proxy"
	"github.com/gin-gonic/gin"
)

func TestStatePolicyTestFailureOnlyClassifiesLocalErrors(t *testing.T) {
	for _, code := range []string{"valid_state_required", "state_account_or_model_mismatch", "ipv6_state_identity_unavailable"} {
		err := fmt.Errorf("wrapped: %w", &proxy.Error{Code: code, Type: proxy.ErrorTypeServerError})
		message, blocked := statePolicyTestFailure(err, "gpt-5.6-sol")
		if !blocked || !strings.Contains(message, code) || !strings.Contains(message, "gpt-5.6-sol") {
			t.Fatalf("local gate lost its code or exact model: %q", message)
		}
	}
	for _, err := range []error{
		nil,
		errors.New("valid_state_required"),
		&proxy.Error{Code: "valid_state_required", Type: proxy.ErrorTypeUpstreamError},
		&proxy.Error{Code: "upstream_error", Type: proxy.ErrorTypeUpstreamError},
		&proxy.Error{Code: "internal_error", Type: proxy.ErrorTypeServerError},
	} {
		if _, blocked := statePolicyTestFailure(err, "gpt-5.6-sol"); blocked {
			t.Fatalf("non-policy error classified as a state gate: %v", err)
		}
	}
}

func TestStatePolicyTestDoesNotDisableAccountOrEraseCooldown(t *testing.T) {
	gin.SetMode(gin.TestMode)
	wham := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer wham.Close()
	t.Cleanup(proxy.SetWhamUsageURLForTest(wham.URL))
	proxy.SetIPv6StateProvider(&proxy.IPv6StateProvider{
		Resolve: func(*auth.Account, string) (string, bool, error) {
			return "", true, ipv6state.ErrStateRequired
		},
	})
	t.Cleanup(func() { proxy.SetIPv6StateProvider(nil) })

	for _, batch := range []bool{true, false} {
		for _, cooling := range []bool{false, true} {
			t.Run(fmt.Sprintf("batch=%v/cooling=%v", batch, cooling), func(t *testing.T) {
				store := auth.NewStore(nil, nil, nil)
				store.SetTestModel("gpt-5.6-sol")
				account := &auth.Account{
					DBID: 7061, AccessToken: "test-token", AccountID: "test-workspace",
					Status: auth.StatusReady, HealthTier: auth.HealthTierHealthy,
				}
				if cooling {
					account.Status = auth.StatusCooldown
					account.CooldownReason = "rate_limited"
					account.CooldownUtil = time.Now().Add(time.Hour)
					account.ErrorMsg = "existing quota evidence"
				}
				store.AddAccount(account)
				originalStatus, originalReason, originalUntil := account.Status, account.CooldownReason, account.CooldownUtil
				originalError := account.ErrorMsg
				handler := &Handler{store: store}
				var message string
				if batch {
					status, result := handler.runSingleBatchTest(context.Background(), account)
					if status != "failed" {
						t.Fatalf("blocked test reported %q", status)
					}
					message = result
				} else {
					router := gin.New()
					router.GET("/accounts/:id/test", handler.TestConnection)
					response := httptest.NewRecorder()
					router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/accounts/7061/test", nil))
					message = response.Body.String()
				}
				if !strings.Contains(message, "valid_state_required") || !strings.Contains(message, "本地 State 策略拦截") {
					t.Fatalf("missing actionable policy result: %q", message)
				}
				if account.Status != originalStatus || account.CooldownReason != originalReason || !account.CooldownUtil.Equal(originalUntil) || account.ErrorMsg != originalError || account.FailureStreak != 0 {
					t.Fatal("local rejection changed account health or quota evidence")
				}
				if !cooling {
					if available, reason, _ := account.StateAvailability("gpt-5.6-sol", time.Now()); !available {
						t.Fatalf("local rejection blocked subsequent state capture: %s", reason)
					}
					if !account.NeedsUsageProbe(time.Minute) {
						t.Fatal("local rejection blocked subsequent usage probes")
					}
				}
			})
		}
	}
}
