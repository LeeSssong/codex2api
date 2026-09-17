package statepool

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strings"
)

// Persist categories only: upstream error strings may contain authenticated URLs.
func transportFailure(err error) string {
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "dns_error"
	}
	var certificate *tls.CertificateVerificationError
	if errors.As(err, &certificate) {
		return "tls_certificate_error"
	}
	message := strings.ToLower(err.Error())
	for _, code := range []string{"proxy_connect_http_407", "proxy_connect_http_403", "proxy_connect_http_429", "proxy_connect_http_502", "proxy_connect_http_503"} {
		if strings.Contains(message, code) {
			return code
		}
	}
	if strings.Contains(message, "proxy authentication required") {
		return "proxy_connect_http_407"
	}
	if strings.Contains(message, "tls handshake timeout") {
		return "tls_handshake_timeout"
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		if op.Timeout() {
			return "connection_timeout"
		}
		return "connection_failed"
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return "network_timeout"
	}
	return "transport_error"
}
