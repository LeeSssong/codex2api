package proxy

import (
	"context"
	"fmt"
	"github.com/codex2api/auth"
	"github.com/codex2api/internal/egressroute"
	"net/http"
)

// accountProxyClient uses the same strict transport and account-separated pool
// for generation and auxiliary requests. The transport owns safe direct fallback.
func accountProxyClient(account *auth.Account, forceUTLS bool) *http.Client {
	route := account.GetProxyURL()
	if auth.AccountProxyUnavailable != nil && auth.AccountProxyUnavailable(account.ID(), route) {
		return directFallbackClient(account, &http.Client{Transport: errorTransport{egressroute.Unavailable(fmt.Errorf("bound proxy disabled"))}}, "account_proxy", forceUTLS)
	}
	if !forceUTLS {
		return getPooledClientForRoute(account, route, true)
	}
	mode := codexTransportModeUTLSChrome
	key := "bound-maint|" + maintenanceClientKey(account, route, mode, "account")
	if v, ok := clientPool.Load(key); ok {
		e := v.(*poolEntry)
		e.touch()
		return e.client
	}
	var primary http.RoundTripper
	if err := auth.ConfigureTransportProxy(&http.Transport{}, route, nil); err != nil {
		primary = errorTransport{egressroute.Unavailable(err)}
	} else {
		primary = NewUTLSTransport(route)
	}
	direct := NewUTLSTransport("")
	entry := &poolEntry{client: &http.Client{Transport: egressroute.Fallback(primary, direct, egressroute.ProxyObservation(route))}}
	entry.touch()
	if v, loaded := clientPool.LoadOrStore(key, entry); loaded {
		releaseEvictedClient(entry.client)
		return v.(*poolEntry).client
	}
	return entry.client
}

func directFallbackClient(account *auth.Account, primary *http.Client, kind string, forceUTLS bool) *http.Client {
	direct := getPooledClientForRoute(account, "", true)
	if forceUTLS {
		direct = hostIPv4Client(account.ID(), direct, codexTransportModeUTLSChrome, "fallback")
	}
	copy := *primary
	copy.Transport = egressroute.Fallback(primary.Transport, direct.Transport, egressroute.Observation{Kind: kind, Source: "account"})
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &copy
}

// WithAccountEgressAudit collects the route actually attempted by the transports.
func WithAccountEgressAudit(ctx context.Context) context.Context { return egressroute.WithAudit(ctx) }
func AccountEgressAudit(ctx context.Context) map[string]string {
	v := egressroute.Snapshot(ctx)
	if v.Kind == "" {
		return nil
	}
	out := map[string]string{"egress_kind": v.Kind, "egress_source": v.Source, "proxy": v.Kind}
	if v.Kind == "account_proxy" {
		out["proxy"] = "configured"
	}
	if v.ProxyHost != "" {
		out["proxy_host"] = v.ProxyHost
	}
	if v.SourceIP != "" {
		out["source_ip"] = v.SourceIP
	}
	if v.FallbackFrom != "" {
		out["fallback_from"] = v.FallbackFrom
		out["fallback_reason"] = v.FallbackReason
	}
	return out
}
