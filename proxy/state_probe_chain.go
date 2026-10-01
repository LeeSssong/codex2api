package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/codex2api/security"
	xproxy "golang.org/x/net/proxy"
)

type stateContextDialer interface {
	Dial(string, string) (net.Conn, error)
	DialContext(context.Context, string, string) (net.Conn, error)
}

type stateScopedDialer struct {
	ctx    context.Context
	dialer stateContextDialer
}

func (d *stateScopedDialer) Dial(network, address string) (net.Conn, error) {
	return d.dialer.DialContext(d.ctx, network, address)
}

func stateProbeProxyChain(forwardURL, captureURL string) (stateContextDialer, error) {
	var dialer stateContextDialer = &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	for _, raw := range []string{forwardURL, captureURL} {
		u, err := security.ParseProxyURL(raw)
		if err != nil {
			return nil, errors.New("invalid_forward_proxy_chain")
		}
		port := u.Port()
		if port == "" {
			port = "80"
			if u.Scheme == "https" {
				port = "443"
			} else if strings.HasPrefix(u.Scheme, "socks5") {
				port = "1080"
			}
		}
		address := net.JoinHostPort(u.Hostname(), port)
		switch strings.ToLower(u.Scheme) {
		case "http", "https":
			dialer = &stateConnectDialer{base: dialer, proxy: u, address: address}
		case "socks5", "socks5h":
			var credentials *xproxy.Auth
			if u.User != nil {
				password, _ := u.User.Password()
				credentials = &xproxy.Auth{User: u.User.Username(), Password: password}
			}
			socks, err := xproxy.SOCKS5("tcp", address, credentials, dialer)
			if err != nil {
				return nil, errors.New("invalid_forward_proxy_chain")
			}
			var ok bool
			dialer, ok = socks.(stateContextDialer)
			if !ok {
				return nil, errors.New("forward_proxy_cancellation_unsupported")
			}
		}
	}
	return dialer, nil
}

type stateConnectDialer struct {
	base    stateContextDialer
	proxy   *url.URL
	address string
}

func (d *stateConnectDialer) Dial(network, address string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, address)
}

func (d *stateConnectDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	conn, err := d.base.DialContext(ctx, network, d.address)
	if err != nil {
		return nil, err
	}
	raw := conn
	stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
	defer stop()
	complete := false
	defer func() {
		if !complete {
			_ = raw.Close()
		}
	}()
	if d.proxy.Scheme == "https" {
		secured := tls.Client(conn, &tls.Config{ServerName: d.proxy.Hostname(), MinVersion: tls.VersionTLS12})
		if err := secured.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		conn = secured
	}
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Host: address}, Host: address, Header: make(http.Header)}
	if d.proxy.User != nil {
		password, _ := d.proxy.User.Password()
		encoded := base64.StdEncoding.EncodeToString([]byte(d.proxy.User.Username() + ":" + password))
		req.Header.Set("Proxy-Authorization", "Basic "+encoded)
	}
	if err := req.Write(conn); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, req)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("proxy_connect_http_%d", response.StatusCode)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	complete = true
	return &bufferedConn{Conn: conn, reader: reader}, nil
}
