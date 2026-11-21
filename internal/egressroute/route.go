// Package egressroute shares account egress observations and safe direct fallback
// between HTTP, OAuth, websocket, and credential runtime adapters.
package egressroute

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"sync"
	"sync/atomic"
)

type Observation struct {
	Kind           string `json:"kind"`
	Source         string `json:"source"`
	ProxyHost      string `json:"proxy_host,omitempty"`
	SourceIP       string `json:"source_ip,omitempty"`
	FallbackFrom   string `json:"fallback_from,omitempty"`
	FallbackReason string `json:"fallback_reason,omitempty"`
}
type auditKey struct{}
type audit struct {
	mu    sync.Mutex
	value Observation
}

func WithAudit(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Value(auditKey{}).(*audit); ok {
		return ctx
	}
	return context.WithValue(ctx, auditKey{}, &audit{})
}
func Record(ctx context.Context, v Observation) {
	if ctx == nil {
		return
	}
	if a, ok := ctx.Value(auditKey{}).(*audit); ok {
		a.mu.Lock()
		a.value = v
		a.mu.Unlock()
	}
}
func Snapshot(ctx context.Context) Observation {
	if ctx != nil {
		if a, ok := ctx.Value(auditKey{}).(*audit); ok {
			a.mu.Lock()
			defer a.mu.Unlock()
			return a.value
		}
	}
	return Observation{}
}
func ProxyObservation(raw string) Observation {
	v := Observation{Kind: "account_proxy", Source: "account"}
	if u, e := url.Parse(raw); e == nil {
		v.ProxyHost = u.Host
	}
	return v
}

type unavailable struct{ error }

func (e unavailable) Unwrap() error { return e.error }

// Unavailable marks a failure discovered before sending account request bytes.
func Unavailable(e error) error {
	if e == nil {
		return nil
	}
	return unavailable{e}
}
func CanFallback(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var setup unavailable
	if errors.As(err, &setup) {
		return true
	}
	for e := err; e != nil; e = errors.Unwrap(e) {
		if op, ok := e.(*net.OpError); ok && op.Op == "dial" {
			return true
		}
	}
	return false
}

type fallbackTransport struct {
	primary, direct http.RoundTripper
	route           Observation
}

func Fallback(primary, direct http.RoundTripper, route Observation) http.RoundTripper {
	return &fallbackTransport{primary, direct, route}
}
func (t *fallbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	Record(req.Context(), t.route)
	var sent atomic.Bool
	trace := &httptrace.ClientTrace{WroteHeaders: func() { sent.Store(true) }, WroteRequest: func(httptrace.WroteRequestInfo) { sent.Store(true) }}
	attempt := req.Clone(httptrace.WithClientTrace(req.Context(), trace))
	resp, err := t.primary.RoundTrip(attempt)
	proxyAuth := resp != nil && resp.StatusCode == http.StatusProxyAuthRequired && req.Context().Err() == nil && !sent.Load()
	if !proxyAuth && (err == nil || req.Context().Err() != nil || sent.Load() || !CanFallback(err)) {
		return resp, err
	}
	next := req.Clone(req.Context())
	if req.Body != nil && req.Body != http.NoBody {
		if req.GetBody == nil {
			return resp, err
		}
		body, e := req.GetBody()
		if e != nil {
			return resp, err
		}
		next.Body = body
	}
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	Record(req.Context(), Observation{Kind: "direct", Source: t.route.Source, FallbackFrom: t.route.Kind, FallbackReason: "bound_egress_unavailable"})
	return t.direct.RoundTrip(next)
}
func (t *fallbackTransport) CloseIdleConnections() {
	for _, rt := range []http.RoundTripper{t.primary, t.direct} {
		if c, ok := rt.(interface{ CloseIdleConnections() }); ok {
			c.CloseIdleConnections()
		}
	}
}
