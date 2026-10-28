package auth

import "testing"

func TestIPv6AccountRouteSelectionPreservesLegacyAndExplicitDirect(t *testing.T) {
	s := &Store{globalProxy: "http://global:8080", proxyPoolEnabled: true, proxyPool: []string{"http://pool:8080"}}
	s.SetGroupProxyURLs(7, []string{"http://group:8080"})
	legacy := &Account{DBID: 1, GroupIDs: []int64{7}}
	if got := s.ResolveProxyForAccount(legacy); got != "http://group:8080" {
		t.Fatalf("legacy changed: %q", got)
	}
	for _, enabled := range []bool{false, true} {
		a := &Account{DBID: 1, GroupIDs: []int64{7}, IPv6EgressConfigured: true, IPv6EgressEnabled: enabled}
		if got := s.ResolveProxyForAccount(a); got != "" {
			t.Fatalf("explicit selection inherited: %q", got)
		}
	}
	explicit := &Account{DBID: 1, IPv6EgressConfigured: true, ProxyURL: "http://account:8080"}
	if got := s.ResolveProxyForAccount(explicit); got != explicit.ProxyURL {
		t.Fatal(got)
	}
}
