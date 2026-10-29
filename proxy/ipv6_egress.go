package proxy

import (
	"context"
	"fmt"
	"github.com/codex2api/database"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/egressipv6"
	"github.com/codex2api/internal/egressroute"
)

const localIPv6RoutePrefix = "local-ipv6://"

// These internal route tokens never pass through public proxy configuration.
func IPv6RouteIP(route string) string { return strings.TrimPrefix(route, localIPv6RoutePrefix) }
func IsIPv6Route(route string) bool   { return strings.HasPrefix(route, localIPv6RoutePrefix) }
func NativeIPv6Account(account *auth.Account) bool {
	_, enabled := account.IPv6EgressSelection()
	return enabled
}

func wrapIPv6Client(account *auth.Account, base *http.Client, replay, forceUTLS bool, scopes ...string) *http.Client {
	mode := codexTransportModeFromEnv()
	if forceUTLS {
		mode = codexTransportModeUTLSChrome
	}
	scope := "responses"
	if len(scopes) > 0 {
		scope = scopes[0]
	}
	return wrapScopedIPv6Client(account, base, replay, mode, scope)
}

func wrapScopedIPv6Client(account *auth.Account, base *http.Client, replay bool, mode, scope string) *http.Client {
	if mode == "" {
		mode = codexTransportModeFromEnv()
	}
	if account.GetProxyURL() != "" {
		return accountProxyClient(account, mode == codexTransportModeUTLSChrome)
	}
	if account != nil && account.HostIPv4Egress() {
		configured, _ := account.IPv6EgressSelection()
		if configured {
			return hostIPv4Client(account.ID(), base, mode, scope)
		}
	}
	if route, enrolled, err := resolveFixedRoute(context.Background(), account); enrolled {
		if err != nil {
			return directFallbackClient(account, &http.Client{Transport: errorTransport{egressroute.Unavailable(err)}}, "fixed", mode == codexTransportModeUTLSChrome)
		}
		// Build the transport with the requested TLS/profile mode, then bind its
		// pool identity to the durable resource generation.
		if mode == "" {
			mode = codexTransportModeFromEnv()
		}
		return directFallbackClient(account, fixedClientForRouteMode(account, route, base, mode, scope, replay), "fixed", mode == codexTransportModeUTLSChrome)
	}
	m := egressipv6.Current()
	if !NativeIPv6Account(account) {
		return base
	}
	if m == nil {
		return directFallbackClient(account, &http.Client{Transport: errorTransport{egressroute.Unavailable(database.ErrIPv6Unavailable)}}, "local_ipv6", mode == codexTransportModeUTLSChrome)
	}
	return directFallbackClient(account, egressipv6.WrapAccountClient(m, account.ID(), base, func(ip string) http.RoundTripper { return sourceIPv6Transport(account.ID(), ip, mode, scope) }, replay), "local_ipv6", mode == codexTransportModeUTLSChrome)
}

func sourceIPv6Transport(id int64, ip, mode, scope string) http.RoundTripper {
	key := fmt.Sprintf("ipv6|%d|%s|%s|%s", id, ip, mode, scope)
	if v, ok := clientPool.Load(key); ok {
		entry := v.(*poolEntry)
		entry.touch()
		return entry.client.Transport
	}
	dialer, err := egressipv6.NewDialer(ip)
	if err != nil {
		return errorTransport{err}
	}
	var rt http.RoundTripper
	if mode == codexTransportModeUTLSChrome {
		rt = &utlsRoundTripper{connections: make(map[string]*utlsConn), pending: make(map[string]*sync.Cond), dialer: dialer}
	} else {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.Proxy = nil
		tr.DialContext = dialer.DialContext
		tr.ResponseHeaderTimeout = 0
		tr.TLSHandshakeTimeout = 0
		tr.MaxIdleConnsPerHost = 4
		tr.IdleConnTimeout = 90 * time.Second
		rt = tr
	}
	entry := &poolEntry{client: &http.Client{Transport: rt}, createdAt: time.Now().UnixNano()}
	entry.touch()
	if v, loaded := clientPool.LoadOrStore(key, entry); loaded {
		releaseEvictedClient(entry.client)
		e := v.(*poolEntry)
		e.touch()
		return e.client.Transport
	}
	return rt
}

type errorTransport struct{ err error }

func (e errorTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, e.err }

// OAuth refresh must never replay a potentially consumed refresh token.
func WrapOAuthIPv6Client(id int64, base *http.Client, forceUTLS bool) *http.Client {
	c := *base
	c.Transport = oauthIPv6Transport{id, base, forceUTLS}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

type oauthIPv6Transport struct {
	id        int64
	base      *http.Client
	forceUTLS bool
}

func (t oauthIPv6Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	m := egressipv6.Current()
	if bound, ok := auth.AccountBoundEgress(req.Context()); ok && bound != "" {
		a := &auth.Account{DBID: t.id, ProxyURL: bound}
		return accountProxyClient(a, t.forceUTLS).Transport.RoundTrip(req)
	}
	if m == nil {
		return t.base.Transport.RoundTrip(req)
	}
	configured, enabled, proxyURL, err := m.Selection(req.Context(), t.id)
	if err != nil {
		return nil, egressroute.Unavailable(err)
	}
	mode, scope := codexTransportModeStandard, "oauth-refresh"
	if t.forceUTLS {
		mode, scope = codexTransportModeUTLSChrome, "oauth-session"
	}
	client := t.base
	if proxyURL != "" {
		return accountProxyClient(&auth.Account{DBID: t.id, ProxyURL: proxyURL}, t.forceUTLS).Transport.RoundTrip(req)
	}
	if enabled {
		primary := egressipv6.WrapAccountClient(m, t.id, t.base, func(ip string) http.RoundTripper { return sourceIPv6Transport(t.id, ip, mode, scope) }, false)
		direct := hostIPv4Client(t.id, t.base, mode, "oauth-fallback")
		copy := *primary
		copy.Transport = egressroute.Fallback(primary.Transport, direct.Transport, egressroute.Observation{Kind: "local_ipv6", Source: "account"})
		client = &copy
	} else if configured && proxyURL == "" {
		client = hostIPv4Client(t.id, t.base, mode, scope)
	}
	rt := client.Transport
	if rt == nil {
		rt = http.DefaultTransport
	}
	return rt.RoundTrip(req)
}

// HostIPv4Dialer deliberately bypasses environment proxies and IPv6 DNS results.
type HostIPv4Dialer struct{ net.Dialer }

func (d *HostIPv4Dialer) DialContext(ctx context.Context, _ string, address string) (net.Conn, error) {
	return d.Dialer.DialContext(ctx, "tcp4", address)
}
func (d *HostIPv4Dialer) Dial(_ string, address string) (net.Conn, error) {
	return d.DialContext(context.Background(), "tcp4", address)
}
func hostIPv4Client(id int64, base *http.Client, mode, scope string) *http.Client {
	key := fmt.Sprintf("ipv4|%d|%s|%s", id, mode, scope)
	c := *base
	if v, ok := clientPool.Load(key); ok {
		entry := v.(*poolEntry)
		entry.touch()
		c.Transport = entry.client.Transport
		return &c
	}
	dialer := &HostIPv4Dialer{net.Dialer{KeepAlive: 30 * time.Second}}
	if mode == codexTransportModeUTLSChrome {
		c.Transport = &utlsRoundTripper{connections: make(map[string]*utlsConn), pending: make(map[string]*sync.Cond), dialer: dialer}
	} else {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.Proxy = nil
		tr.DialContext = dialer.DialContext
		tr.ResponseHeaderTimeout = 0
		tr.TLSHandshakeTimeout = 0
		tr.MaxIdleConnsPerHost = 4
		tr.IdleConnTimeout = 90 * time.Second
		c.Transport = tr
	}
	entry := &poolEntry{client: &c, createdAt: time.Now().UnixNano()}
	entry.touch()
	if v, loaded := clientPool.LoadOrStore(key, entry); loaded {
		releaseEvictedClient(entry.client)
		c.Transport = v.(*poolEntry).client.Transport
	}
	return &c
}

func resolveIPv6Websocket(ctx context.Context, account *auth.Account, egress CodexEgress) CodexEgress {
	m := egressipv6.Current()
	if !NativeIPv6Account(account) {
		return egress
	}
	if m == nil {
		egress.Err = database.ErrIPv6Unavailable
		return egress
	}
	route, release, err := m.AcquireAccount(ctx, account.ID())
	if release != nil {
		release()
	}
	if err != nil {
		egress.Err = err
		return egress
	}
	if route.Config.Enabled {
		egress.Kind = "local_ipv6"
		egress.DialProxyURL = localIPv6RoutePrefix + route.Binding.IP
		egress.IPv6 = route
	}
	return egress
}

// WrapBootstrapOAuthClient applies the requested new-account route before an ID
// exists. The temporary reservation uses the same persistent pool lease table.
func WrapBootstrapOAuthClient(base *http.Client, enabled bool) *http.Client {
	c := *base
	if !enabled {
		return hostIPv4Client(0, base, codexTransportModeStandard, "oauth-bootstrap")
	}
	c.Transport = bootstrapIPv6Transport{}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

type bootstrapIPv6Transport struct{}

func (bootstrapIPv6Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	m := egressipv6.Current()
	if m == nil {
		return nil, database.ErrIPv6Unavailable
	}
	ip, release, err := m.AcquireBootstrap(req.Context())
	if err != nil {
		return nil, err
	}
	resp, err := sourceIPv6Transport(0, ip, codexTransportModeStandard, "oauth-bootstrap").RoundTrip(req)
	if err != nil {
		release()
		return nil, err
	}
	if resp.Body == nil {
		release()
	} else {
		resp.Body = &bootstrapLeaseBody{ReadCloser: resp.Body, release: release}
	}
	return resp, nil
}

type bootstrapLeaseBody struct {
	io.ReadCloser
	release func()
}

func (b *bootstrapLeaseBody) Close() error { err := b.ReadCloser.Close(); b.release(); return err }
