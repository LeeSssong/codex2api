package wsrelay

import (
	"context"
	"errors"
	"net"
	"net/http"

	"github.com/codex2api/auth"
	"github.com/codex2api/egressipv6"
	"github.com/codex2api/internal/egressroute"
	"github.com/codex2api/proxy"
	"github.com/tidwall/gjson"
)

type ipv6WSAttempt struct {
	ctx       context.Context
	manager   *egressipv6.Manager
	accountID int64
	route     egressipv6.Route
	attempts  int
	execute   func(string) (*WsResponse, error)
}

func (e *Executor) ExecuteRequestViaWebsocket(ctx context.Context, account *auth.Account, body []byte, sessionID, proxyOverride, apiKey string, deviceCfg *proxy.DeviceProfileConfig, headers http.Header, poolRouteKey string) (*WsResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, bound := proxy.AccountBoundEgress(ctx); (bound || proxy.IsStateDirectRoute(proxyOverride)) && !proxy.NativeIPv6Account(account) {
		return e.executeRequestViaWebsocket(ctx, account, body, sessionID, proxyOverride, apiKey, deviceCfg, headers, poolRouteKey)
	}
	m := egressipv6.Current()
	if !proxy.NativeIPv6Account(account) {
		return e.executeRequestViaWebsocket(ctx, account, body, sessionID, proxyOverride, apiKey, deviceCfg, headers, poolRouteKey)
	}
	if m == nil {
		eg := context.WithValue(ctx, directFallbackKey{}, true)
		egres := egressroute.Observation{Kind: "direct", Source: "account", FallbackFrom: "local_ipv6", FallbackReason: "bound_egress_unavailable"}
		egressroute.Record(ctx, egres)
		return e.executeRequestViaWebsocket(eg, account, body, sessionID, "", apiKey, deviceCfg, headers, poolRouteKey)
	}
	if boundProxy, bound := proxy.AccountBoundEgress(ctx); bound && boundProxy != "" {
		return nil, errors.New("account-bound route conflicts with local IPv6 membership")
	}
	route, release, err := m.AcquireAccount(ctx, account.ID())
	if release != nil {
		release()
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, err
		}
		eg := context.WithValue(ctx, directFallbackKey{}, true)
		egressroute.Record(ctx, egressroute.Observation{Kind: "direct", Source: "account", FallbackFrom: "local_ipv6", FallbackReason: "bound_egress_unavailable"})
		return e.executeRequestViaWebsocket(eg, account, body, sessionID, "", apiKey, deviceCfg, headers, poolRouteKey)
	}

	a := &ipv6WSAttempt{ctx: ctx, manager: m, accountID: account.ID(), route: route}
	a.execute = func(ip string) (*WsResponse, error) {
		return e.executeRequestViaWebsocket(ctx, account, body, sessionID, "local-ipv6://"+ip, apiKey, deviceCfg, headers, poolRouteKey)
	}
	return a.run()
}
func (a *ipv6WSAttempt) run() (*WsResponse, error) {
	for {
		a.attempts++
		if a.manager.ObserveSource != nil {
			a.manager.ObserveSource(a.ctx, a.accountID, a.route.Binding.IP)
		}
		r, err := a.execute(a.route.Binding.IP)
		if err == nil {
			r.ipv6Attempt = a
			return r, nil
		}
		reason := ""
		var hs *HandshakeHTTPError
		var op *net.OpError
		if errors.As(err, &hs) {
			reason = egressipv6.FailureReason(hs.StatusCode, []byte(hs.Body), a.route.Config.Retry5xx)
		} else if errors.As(err, &op) && op.Op == "dial" {
			reason = "connect_failed"
		}
		if !a.rotate(reason) {
			return nil, err
		}
	}
}
func (a *ipv6WSAttempt) rotate(reason string) bool {
	if reason == "" || a.ctx.Err() != nil {
		return false
	}
	next, err := a.manager.RotateAccount(a.ctx, a.accountID, a.route.Binding.IP, reason)
	if err != nil || !next.Config.Enabled || next.Binding.IP == a.route.Binding.IP || a.attempts >= a.route.Config.MaxAttempts {
		return false
	}
	a.route = next
	return true
}

// Buffer only creation metadata. Once any output is forwarded the request is
// never replayed, including tool calls and reasoning deltas.
func (r *WsResponse) ReadStream(callback func([]byte) bool) error {
	a := r.ipv6Attempt
	if a == nil {
		reason := ""
		err := r.readStream(func(payload []byte) bool {
			if r.conn != nil && r.conn.fixedManager != nil {
				reason = egressipv6.FrameFailure(payload, r.conn.fixedRoute.Pool.Retry5xx)
				if reason == "http_429" && !r.conn.fixedRoute.Pool.Rotate429 {
					reason = ""
				}
			}
			return callback(payload)
		})
		if r.conn != nil && r.conn.fixedManager != nil && reason != "" {
			route := r.conn.fixedRoute
			_ = r.conn.fixedManager.Failure(context.Background(), route.Binding.AccountID, route.Binding.Generation, route.Binding.ResourceID, reason)
		}
		return err
	}
	buffered := [][]byte{}
	size := 0
	committed := false
	reason := ""
	retry := false
	err := r.readStream(func(payload []byte) bool {
		reason = egressipv6.FrameFailure(payload, a.route.Config.Retry5xx)
		if reason != "" {
			rotated := a.rotate(reason)
			if !committed && rotated && gjson.GetBytes(payload, "response.usage.total_tokens").Int() == 0 && gjson.GetBytes(payload, "response.usage.output_tokens").Int() == 0 && gjson.GetBytes(payload, "response.usage.input_tokens").Int() == 0 {
				retry = true
				return false
			}
		}
		typ := gjson.GetBytes(payload, "type").String()
		if !committed && (typ == "response.created" || typ == "response.in_progress") && size+len(payload) <= 64*1024 {
			buffered = append(buffered, append([]byte(nil), payload...))
			size += len(payload)
			return true
		}
		committed = true
		for _, p := range buffered {
			if !callback(p) {
				return false
			}
		}
		buffered = nil
		return callback(payload)
	})
	if retry {
		r.markConnBroken()
		r.Close()
		replacement, retryErr := a.run()
		if retryErr != nil {
			return retryErr
		}
		r.mu.Lock()
		r.ipv6Replacement = replacement
		r.mu.Unlock()
		defer replacement.Close()
		return replacement.ReadStream(callback)
	}
	if err != nil && a.ctx.Err() == nil {
		r.mu.Lock()
		closed := r.closed
		r.mu.Unlock()
		if !closed {
			_, _ = a.manager.RotateAccount(a.ctx, a.accountID, a.route.Binding.IP, "stream_disconnected")
		}
	}
	return err
}
