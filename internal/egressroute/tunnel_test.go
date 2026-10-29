package egressroute

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestCredentialTunnelUsesSelectedDialer(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "bound") }))
	defer target.Close()
	called := 0
	raw, closeTunnel, err := StartTunnel(func(ctx context.Context, address string) (net.Conn, error) {
		called++
		return (&net.Dialer{}).DialContext(ctx, "tcp", target.Listener.Addr().String())
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTunnel()
	u, _ := url.Parse(raw)
	tr := target.Client().Transport.(*http.Transport).Clone()
	tr.Proxy = http.ProxyURL(u)
	tr.TLSClientConfig = tr.TLSClientConfig.Clone()
	tr.TLSClientConfig.ServerName = "example.com"
	defer tr.CloseIdleConnections()
	r, e := (&http.Client{Transport: tr}).Get("https://auth.openai.com/")
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	if called != 1 {
		t.Fatalf("dialed %d", called)
	}
}
