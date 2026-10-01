package statepool

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/codex2api/auth"
	"github.com/codex2api/security"
)

func TestProxySessionPreservesCredentialsAndOptions(t *testing.T) {
	raw := "http://customer-res-country-us-session-1664661000-sesstime-5:p%40ss%3Aword@gate.ipdeep.com:8082"
	rotated, err := proxyWithSession(raw, "1234567890")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := security.ParseProxyURL(rotated)
	password, _ := u.User.Password()
	if u.User.Username() != "customer-res-country-us-session-1234567890-sesstime-5" || password != "p@ss:word" || u.Host != "gate.ipdeep.com:8082" {
		t.Fatal("session replacement changed credentials, endpoint, or other options")
	}
	for _, invalid := range []string{
		"http://customer-session-123:password@other.example:8082",
		"http://customer:password@gate.ipdeep.com:8082",
		"http://customer-session-123:password@ipdeep.com.evil.example:8082",
	} {
		if SupportsSessionRotation(invalid) {
			t.Fatal("rewriting unknown proxy username is unsafe")
		}
	}
}

func TestDynamicCandidatesHaveIndependentSessionsAndSharedBudget(t *testing.T) {
	m, account := fixture(t, "member", false)
	account.ProxyURL = "http://business.local:8080"
	base := "http://customer-res-country-us-session-1664661000:password@gate.ipdeep.com:8082"
	proxyID := addProxy(t, m, base, "192.0.2.1")
	options := CaptureOptions{ProxyIDs: []int64{proxyID}, Candidates: 3, NewSession: true, DistinctIPs: true}
	if _, err := m.Capture(context.Background(), []int64{account.ID()}, []string{"sol", "terra"}, true, true, options); err != nil {
		t.Fatal(err)
	}
	jobs, _ := m.Jobs(context.Background())
	sessions := map[string]bool{}
	for _, job := range jobs {
		ref := job.CaptureProxy
		if len(ref.SessionID) != 10 || sessions[ref.SessionID] || ref.LastTestIP != "" {
			t.Fatal("candidate session duplicated or stale IP advertised")
		}
		sessions[ref.SessionID] = true
		resolved, effective, err := m.resolveCaptureProxy(context.Background(), ref, account.ProxyURL)
		if err != nil || !strings.Contains(resolved, "-session-"+ref.SessionID) || routeReservationKey(effective, resolved) != Hash(base) {
			t.Fatal("generated route bypassed base proxy budget")
		}
	}
	if len(sessions) != 6 {
		t.Fatal("multi-model capture did not create independent candidates")
	}
	businessRef, _ := m.businessProxyRef(context.Background(), base)
	if routeReservationKey(businessRef, base) != Hash(base) {
		t.Fatal("business and candidate sessions did not share gateway budget")
	}
	var calls int
	m.execute = func(_ context.Context, _ *auth.Account, body []byte, route string, headers http.Header) (*http.Response, error) {
		calls++
		model := Models[0]
		if strings.Contains(string(body), Models[1]) {
			model = Models[1]
		}
		if headers.Get(Header) != "" {
			if route != account.ProxyURL {
				t.Fatal("replay used dynamic capture egress")
			}
			return response(model, correctVerification(), "ignored"), nil
		}
		if route == base || !strings.Contains(route, "gate.ipdeep.com:8082") {
			t.Fatal("capture failed to use independent session")
		}
		return response(model, correctCandy, "captured-state"), nil
	}
	runOne(t, m)
	if calls != 2 || len(m.Entries()) != 1 || m.Entries()[0].Checks[0].SessionID == "" || m.Entries()[0].Checks[1].SessionID != "" {
		t.Fatal("capture and replay provenance missing")
	}
}
