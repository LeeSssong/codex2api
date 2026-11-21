package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/codex2api/egressipv6"
	"github.com/codex2api/fixedegress"
	"github.com/codex2api/internal/egressroute"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

type fixedEgressTransport struct {
	manager     *fixedegress.Manager
	id          int64
	base        *http.Client
	mode, scope string
	replay      bool
}

func (t *fixedEgressTransport) CloseIdleConnections() {}
func (t *fixedEgressTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	route, e := t.manager.Acquire(req.Context(), t.id)
	if e != nil {
		return nil, egressroute.Unavailable(e)
	}
	if !route.Enrolled {
		rt := t.base.Transport
		if rt == nil {
			rt = http.DefaultTransport
		}
		return rt.RoundTrip(req)
	}

	// The outer gateway owns retries for account/model errors. Only failures
	// proving that no upstream request was accepted may replay here.
	for attempt := 1; ; attempt++ {
		release, e := t.manager.TrackRoute(req.Context(), route)
		if e != nil {
			return nil, egressroute.Unavailable(e)
		}
		eg := egressroute.ProxyObservation(route.Binding.ProxyURL)
		eg.Kind = "fixed"
		egressroute.Record(req.Context(), eg)
		resp, requestErr := fixedRawTransport(t.id, route, t.mode, t.scope).RoundTrip(req)
		reason := fixedFailureReason(resp, requestErr, route.Pool)
		if resp != nil && resp.StatusCode >= 400 && ((resp.StatusCode == 429 && route.Pool.Rotate429) || (route.Pool.Retry5xx && resp.StatusCode >= 500)) {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, 65537))
			resp.Body = &fixedPrefixBody{Reader: io.MultiReader(bytes.NewReader(body), resp.Body), Closer: resp.Body}
			reason = ""
			if readErr == nil && len(body) <= 65536 {
				reason = egressipv6.FailureReason(resp.StatusCode, body, route.Pool.Retry5xx)
			}
		}
		if reason != "" && req.Context().Err() == nil {
			_ = t.manager.Failure(req.Context(), t.id, route.Binding.Generation, route.Binding.ResourceID, reason)
		}
		if requestErr != nil {
			release()
		} else if resp != nil && resp.Body != nil {
			resp.Body = &fixedLeaseBody{ReadCloser: resp.Body, release: release}
		} else {
			release()
		}
		safeConnect := reason == "connect_failed" || reason == "proxy_auth"
		if !safeConnect || !t.replay || attempt >= route.Pool.MaxAttempts || req.Context().Err() != nil {
			return resp, requestErr
		}
		next, e := t.manager.Acquire(req.Context(), t.id)
		if e != nil || next.Binding.Generation == route.Binding.Generation {
			return resp, requestErr
		}
		if req.Body != nil && req.GetBody == nil {
			return resp, requestErr
		}
		clone := req.Clone(req.Context())
		if req.Body != nil {
			clone.Body, e = req.GetBody()
			if e != nil {
				return resp, requestErr
			}
		}
		if resp != nil {
			resp.Body.Close()
		}
		req = clone
		route = next
	}
}

type fixedPrefixBody struct {
	io.Reader
	io.Closer
}
type fixedLeaseBody struct {
	io.ReadCloser
	release func()
	once    sync.Once
}

func (b *fixedLeaseBody) Read(p []byte) (int, error) {
	n, e := b.ReadCloser.Read(p)
	if e == io.EOF {
		b.once.Do(b.release)
	}
	return n, e
}
func (b *fixedLeaseBody) Close() error { e := b.ReadCloser.Close(); b.once.Do(b.release); return e }
func fixedFailureReason(resp *http.Response, e error, p database.FixedEgressPool) string {
	if e != nil {
		if errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) {
			return ""
		}
		for inner := e; inner != nil; inner = errors.Unwrap(inner) {
			if op, ok := inner.(*net.OpError); ok && op.Op == "dial" {
				return "connect_failed"
			}
		}
		return ""
	}
	if resp != nil && resp.StatusCode == 407 {
		return "proxy_auth"
	}
	return ""
}
func fixedRawTransport(id int64, r fixedegress.Route, mode, scope string) http.RoundTripper {
	endpointHash := sha256.Sum256([]byte(r.Binding.ProxyURL))
	key := fmt.Sprintf("fixed-transport|%d|%s|%d|%s|%s|%x", id, r.Binding.ResourceID, r.Binding.Generation, mode, scope, endpointHash[:8])
	if v, ok := clientPool.Load(key); ok {
		p := v.(*poolEntry)
		p.touch()
		return p.client.Transport
	}
	var rt http.RoundTripper
	if mode == codexTransportModeUTLSChrome {
		rt = NewUTLSTransport(r.Binding.ProxyURL)
	} else {
		rt = newCodexStandardTransport(r.Binding.ProxyURL)
	}
	entry := &poolEntry{client: &http.Client{Transport: rt}, createdAt: time.Now().UnixNano()}
	entry.touch()
	if v, ok := clientPool.LoadOrStore(key, entry); ok {
		releaseEvictedClient(entry.client)
		p := v.(*poolEntry)
		p.touch()
		return p.client.Transport
	}
	return rt
}
func WrapFixedClient(id int64, base *http.Client) *http.Client {
	return wrapFixedClient(id, base, codexTransportModeFromEnv(), "maintenance", false)
}
func wrapFixedClient(id int64, base *http.Client, mode, scope string, replay bool) *http.Client {
	m := fixedegress.Current()
	if m == nil || base == nil {
		return base
	}
	c := *base
	primary := &fixedEgressTransport{m, id, base, mode, scope, replay}
	direct := hostIPv4Client(id, base, mode, "fixed-fallback")
	c.Transport = egressroute.Fallback(primary, direct.Transport, egressroute.Observation{Kind: "fixed", Source: "account"})
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}
func WrapOAuthFixedClient(id int64, base *http.Client, forceUTLS bool) *http.Client {
	if m := egressipv6.Current(); m != nil {
		configured, _, _, err := m.Selection(context.Background(), id)
		if err != nil {
			return &http.Client{Transport: errorTransport{err}}
		}
		if configured {
			return base
		}
	}
	mode := codexTransportModeStandard
	scope := "oauth-refresh"
	if forceUTLS {
		mode = codexTransportModeUTLSChrome
		scope = "oauth-session"
	}
	return wrapFixedClient(id, base, mode, scope, false)
}
func fixedClientForRoute(account *auth.Account, r fixedegress.Route, base *http.Client) *http.Client {
	return fixedClientForRouteMode(account, r, base, codexTransportModeFromEnv(), "responses", true)
}
func fixedClientForRouteMode(account *auth.Account, r fixedegress.Route, base *http.Client, mode string, options ...any) *http.Client {
	scope := "responses"
	replay := true
	if len(options) > 0 {
		scope, _ = options[0].(string)
	}
	if len(options) > 1 {
		replay, _ = options[1].(bool)
	}
	return wrapFixedClient(account.ID(), base, mode, scope, replay)
}
func resolveFixedRoute(ctx context.Context, account *auth.Account) (fixedegress.Route, bool, error) {
	m := fixedegress.Current()
	configured, _ := account.IPv6EgressSelection()
	if m == nil || account == nil || account.IsRelayStyle() || configured {
		return fixedegress.Route{}, false, nil
	}
	r, e := m.Acquire(ctx, account.ID())

	return r, r.Enrolled, e
}
func FixedRoute(ctx context.Context, account *auth.Account) (fixedegress.Route, bool, error) {
	return resolveFixedRoute(ctx, account)
}
