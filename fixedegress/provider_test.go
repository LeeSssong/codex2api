package fixedegress

import (
	"testing"
)

func TestParseProxyURLSupportsEscapedCredentialsAndIPv6(t *testing.T) {
	got, err := ParseProxyURL("http://u%40ser:p%3Ass@[2001:db8::7]:8443")
	if err != nil {
		t.Fatal(err)
	}
	if got.Host != "[2001:db8::7]:8443" || got.Username != "u@ser" || got.Password != "p:ss" {
		t.Fatalf("got %#v", got)
	}
}

func TestParseStaticProxyLineSupportsLegacyTypes(t *testing.T) {
	for _, tc := range []struct{ raw, wantUser, wantPass, wantHost string }{
		{"host.example:8080", "", "", "host.example:8080"},
		{"host.example:8080:user:pass", "user", "pass", "host.example:8080"},
		{"user:pass@host.example:8080", "user", "pass", "host.example:8080"},
	} {
		got, err := ParseStaticProxy(tc.raw)
		if err != nil {
			t.Fatalf("%s: %v", tc.raw, err)
		}
		if got.Host != tc.wantHost || got.Username != tc.wantUser || got.Password != tc.wantPass {
			t.Fatalf("%s: %#v", tc.raw, got)
		}
	}
}
func TestParseProxyRejectsMissingOrInvalidPort(t *testing.T) {
	for _, raw := range []string{"http://host", "http://host:0", "http://host:99999", "http://host:abc", "http://host:443/path"} {
		if _, e := ParseProxyURL(raw); e == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
