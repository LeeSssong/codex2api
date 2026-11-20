package proxy

import (
	"context"
	"fmt"
	"github.com/codex2api/auth"
	"github.com/codex2api/egressipv6"
	"github.com/codex2api/internal/egressroute"
	xproxy "golang.org/x/net/proxy"
	"net"
	"sync"
	"time"
)

func StartAccountEgressTunnel(account *auth.Account) (string, func(), error) {
	if account == nil {
		return "", nil, fmt.Errorf("account is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	bound := account.GetProxyURL()
	ipv6 := NativeIPv6Account(account)
	var selected xproxy.Dialer
	var release func()
	var err error
	if bound != "" {
		if auth.AccountProxyUnavailable != nil && auth.AccountProxyUnavailable(account.ID(), bound) {
			return "", nil, fmt.Errorf("bound proxy disabled")
		}
		selected, err = buildProxyDialer(bound)
	} else if ipv6 {
		manager := egressipv6.Current()
		if manager == nil {
			return "", nil, fmt.Errorf("IPv6 unavailable")
		}
		var route egressipv6.Route
		route, release, err = manager.AcquireAccount(ctx, account.ID())
		if err == nil {
			selected, err = egressipv6.NewDialer(route.Binding.IP)
		}
	} else if route, enrolled, routeErr := resolveFixedRoute(ctx, account); enrolled {
		err = routeErr
		if err == nil {
			selected, err = buildProxyDialer(route.Binding.ProxyURL)
		}
	} else if account.WorkstationRequiresDedicatedEgress() {
		err = fmt.Errorf("workstation requires dedicated egress")
	} else {
		selected = &net.Dialer{Timeout: 10 * time.Second}
	}
	if err != nil || selected == nil {
		if release != nil {
			release()
		}
		return "", nil, fmt.Errorf("account authorization egress unavailable")
	}
	tunnel, closeTunnel, err := egressroute.StartTunnel(func(ctx context.Context, target string) (net.Conn, error) {
		if account.GetProxyURL() != bound || NativeIPv6Account(account) != ipv6 {
			return nil, fmt.Errorf("account authorization route changed")
		}
		if bound != "" && auth.AccountProxyUnavailable != nil && auth.AccountProxyUnavailable(account.ID(), bound) {
			return nil, fmt.Errorf("bound proxy disabled")
		}
		return dialSelected(ctx, selected, target)
	})
	if err != nil {
		if release != nil {
			release()
		}
		return "", nil, err
	}
	var once sync.Once
	closeAll := func() {
		once.Do(func() {
			closeTunnel()
			if release != nil {
				release()
			}
		})
	}
	timer := time.AfterFunc(30*time.Minute, closeAll)
	return tunnel, func() { timer.Stop(); closeAll() }, nil
}
func dialSelected(ctx context.Context, d xproxy.Dialer, target string) (net.Conn, error) {
	if d, ok := d.(xproxy.ContextDialer); ok {
		return d.DialContext(ctx, "tcp", target)
	}
	type result struct {
		c net.Conn
		e error
	}
	ch := make(chan result)
	go func() {
		c, e := d.Dial("tcp", target)
		select {
		case ch <- result{c, e}:
		case <-ctx.Done():
			if c != nil {
				c.Close()
			}
		}
	}()
	select {
	case r := <-ch:
		return r.c, r.e
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
