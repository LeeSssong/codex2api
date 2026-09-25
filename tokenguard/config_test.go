package tokenguard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecretsPreservedByExplicitAccountID(t *testing.T) {
	old := DefaultConfig()
	old.BarkKey = "secret-bark"
	old.ProbeHeaders = map[string]string{"Authorization": "secret-header"}
	old.ReloginAccounts = []ReloginAccount{{AccountID: 4, Email: "a@example.com", Password: "secret-password", MFASecret: "secret-mfa"}}
	public := PublicConfig(old)
	if public.BarkKey != "********" || public.ReloginAccounts[0].Password != "********" {
		t.Fatal("secret exposed")
	}
	if old.ProbeHeaders["Authorization"] != "secret-header" {
		t.Fatal("mask mutated worker config")
	}
	next := DefaultConfig()
	next.ReloginAccounts = []ReloginAccount{{AccountID: 4, Email: "a@example.com"}}
	merged := RestoreSecrets(next, old)
	if merged.BarkKey != old.BarkKey || merged.ProbeHeaders["Authorization"] != "secret-header" || merged.ReloginAccounts[0].Password != "secret-password" {
		t.Fatal("blank/omitted secret erased existing value")
	}
	next.ReloginAccounts[0].AccountID = 5
	merged = RestoreSecrets(next, old)
	if merged.ReloginAccounts[0].Password != "" {
		t.Fatal("secret reused for unrelated account ID")
	}
	next = old
	next.ReloginAccounts = []ReloginAccount{{AccountID: 4, Email: "b@example.com"}}
	if RestoreSecrets(next, old).ReloginAccounts[0].Password != "" {
		t.Fatal("changed email reused old password")
	}
}
func TestProbeStructuredEvidenceAndSecretRedaction(t *testing.T) {
	for _, tt := range []struct {
		name, body, want string
		status           int
	}{
		{"service401", `{"type":"result","payload":{"error":{"code":"invalid_token"}}}`, "transient", 401},
		{"tokeninvalid", `{"type":"result","payload":{"error":{"code":"invalid_token","message":"SECRET"}}}`, "auth", 200},
		{"unknown", `{"type":"result","payload":{"error":{"message":"invalid token SECRET"}}}`, "transient", 200},
		{"healthy", `{"type":"result","payload":{"status":"active"}}`, "ok", 200},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tt.status); w.Write([]byte(tt.body)) }))
			defer srv.Close()
			cfg := DefaultConfig()
			cfg.ProbeEndpoint = srv.URL
			got := NewClient(nil).Probe(context.Background(), cfg, "SECRET")
			if got.State != tt.want || strings.Contains(got.Detail, "SECRET") {
				t.Fatalf("classification/redaction: %+v", got)
			}
		})
	}
}
func TestProbeRejectsRedirectOversizeAndMissingAccessToken(t *testing.T) {
	downstreamCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { downstreamCalls++; w.WriteHeader(200) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, target.URL, 307)
			return
		}
		w.Write([]byte(strings.Repeat("x", (2<<20)+1)))
	}))
	defer source.Close()
	cfg := DefaultConfig()
	client := NewClient(nil)
	for _, path := range []string{"/redirect", "/large"} {
		cfg.ProbeEndpoint = source.URL + path
		if got := client.Probe(context.Background(), cfg, "secret"); got.State != "transient" {
			t.Fatal("unsafe response accepted")
		}
	}
	if downstreamCalls != 0 {
		t.Fatal("redirect followed with credential payload")
	}
	if got := client.Probe(context.Background(), cfg, ""); got.State != "transient" {
		t.Fatal("missing access token must not count as external invalid-token evidence")
	}
}
func TestInvalidGroupScopeCannotNormalizeIntoAllAccounts(t *testing.T) {
	cfg := DefaultConfig()
	cfg.GroupIDs = []int64{-1}
	if err := ValidateConfig(Normalize(cfg)); err == nil {
		t.Fatal("invalid limited scope silently expanded into all accounts")
	}
}
