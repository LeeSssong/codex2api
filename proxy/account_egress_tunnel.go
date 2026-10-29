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

// StartAccountEgressTunnel adapts account proxy/IPv6 to a credential worker's
// HTTP CONNECT input. Only connection establishment can fall back to host direct;
// OAuth bytes pass once through the tunnel and can never be replayed here.
func StartAccountEgressTunnel(account *auth.Account) (string, func(), error) {
	return egressroute.StartTunnel(func(ctx context.Context, target string) (net.Conn, error) {
		bound := account.GetProxyURL()
		var selected xproxy.Dialer
		var release func()
		var err error
		if bound != "" {
			if auth.AccountProxyUnavailable != nil && auth.AccountProxyUnavailable(account.ID(), bound) {
				err = fmt.Errorf("bound proxy disabled")
			} else {
				selected, err = buildProxyDialer(bound)
			}
		} else if NativeIPv6Account(account) {
			if m := egressipv6.Current(); m != nil {
				var route egressipv6.Route
				route, release, err = m.AcquireAccount(ctx, account.ID())
				if err == nil {
					selected, err = egressipv6.NewDialer(route.Binding.IP)
				}
			} else {
				err = fmt.Errorf("IPv6 unavailable")
			}
		} else if route, enrolled, e := resolveFixedRoute(ctx, account); enrolled {
			err = e
			if e == nil {
				selected, err = buildProxyDialer(route.Binding.ProxyURL)
			}
		}
		if err == nil && selected != nil {
			conn, e := dialSelected(ctx, selected, target)
			if e == nil {
				if release != nil {
					return &accountTunnelConn{Conn: conn, release: release}, nil
				}
				return conn, nil
			}
			err = e
		}
		if release != nil {
			release()
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Dial only: no account request has been sent, so direct is safe.
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", target)
	})
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

type accountTunnelConn struct {
	net.Conn
	release func()
	once    sync.Once
}

func (c *accountTunnelConn) Close() error { e := c.Conn.Close(); c.once.Do(c.release); return e }
