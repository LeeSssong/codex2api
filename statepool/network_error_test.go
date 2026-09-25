package statepool

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
)

func TestTransportFailureDoesNotExposeCredentials(t *testing.T) {
	for _, test := range []struct {
		err  error
		want string
	}{
		{&net.DNSError{Name: "secret.example", Err: "no such host"}, "dns_error"},
		{&net.OpError{Op: "dial", Err: context.DeadlineExceeded}, "connection_timeout"},
		{&net.OpError{Op: "dial", Err: errors.New("connection refused")}, "connection_failed"},
		{errors.New("proxy_connect_http_407"), "proxy_connect_http_407"},
		{errors.New("https://user:password@proxy.example TLS handshake timeout"), "tls_handshake_timeout"},
		{errors.New("https://user:password@proxy.example bad connection"), "transport_error"},
	} {
		if got := transportFailure(fmt.Errorf("upstream: %w", test.err)); got != test.want {
			t.Fatalf("category = %q, want %q", got, test.want)
		}
	}
}
