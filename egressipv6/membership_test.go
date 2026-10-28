package egressipv6

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAccountLeaseRetirementDrainsAcrossManagers(t *testing.T) {
	m, id := httpFixture(t)
	ctx := context.Background()
	if err := m.DB.UpdateCredentials(ctx, id, map[string]interface{}{"ipv6_egress_enabled": true}); err != nil {
		t.Fatal(err)
	}
	route, release, err := m.AcquireAccount(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	other := New(m.DB)
	other.LocalIPs = m.LocalIPs
	if err = m.DB.UpdateCredentials(ctx, id, map[string]interface{}{"ipv6_egress_enabled": false}); err != nil {
		t.Fatal(err)
	}
	if err = other.RetireAccount(ctx, id); err != nil {
		t.Fatal(err)
	}
	bindings, cooldowns, err := m.DB.IPv6EgressStatus(ctx, time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 1 || len(cooldowns) != 0 {
		t.Fatalf("retired before drain: %+v %+v", bindings, cooldowns)
	}
	if _, _, err = other.AcquireAccount(ctx, id); err == nil {
		t.Fatal("opted out account acquired lease")
	}
	release()
	release()
	bindings, cooldowns, err = m.DB.IPv6EgressStatus(ctx, time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 0 || len(cooldowns) != 1 || cooldowns[0].IP != route.Binding.IP {
		t.Fatalf("drain/cooldown failed: %+v %+v", bindings, cooldowns)
	}
}
func TestAccountLeasePoolDisabledFailsClosed(t *testing.T) {
	m, id := httpFixture(t)
	ctx := context.Background()
	m.DB.UpdateCredentials(ctx, id, map[string]interface{}{"ipv6_egress_enabled": true})
	c, _ := m.DB.IPv6EgressConfig(ctx)
	c.Enabled = false
	m.DB.SaveIPv6EgressConfig(ctx, c)
	if _, _, err := m.AcquireAccount(ctx, id); err == nil {
		t.Fatal("disabled pool allowed account route")
	}
}

func TestAccountHTTPBodyHoldsLeaseUntilClose(t *testing.T) {
	m, id := httpFixture(t)
	ctx := context.Background()
	if err := m.DB.UpdateCredentials(ctx, id, map[string]interface{}{"ipv6_egress_enabled": true}); err != nil {
		t.Fatal(err)
	}
	sources, fallbacks := 0, 0
	base := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		fallbacks++
		return nil, fmt.Errorf("unexpected fallback")
	})}
	client := WrapAccountClient(m, id, base, func(string) http.RoundTripper {
		return roundTripFunc(func(*http.Request) (*http.Response, error) {
			sources++
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("body"))}, nil
		})
	}, true)
	req, _ := http.NewRequest("GET", "https://fixture.invalid", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	m.DB.UpdateCredentials(ctx, id, map[string]interface{}{"ipv6_egress_enabled": false})
	if err = m.RetireAccount(ctx, id); err != nil {
		t.Fatal(err)
	}
	_, cooldowns, _ := m.DB.IPv6EgressStatus(ctx, time.Now().Unix())
	if len(cooldowns) != 0 {
		t.Fatal("active body retired early")
	}
	if _, err = client.Do(req); err == nil {
		t.Fatal("stale HTTP wrapper reacquired removed membership")
	}
	if sources != 1 || fallbacks != 0 {
		t.Fatalf("sources=%d fallbacks=%d", sources, fallbacks)
	}
	if err = resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	bindings, cooldowns, _ := m.DB.IPv6EgressStatus(ctx, time.Now().Unix())
	if len(bindings) != 0 || len(cooldowns) != 1 {
		t.Fatalf("not retired after close: %+v %+v", bindings, cooldowns)
	}
}

func TestAccountHTTPDisabledPoolNeverUsesFallback(t *testing.T) {
	m, id := httpFixture(t)
	ctx := context.Background()
	m.DB.UpdateCredentials(ctx, id, map[string]interface{}{"ipv6_egress_enabled": true})
	c, _ := m.DB.IPv6EgressConfig(ctx)
	c.Enabled = false
	m.DB.SaveIPv6EgressConfig(ctx, c)
	calls := 0
	client := WrapAccountClient(m, id, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, fmt.Errorf("fallback") })}, func(string) http.RoundTripper { calls++; return nil }, false)
	req, _ := http.NewRequest("GET", "https://fixture.invalid", nil)
	if _, err := client.Do(req); err == nil {
		t.Fatal("accepted disabled pool")
	}
	if calls != 0 {
		t.Fatal("dialed with disabled pool")
	}
}

func TestIPv6BootstrapReservationSharesPoolAndCooldown(t *testing.T) {
	m, id := httpFixture(t)
	ctx := context.Background()
	if err := m.DB.UpdateCredentials(ctx, id, map[string]interface{}{"ipv6_egress_enabled": true}); err != nil {
		t.Fatal(err)
	}
	ip, release, err := m.AcquireBootstrap(ctx)
	if err != nil || ip == "" {
		t.Fatalf("bootstrap: %q %v", ip, err)
	}
	other := New(m.DB)
	other.LocalIPs = m.LocalIPs
	route, accountRelease, err := other.AcquireAccount(ctx, id)
	if err != nil || route.Binding.IP == ip {
		t.Fatalf("bootstrap source reused: %+v %v", route, err)
	}
	accountRelease()
	release()
	release()
	_, cooldowns, err := m.DB.IPv6EgressStatus(ctx, time.Now().Unix())
	if err != nil || len(cooldowns) != 1 || cooldowns[0].IP != ip {
		t.Fatalf("bootstrap cooldown: %+v %v", cooldowns, err)
	}
	cfg, _ := m.DB.IPv6EgressConfig(ctx)
	cfg.Enabled = false
	if err := m.DB.SaveIPv6EgressConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.AcquireBootstrap(ctx); err == nil {
		t.Fatal("disabled pool allowed OAuth bootstrap")
	}
}
