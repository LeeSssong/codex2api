package egressroute

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestFallbackOnlyBeforeDispatch(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		status   int
		fallback bool
	}{
		{"unavailable", Unavailable(errors.New("no route")), 0, true},
		{"dial", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("refused")}, 0, true},
		{"uncertain write", &net.OpError{Op: "write", Net: "tcp", Err: errors.New("reset")}, 0, false},
		{"canceled", context.Canceled, 0, false},
		{"upstream failure", nil, 503, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			direct := 0
			primary := roundTripFunc(func(*http.Request) (*http.Response, error) {
				if tc.err != nil {
					return nil, tc.err
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader("upstream"))}, nil
			})
			fallback := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				direct++
				body, _ := io.ReadAll(r.Body)
				if string(body) != "payload" {
					t.Errorf("body %s", body)
				}
				return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
			})
			ctx := WithAudit(context.Background())
			req, _ := http.NewRequestWithContext(ctx, "POST", "http://fixture.invalid", strings.NewReader("payload"))
			resp, err := Fallback(primary, fallback, Observation{Kind: "account_proxy", Source: "account"}).RoundTrip(req)
			if resp != nil {
				resp.Body.Close()
			}
			if (direct == 1) != tc.fallback {
				t.Fatalf("direct=%d err=%v", direct, err)
			}
			if tc.fallback {
				got := Snapshot(ctx)
				if got.Kind != "direct" || got.FallbackReason == "" {
					t.Fatalf("audit %+v", got)
				}
			}
		})
	}
}
func TestDeadProxyFallsBackWithoutEnvironmentProxy(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "direct") }))
	defer target.Close()
	primary := roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, Unavailable(errors.New("invalid proxy")) })
	direct := http.DefaultTransport.(*http.Transport).Clone()
	direct.Proxy = nil
	defer direct.CloseIdleConnections()
	ctx := WithAudit(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", target.URL, nil)
	resp, err := Fallback(primary, direct, Observation{Kind: "account_proxy", Source: "account"}).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "direct" {
		t.Fatal(string(b))
	}
}

func TestHTTPSProxyRejectionFallsBackBeforeDispatch(t *testing.T) {
	var directCalls int
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			t.Errorf("method %s", r.Method)
		}
		w.WriteHeader(http.StatusProxyAuthRequired)
	}))
	defer proxy.Close()
	primary := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusProxyAuthRequired, Body: io.NopCloser(strings.NewReader("proxy rejected"))}, nil
	})
	direct := roundTripFunc(func(*http.Request) (*http.Response, error) {
		directCalls++
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
	})
	req, _ := http.NewRequest("POST", "https://never-resolved.invalid/token", strings.NewReader("single-use-code"))
	resp, err := Fallback(primary, direct, ProxyObservation(proxy.URL)).RoundTrip(req)
	if resp != nil {
		resp.Body.Close()
	}
	if err != nil || directCalls != 1 {
		t.Fatalf("direct=%d err=%v", directCalls, err)
	}
}

func TestProxyAuthAfterRequestWriteDoesNotReplay(t *testing.T) {
	directCalls := 0
	primary := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if trace := httptrace.ContextClientTrace(r.Context()); trace != nil {
			trace.WroteHeaders()
		}
		return &http.Response{StatusCode: http.StatusProxyAuthRequired, Body: http.NoBody}, nil
	})
	direct := roundTripFunc(func(*http.Request) (*http.Response, error) {
		directCalls++
		return nil, errors.New("unexpected replay")
	})
	req, _ := http.NewRequest("POST", "http://fixture.invalid", strings.NewReader("submitted"))
	_, _ = Fallback(primary, direct, ProxyObservation("http://proxy.invalid")).RoundTrip(req)
	if directCalls != 0 {
		t.Fatal("request was replayed after headers were written")
	}
}

func TestFallbackDoesNotReplayAfterHeadersOrStartedStream(t *testing.T) {
	for _, stream := range []bool{false, true} {
		directCalls := 0
		primary := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if trace := httptrace.ContextClientTrace(r.Context()); trace != nil {
				trace.WroteHeaders()
			}
			if stream {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(errorReader{})}, nil
			}
			return nil, Unavailable(errors.New("late failure must not replay"))
		})
		direct := roundTripFunc(func(*http.Request) (*http.Response, error) { directCalls++; return nil, errors.New("unexpected") })
		req, _ := http.NewRequest("POST", "http://fixture.invalid", strings.NewReader("once"))
		resp, _ := Fallback(primary, direct, ProxyObservation("http://proxy.invalid")).RoundTrip(req)
		if resp != nil {
			io.ReadAll(resp.Body)
			resp.Body.Close()
		}
		if directCalls != 0 {
			t.Fatal("replayed submitted request")
		}
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
