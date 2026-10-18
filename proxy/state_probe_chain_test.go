package proxy

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func probeTestProxy(t *testing.T, user, target, destination string, seen chan<- string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect || r.Host != target || r.Header.Get("Authorization") != "" ||
			r.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":password")) {
			t.Error("incorrect tunnel target or credential isolation")
			http.Error(w, "rejected", 407)
			return
		}
		upstream, err := net.Dial("tcp", destination)
		if err != nil {
			t.Error(err)
			http.Error(w, "unreachable", 502)
			return
		}
		conn, buffered, err := w.(http.Hijacker).Hijack()
		if err != nil {
			_ = upstream.Close()
			t.Error(err)
			return
		}
		seen <- user
		_, _ = fmt.Fprint(buffered, "HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = buffered.Flush()
		go func() { _, _ = io.Copy(upstream, buffered); _ = upstream.Close() }()
		_, _ = io.Copy(conn, upstream)
		_ = conn.Close()
	}))
}

func TestStateProbeForwardChainPreservesTLSAndCredentialIsolation(t *testing.T) {
	t.Setenv("CODEX_TRANSPORT_MODE", "standard")
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != "" || r.Header.Get("Authorization") != "Bearer upstream-only" {
			t.Error("proxy credentials leaked or upstream credentials lost")
		}
		_, _ = w.Write([]byte("complete"))
	}))
	defer target.Close()
	seen := make(chan string, 2)
	targetURL, _ := url.Parse(target.URL)
	inner := probeTestProxy(t, "capture-session-123", targetURL.Host, targetURL.Host, seen)
	defer inner.Close()
	outer := probeTestProxy(t, "forward", "capture.invalid:8082", strings.TrimPrefix(inner.URL, "http://"), seen)
	defer outer.Close()
	forwardURL, _ := url.Parse(outer.URL)
	forwardURL.User = url.UserPassword("forward", "password")
	ctx := WithFreshStateProbe(context.Background(), forwardURL.String())
	client, finish, err := stateProbeClient(ctx, CodexEgress{DialProxyURL: "http://capture-session-123:password@capture.invalid:8082"})
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(target.Certificate())
	client.Transport.(*http.Transport).TLSClientConfig.RootCAs = roots
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, target.URL, nil)
	req.Header.Set("Authorization", "Bearer upstream-only")
	res, err := client.Do(req)
	if err != nil {
		finish(nil)
		t.Fatal(err)
	}
	finish(res)
	body, err := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if err != nil || string(body) != "complete" || len(seen) != 2 {
		t.Fatal("both configured proxy hops were not used")
	}
}

func TestStateProbeForwardConnectCancellationAndRejection(t *testing.T) {
	started := make(chan struct{})
	released := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		close(started)
		_, _ = io.Copy(io.Discard, conn)
		close(released)
	}))
	dialer, err := stateProbeProxyChain(server.URL, "http://unreachable.invalid:8080")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		conn, err := dialer.DialContext(ctx, "tcp", "target.invalid:443")
		if conn != nil {
			_ = conn.Close()
		}
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled CONNECT returned a connection")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("CONNECT ignored cancellation")
	}
	<-released
	server.Close()
	reject := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(407) }))
	defer reject.Close()
	dialer, _ = stateProbeProxyChain(reject.URL, "http://unreachable.invalid:8080")
	if _, err := dialer.DialContext(context.Background(), "tcp", "target.invalid:443"); err == nil || err.Error() != "proxy_connect_http_407" {
		t.Fatal("proxy rejection was hidden or bypassed")
	}
}
