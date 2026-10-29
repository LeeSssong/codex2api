package proxy

import (
	"context"
	"errors"
	"net/http"

	"github.com/codex2api/auth"
	"github.com/codex2api/internal/egressroute"
)

// WithAccountBoundEgress freezes an account's bound proxy for this request.
// An empty proxy URL explicitly means direct egress, including when Resin or
// environment proxies are configured. Callers must pass account.GetProxyURL(),
// not the inherited group/global/pool route.
func WithAccountBoundEgress(ctx context.Context, proxyURL string) context.Context {
	return auth.WithAccountBoundEgress(ctx, proxyURL)
}

// AccountBoundEgress reports the explicit route and whether it was requested.
func AccountBoundEgress(ctx context.Context) (string, bool) {
	return auth.AccountBoundEgress(ctx)
}

// requestProxyURL preserves an explicit empty route instead of re-reading an
// account or inheriting configuration after this request started.
func requestProxyURL(ctx context.Context, fallback string) string {
	if route, ok := AccountBoundEgress(ctx); ok {
		return route
	}
	return fallback
}

func getRequestClient(ctx context.Context, account *auth.Account, fallback string) *http.Client {
	if account.GetProxyURL() != "" || NativeIPv6Account(account) {
		return getPooledClient(account, fallback)
	}
	if r, enrolled, _ := resolveFixedRoute(ctx, account); enrolled {
		_ = r
		return getPooledClient(account, fallback)
	}
	if route, ok := AccountBoundEgress(ctx); ok {
		if route == "" {
			egressroute.Record(ctx, egressroute.Observation{Kind: "direct", Source: "account"})
		}
		if NativeIPv6Account(account) {
			if route != "" {
				return &http.Client{Transport: errorTransport{errors.New("account-bound proxy conflicts with local IPv6 membership")}}
			}
			return getPooledClient(account, "")
		}
		base := getPooledClientForRoute(account, route, true)
		if configured, _ := account.IPv6EgressSelection(); configured && route == "" {
			return wrapIPv6Client(account, base, true, false)
		}
		return base
	}
	return getPooledClient(account, fallback)
}

func recycleRequestClient(ctx context.Context, account *auth.Account, fallback string) {
	if route, ok := AccountBoundEgress(ctx); ok {
		recyclePooledClientForRoute(account, route, true)
		return
	}
	recyclePooledClient(account, fallback)
}

type accountBoundProxyErrorTransport struct{}

func (accountBoundProxyErrorTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("invalid account-bound proxy configuration")
}

func newAccountBoundTransport(proxyURL string) http.RoundTripper {
	direct := newCodexTransport("")
	if standard, ok := direct.(*http.Transport); ok {
		standard.Proxy = nil
	}
	if proxyURL == "" {
		return direct
	}
	var primary http.RoundTripper
	if err := auth.ConfigureTransportProxy(&http.Transport{}, proxyURL, nil); err != nil {
		primary = errorTransport{egressroute.Unavailable(err)}
	} else {
		primary = newCodexTransport(proxyURL)
	}
	return egressroute.Fallback(primary, direct, egressroute.ProxyObservation(proxyURL))
}
