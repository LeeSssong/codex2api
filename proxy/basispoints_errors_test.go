package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func TestBasispointsModelRejectionDoesNotRotateOrDisableAccounts(t *testing.T) {
	body := []byte(`{"error":{"code":"basispoints_model_access_changed","type":"invalid_request_error","message":"Model access has changed."}}`)
	general, rate := 0, 0
	policy := database.ContinuousRetryPolicy{Enabled: true, CatchAll: true}
	if shouldRetryHTTPStatus(403, body, &general, &rate, 4, 4, policy) || general != 0 || rate != 0 {
		t.Fatal("model access rejection must not rotate through the entire pool")
	}
	if got := classifyHTTPFailure(403, body); got != "" {
		t.Fatalf("model access rejection must not damage account health: %s", got)
	}
	handler := &Handler{}
	account := &auth.Account{DBID: 1, PlanType: "plus"}
	// A nil store makes any attempt to write an account cooldown fail this test.
	decision := handler.applyCooldownForModel(account, 403, body, nil, "gpt-6-sol")
	if decision.Reason != "" || decision.Cooldown != 0 || account.Disabled != 0 {
		t.Fatal("model access rejection was treated as an account or payment failure")
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	handler.sendFinalUpstreamError(ctx, 403, body)
	if recorder.Code != 403 || gjson.GetBytes(recorder.Body.Bytes(), "error.code").String() != "basispoints_model_access_changed" {
		t.Fatalf("model rejection was misreported as a pool outage: %s", recorder.Body.String())
	}
	if got := upstreamErrorKind(403, body, decision); got != "basispoints_model_access_changed" {
		t.Fatal("usage log lost the actionable error kind")
	}
}

func TestBasispointsProtocolFailureIsRequestScoped(t *testing.T) {
	for _, code := range []string{"basispoints_protocol_error", "basispoints_model_access_changed"} {
		body := []byte(`{"type":"response.failed","response":{"error":{"code":"` + code + `","message":"Rejected"},"status":"failed"}}`)
		outcome := classifyResponseFailedOutcome(body)
		if outcome.penalize || !outcome.requestScoped || outcome.failureKind != code {
			t.Fatalf("Basispoints request failure penalizes account: %+v", outcome)
		}
		policy := database.ContinuousRetryPolicy{Enabled: true, CatchAll: true}
		general, rate := 0, 0
		if shouldTransparentRetryStreamWithBudgets(outcome, &general, &rate, 4, 4, false, nil, nil, policy) {
			t.Fatal("a deterministic tool-format failure must not start an unlimited retry loop")
		}
	}
	other := []byte(`{"error":{"code":"codex_access_restricted","message":"basispoints_model_access_changed"}}`)
	general, rate := 0, 0
	if basispointsRequestErrorCode(other) != "" || !shouldRetryHTTPStatus(http.StatusForbidden, other, &general, &rate, 4, 4) {
		t.Fatal("ordinary Codex account failures must retain their existing recovery")
	}
}
