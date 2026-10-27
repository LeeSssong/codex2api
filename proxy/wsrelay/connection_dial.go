package wsrelay

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/codex2api/auth"
	"github.com/codex2api/egressipv6"
	"github.com/codex2api/fixedegress"
	"github.com/codex2api/proxy"
	"github.com/gorilla/websocket"
)

type connectionDialRequest struct {
	account       *auth.Account
	url           string
	sessionKey    string
	headers       http.Header
	proxyOverride string
	capacity      *connectionCapacitySlot
	fixedSnapshot *fixedegress.Route
}

func (m *Manager) createConnection(ctx context.Context, input connectionDialRequest) (wc *WsConnection, err error) {
	defer func() {
		if err != nil {
			input.capacity.release()
		}
	}()
	var releaseFixed func()
	var fixedRoute fixedegress.Route
	if _, bound := proxy.AccountBoundEgress(ctx); !bound && !proxy.IsStateDirectRoute(input.proxyOverride) {
		r, enrolled, routeErr := proxy.FixedRoute(ctx, input.account)
		if routeErr != nil {
			return nil, routeErr
		}
		if enrolled {
			fixedRoute = r
			input.fixedSnapshot = &fixedRoute
			releaseFixed, err = fixedegress.Current().TrackRoute(ctx, r)
			if err != nil {
				return nil, err
			}
			defer func() {
				if err != nil {
					releaseFixed()
				}
			}()
		}
	}
	dialer, err := m.connectionDialer(ctx, input)
	if err != nil {
		return nil, err
	}
	session, poolKey := m.newDialSession(ctx, input)
	conn, resp, err := dialer.DialContext(ctx, input.url, input.headers)
	if err != nil {
		if releaseFixed != nil && ctx.Err() == nil {
			reason := "connect_failed"
			if resp != nil {
				reason = ""
				if resp.StatusCode == 407 {
					reason = "proxy_auth"
				}
			}
			if reason != "" {
				_ = fixedegress.Current().Failure(ctx, input.account.ID(), fixedRoute.Binding.Generation, fixedRoute.Binding.ResourceID, reason)
			}
		}
		m.sessions.CompareAndDelete(poolKey, session)
		session.Close()
		return nil, formatDialHandshakeError(err, resp)
	}
	logCompressionNegotiation(resp, input.account.ID())
	wc = m.wrapDialedConnection(dialedConnection{conn: conn, response: resp, session: session, poolKey: poolKey, request: input})
	wc.fixedRelease = releaseFixed
	if releaseFixed != nil {
		wc.fixedRoute = fixedRoute
		wc.fixedManager = fixedegress.Current()
	}
	input.capacity.attach(wc)
	select {
	case <-m.stopCleanup:
		m.DiscardConnection(wc)
		return nil, context.Canceled
	default:
		wc.installControlHandlers()
		wc.StartReadPump()
		return wc, nil
	}
}

func (m *Manager) connectionDialer(ctx context.Context, input connectionDialRequest) (*websocket.Dialer, error) {
	dialer := *m.dialer
	proxyURL := effectiveProxyURL(input.account, input.proxyOverride)
	dialProxy := proxy.CodexDialProxyURL(input.account, proxyURL)
	if _, bound := proxy.AccountBoundEgress(ctx); !bound && !proxy.IsStateDirectRoute(input.proxyOverride) {
		if input.fixedSnapshot != nil {
			dialProxy = input.fixedSnapshot.Binding.ProxyURL
		} else {
			r, enrolled, e := proxy.FixedRoute(ctx, input.account)
			if e != nil {
				return nil, e
			}
			if enrolled {
				dialProxy = r.Binding.ProxyURL
			}
		}
	}

	if boundProxy, ok := proxy.AccountBoundEgress(ctx); ok {
		dialProxy = strings.TrimSpace(boundProxy)
		dialer.Proxy = nil
	}
	if proxy.IsIPv6Route(dialProxy) {
		sourceDialer, err := egressipv6.NewDialer(proxy.IPv6RouteIP(dialProxy))
		if err != nil {
			return nil, err
		}
		dialer.Proxy = nil
		dialer.NetDialContext = sourceDialer.DialContext
		dialer.NetDialTLSContext = nil
	} else if dialProxy != "" {
		if err := configureWebsocketDialerProxy(&dialer, dialProxy); err != nil {
			return nil, err
		}
	}
	return &dialer, nil
}

func (m *Manager) newDialSession(ctx context.Context, input connectionDialRequest) (*Session, string) {
	routeKey := requestProxyKey(ctx, input.account, input.proxyOverride)
	if input.fixedSnapshot != nil {
		routeKey = fmt.Sprintf("\x00fixed|%s|%d", input.fixedSnapshot.Binding.ResourceID, input.fixedSnapshot.Binding.Generation)
	}
	poolKey := m.poolKey(input.account.ID(), input.url, input.sessionKey, routeKey)
	if value, ok := m.sessions.Load(poolKey); ok {
		value.(*Session).Close()
	}
	session := NewSession(input.account.ID(), m)
	if key := strings.TrimSpace(input.sessionKey); key != "" {
		session.ID = key
	}
	m.sessions.Store(poolKey, session)
	return session, poolKey
}

type dialedConnection struct {
	conn     *websocket.Conn
	response *http.Response
	session  *Session
	poolKey  string
	request  connectionDialRequest
}

func (m *Manager) wrapDialedConnection(input dialedConnection) *WsConnection {
	wc := NewWsConnection(input.conn, input.session, input.request.url)
	wc.account = input.request.account
	wc.PoolKey = input.poolKey
	if proxy.IsIPv6Route(input.request.proxyOverride) {
		wc.ipv6Route = input.request.proxyOverride
	}
	wc.upstreamUserAgent = strings.TrimSpace(input.request.headers.Get("User-Agent"))
	wc.upstreamUserAgentKnown = true
	wc.upstreamClientIdentity = websocketClientIdentity(input.request.headers)
	wc.upstreamVersion = strings.TrimSpace(input.request.headers.Get("Version"))
	wc.httpResp = input.response
	wc.onDisconnected = m.getOnDisconnected()
	wc.onReadFailure = m.DiscardConnection
	input.session.SetConnected(true)
	return wc
}
