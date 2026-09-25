package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/codex2api/security"
)

type freshStateProbeKey struct{}
type freshStateProbeConfig struct{ forwardURL string }

// Each probe gets an independent transport without disturbing business pools.
func WithFreshStateProbe(ctx context.Context, forwardURL ...string) context.Context {
	config := freshStateProbeConfig{}
	if len(forwardURL) > 0 {
		config.forwardURL = forwardURL[0]
	}
	return context.WithValue(WithoutStatePool(ctx), freshStateProbeKey{}, config)
}

type stateProbeBody struct {
	io.ReadCloser
	release func()
	once    sync.Once
}

func (b *stateProbeBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.release)
	return err
}

func stateProbeClient(ctx context.Context, egress CodexEgress) (*http.Client, func(*http.Response), error) {
	config, fresh := ctx.Value(freshStateProbeKey{}).(freshStateProbeConfig)
	if !fresh {
		return egress.Client(), func(*http.Response) {}, nil
	}
	if egress.ViaResin() {
		return nil, nil, errors.New("state capture requires directly selectable proxy egress; Resin overrides proxy selection")
	}
	if egress.DialProxyURL != "" {
		if _, err := security.ParseProxyURL(egress.DialProxyURL); err != nil {
			return nil, nil, errors.New("invalid state probe proxy URL")
		}
	}
	transport := newCodexTransport(egress.DialProxyURL)
	if config.forwardURL != "" {
		if egress.DialProxyURL == "" {
			return nil, nil, errors.New("forward_proxy_requires_capture_proxy")
		}
		dialer, err := stateProbeProxyChain(config.forwardURL, egress.DialProxyURL)
		if err != nil {
			return nil, nil, err
		}
		switch current := transport.(type) {
		case *http.Transport:
			current.Proxy = nil
			current.DialContext = dialer.DialContext
		case *utlsRoundTripper:
			current.dialer = &stateScopedDialer{ctx: ctx, dialer: dialer}
		default:
			return nil, nil, errors.New("unsupported_forward_proxy_transport")
		}
	}
	if standard, ok := transport.(*http.Transport); ok {
		standard.ResponseHeaderTimeout = 0
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	finish := func(response *http.Response) {
		if response == nil || response.Body == nil {
			releaseEvictedClient(client)
			return
		}
		response.Body = &stateProbeBody{ReadCloser: response.Body, release: func() { releaseEvictedClient(client) }}
	}
	return client, finish, nil
}
