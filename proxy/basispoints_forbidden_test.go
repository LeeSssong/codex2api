package proxy

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/tidwall/gjson"
)

func TestBasispointsHTTP403IsTerminalWithoutAccountPenalty(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"json", "{\"error\":{\"message\":\"forbidden secret-image-url\",\"type\":\"server_error\"}}"},
		{"waf", "<html>secret-image-url forbidden</html>"},
		{"oversized", strings.Repeat("x", codexRoutePrefixLimit+1) + "secret-image-url"},
		{"path denial", "{\"error\":{\"code\":\"codex_access_restricted\",\"message\":\"denied\"}}"},
		{"ambiguous usage", "{\"error\":{\"message\":\"403: This request was blocked by our usage policy.\",\"type\":\"server_error\",\"code\":null}}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enableBasispointsForTest(t)
			a := &auth.Account{DBID: 993851, AccessToken: "token", AccountID: "workspace"}
			bpsCalls, nativeCalls := 0, 0
			installBasispointsTransport(t, a, func(*http.Request) (*http.Response, error) {
				bpsCalls++
				r := routeTestResponse(403, tc.body)
				r.Header.Set("X-Request-Id", "trusted-request-id")
				return r, nil
			})
			ctx := context.Background()
			d := newCodexRouteDecision(ctx, "gpt-6-astra", "gpt-6-astra", database.APIKeyLimits{CodexRoutePolicy: database.CodexRouteBasispointsPrefer}, 4)
			ctx = context.WithValue(ctx, codexRouteKey{}, d)
			native := func(context.Context, *auth.Account, []byte, string, string, string, *DeviceProfileConfig, http.Header) (*http.Response, error) {
				nativeCalls++
				return routeTestResponse(200, ""), nil
			}
			resp, err := executeCodexRoute(ctx, a, []byte("{\"model\":\"gpt-6-astra\",\"input\":\"hello\"}"), "", "", "key", nil, nil, false, native)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != 403 || gjson.GetBytes(body, "error.code").String() != "basispoints_upstream_error" || strings.Contains(string(body), "secret-image-url") {
				t.Fatalf("unsafe response: %d %.250s", resp.StatusCode, body)
			}
			if resp.Header.Get("X-Request-Id") != "trusted-request-id" {
				t.Fatal("lost request ID")
			}
			if bpsCalls != 1 || nativeCalls != 0 || !d.NoSwitch || d.Switched {
				t.Fatalf("403 replayed: bps=%d native=%d decision=%+v", bpsCalls, nativeCalls, d)
			}
			general, rate := 0, 0
			if shouldRetryHTTPStatus(403, body, &general, &rate, 4, 4, database.ContinuousRetryPolicy{Enabled: true, CatchAll: true}) || general != 0 || rate != 0 {
				t.Fatal("outer retry can replay 403")
			}
			if classifyHTTPFailure(403, body) != "" {
				t.Fatal("403 penalized account health")
			}
			decision := (&Handler{}).applyCooldownForModel(a, 403, body, resp, "gpt-6-astra")
			if decision.Reason != "" || decision.Cooldown != 0 || a.Disabled != 0 {
				t.Fatal("403 caused account cooldown")
			}
			if !a.CodexPathSnapshot(database.CodexPathNative, "gpt-6-astra", time.Now()).Allowed {
				t.Fatal("native path disabled")
			}
			ws := responsesWSUpstreamAPIError(403, body)
			if string(ws.Code) != "basispoints_upstream_error" {
				t.Fatalf("WS lost request-scoped code: %+v", ws)
			}
		})
	}
}

func TestBasispointsHTTP403PreservesStructuredIndependentFailures(t *testing.T) {
	for _, code := range []string{"basispoints_model_access_changed", "invalid_api_key", "content_policy_violation", "rate_limit_exceeded", "insufficient_quota"} {
		t.Run(code, func(t *testing.T) {
			enableBasispointsForTest(t)
			a := &auth.Account{DBID: 993852, AccessToken: "token", AccountID: "workspace"}
			raw := "{\"error\":{\"code\":\"" + code + "\",\"message\":\"rejected\"}}"
			installBasispointsTransport(t, a, func(*http.Request) (*http.Response, error) { return routeTestResponse(403, raw), nil })
			resp, err := executeBasispointsRequest(context.Background(), a, []byte("{\"model\":\"gpt-6-astra\",\"input\":\"hello\"}"), "", "", "key", nil)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if gjson.GetBytes(body, "error.code").String() != code {
				t.Fatalf("changed %s: %s", code, body)
			}
		})
	}
}
