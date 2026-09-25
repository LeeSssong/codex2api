package proxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/codex2api/api"
	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
)

func TestExplicitQuotaExhaustionIsAccountLimitedAcrossTransports(t *testing.T) {
	for _, code := range []string{"usage_limit_reached", "insufficient_quota", "quota_exhausted", "billing_hard_limit_reached"} {
		for _, status := range []int{400, 403, 429, 500, 200} {
			t.Run(fmt.Sprintf("%s/%d", code, status), func(t *testing.T) {
				store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, IgnoreUsageLimitStatus: true})
				t.Cleanup(store.Stop)
				a := &auth.Account{DBID: 98710, AccessToken: "token", PlanType: "plus", Status: auth.StatusReady}
				store.AddAccount(a)
				h := &Handler{store: store}
				body := []byte(fmt.Sprintf(`{"error":{"code":%q,"resets_in_seconds":1800}}`, code))
				var decision codex429Decision
				if status == 200 {
					payload := []byte(`{"type":"response.failed","response":` + string(body) + `}`)
					if got := classifyResponseFailedOutcome(payload).logStatusCode; got != 429 {
						t.Fatalf("stream status = %d", got)
					}
					decision = h.applyResponseFailedCooldown(a, payload, &http.Response{StatusCode: 200, Header: http.Header{}}, "gpt-6-astra")
				} else {
					decision = h.applyCooldownForModel(a, status, body, nil, "gpt-6-astra")
				}
				if decision.Scope != rateLimitScopeAccount || decision.Cooldown != 30*time.Minute {
					t.Fatalf("decision = %+v", decision)
				}
				if a.RuntimeStatus() != auth.ResponsesRateLimitedCooldownReason || a.IsAvailable() || !a.FreshDispatchUsageLimited() {
					t.Fatalf("quota account still dispatchable: %s", a.RuntimeStatus())
				}
				if got := store.Next(); got != nil {
					store.Release(got)
					t.Fatal("fresh scheduler selected exhausted account")
				}
				store.BindSessionAffinity("turn", a, "")
				if got, _ := store.NextForContinuationWithFilter("turn", 0, nil, nil); got != nil {
					store.Release(got)
					t.Fatal("continuation bypassed explicit quota")
				}
				r := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(r)
				h.sendFinalUpstreamError(c, status, body)
				if r.Code != 429 || !strings.Contains(r.Body.String(), "account_pool_usage_limit_reached") || r.Header().Get("Retry-After") != "1800" {
					t.Fatalf("final quota response = %d %s", r.Code, r.Body.String())
				}
				for _, hidden := range []bool{false, true} {
					wsErr := responsesWSClientUpstreamAPIError(responsesWSUpstreamAPIError(status, body), hidden)
					if wsErr.Code != api.ErrorCode(ErrorCodeAccountPoolUsageLimit) || wsErr.Type != api.ErrorTypeRateLimit {
						t.Fatalf("WS quota hidden or misclassified: %+v", wsErr)
					}
				}
			})
		}
	}
}

func TestQuotaClassificationRequiresExactStructuredEvidence(t *testing.T) {
	for _, body := range []string{
		`{"error":{"type":"server_error","code":null,"message":"403: This request was blocked by our usage policy."}}`,
		`{"error":{"code":"rate_limit_exceeded"}}`,
		`{"error":{"code":"prefix_insufficient_quota_suffix"}}`,
		`{"error":{"message":"Documentation mentions insufficient_quota"}}`,
		`{"input":[{"type":"message","content":"insufficient_quota"}]}`,
		`<html>insufficient_quota</html>`,
	} {
		if IsUsageLimitReachedError([]byte(body)) {
			t.Fatalf("false quota evidence: %s", body)
		}
	}
	decision := classify429RateLimit(&auth.Account{PlanType: "free"}, []byte(`{"error":{"code":"insufficient_quota"}}`), nil, time.Now(), "gpt-6-astra")
	if decision.Cooldown != 30*time.Minute {
		t.Fatalf("invented a subscription reset: %+v", decision)
	}
}

func TestBillingQuotaOnSparkBlocksAccountWhileSparkWindowStaysLocal(t *testing.T) {
	for _, code := range []string{"usage_limit_reached", "insufficient_quota"} {
		d := classify429RateLimit(&auth.Account{PlanType: "pro"}, []byte(fmt.Sprintf(`{"error":{"code":%q,"resets_in_seconds":300}}`, code)), nil, time.Now(), "gpt-5.3-codex-spark")
		want := rateLimitScopeAccount
		if code == "usage_limit_reached" {
			want = rateLimitScopeModel
		}
		if d.Scope != want {
			t.Fatalf("%s scope=%s want=%s", code, d.Scope, want)
		}
	}
}

func TestGenericQuotaDoesNotFabricateSubscriptionUsage(t *testing.T) {
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1})
	t.Cleanup(store.Stop)
	a := &auth.Account{DBID: 98731, AccessToken: "token", PlanType: "plus", Status: auth.StatusReady}
	store.AddAccount(a)
	resp := &http.Response{Header: make(http.Header)}
	resp.Header.Set("X-Codex-Primary-Used-Percent", "12")
	resp.Header.Set("X-Codex-Primary-Window-Minutes", "300")
	body := []byte(`{"error":{"code":"insufficient_quota","plan_type":"free","resets_in_seconds":300}}`)
	d := Apply429Cooldown(store, a, body, resp, "gpt-6-astra")
	if d.Reason != "usage_limit" || a.GetPlanType() != "plus" {
		t.Fatalf("invented plan/window metadata: %+v plan=%s", d, a.GetPlanType())
	}
	if _, valid := a.GetUsagePercent5h(); valid {
		t.Fatal("billing rejection fabricated 5h percentage")
	}
	if _, valid := a.GetUsagePercent7d(); valid {
		t.Fatal("billing rejection fabricated 7d percentage")
	}
	resp.Header.Set("Retry-After", "90")
	d = classify429RateLimit(a, []byte(`{"error":{"code":"insufficient_quota"}}`), resp, time.Now(), "gpt-6-astra")
	if d.Cooldown != 90*time.Second {
		t.Fatalf("quota Retry-After ignored: %+v", d)
	}
}
