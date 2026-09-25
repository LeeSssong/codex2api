package proxy

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/codex2api/auth"
)

func TestBasispointsModelAllowedRespectsEnvAndDefaults(t *testing.T) {
	cases := []struct {
		env   string
		model string
		want  bool
	}{
		{"", "gpt-6-astra", true},
		{"", "gpt-5.6-sol", true},
		{"", "GPT-6-Astra", true},                  // case-insensitive
		{"", "gpt-6-astra-2026-01-15", true},       // dated snapshot via prefix
		{"", "gpt-5.5", false},                     // not served by default
		{"", "gpt-6-sol", false},                   // near-miss must not match gpt-6-astra
		{"", "", false},                            // empty model is never served
		{"*", "gpt-5.5", true},                     // wildcard keeps every model
		{"all", "anything", true},                  // "all" alias
		{"gpt-5.5,gpt-9-nova", "gpt-9-nova", true}, // explicit list
		{"gpt-5.5,gpt-9-nova", "gpt-6-astra", false},
		{"  ", "gpt-6-astra", true}, // blank falls back to defaults
	}
	for _, tc := range cases {
		t.Setenv(basispointsModelsEnv, tc.env)
		if got := basispointsModelAllowed(tc.model); got != tc.want {
			t.Errorf("basispointsModelAllowed(%q) with env %q = %v, want %v", tc.model, tc.env, got, tc.want)
		}
	}
}

// With the switch on, only allowlisted models reach Basispoints; every other
// model is served by the original Codex channel, and the routing-only web_search
// field ingress kept for the Basispoints decision is dropped before it leaves.
func TestBasispointsNonAllowlistedModelUsesNativeCodex(t *testing.T) {
	enableBasispointsForTest(t)
	settings := CurrentRuntimeSettings()
	settings.CodexForceWebsocket = false
	settings.CodexRequestCompression = false
	ApplyRuntimeSettings(settings)
	account := &auth.Account{DBID: 91280, AccountID: "workspace", AccessToken: "test-token"}

	var bpsCalls, nativeCalls int
	var nativeBodies []string
	installBasispointsTransport(t, account, func(*http.Request) (*http.Response, error) {
		bpsCalls++
		return basispointsTestCompleted("resp_bps", nil), nil
	})
	installClaudeBoundaryTransport(t, account, func(req *http.Request) (*http.Response, error) {
		nativeCalls++
		if req.URL.String() != CodexBaseURL+"/responses" || req.Header.Get("X-Basispoints-Auth-Mode") != "" {
			t.Fatalf("native route used the wrong upstream or identity: %s", req.URL)
		}
		body, _ := io.ReadAll(req.Body)
		nativeBodies = append(nativeBodies, string(body))
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\n"))}, nil
	})
	execute := func(body string) *http.Response {
		t.Helper()
		resp, err := ExecuteRequest(context.Background(), account, []byte(body), "", "", "client-key", nil, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp
	}

	// A non-allowlisted model goes native even with a plain (cacheable) web_search
	// declaration that would otherwise stay on Basispoints.
	prepared, _ := PrepareResponsesBody([]byte(`{"model":"gpt-5.5","input":"hi","tools":[{"type":"web_search","external_web_access":false,"search_context_size":"low"}]}`))
	resp := execute(string(prepared))
	if nativeCalls != 1 || bpsCalls != 0 {
		t.Fatalf("non-allowlisted model did not use native Codex: native=%d bps=%d", nativeCalls, bpsCalls)
	}
	if resp.Header.Get("X-Codex2API-Upstream") == "basispoints" {
		t.Fatalf("non-allowlisted model was labeled as a Basispoints response: %v", resp.Header)
	}
	sent := nativeBodies[len(nativeBodies)-1]
	if !strings.Contains(sent, `"web_search"`) || strings.Contains(sent, "external_web_access") {
		t.Fatalf("native request kept the routing-only web_search field: %s", sent)
	}

	// An allowlisted model still reaches Basispoints.
	if execute(`{"model":"gpt-6-astra","input":"hi"}`); bpsCalls != 1 || nativeCalls != 1 {
		t.Fatalf("allowlisted model did not reach Basispoints: native=%d bps=%d", nativeCalls, bpsCalls)
	}
}
