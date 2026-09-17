package statepool

import (
	"crypto/rand"
	"errors"
	"math/big"
	"net/url"
	"regexp"
	"strings"

	"github.com/codex2api/security"
)

var ipdeepSession = regexp.MustCompile(`-session-([A-Za-z0-9]+)(-|$)`)

// Only rewrite the vendor-specific session segment, never generic credentials.
func SupportsSessionRotation(raw string) bool {
	u, err := security.ParseProxyURL(raw)
	if err != nil || u.User == nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return (host == "ipdeep.com" || strings.HasSuffix(host, ".ipdeep.com")) && ipdeepSession.MatchString(u.User.Username())
}

func newProxySession() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(9000000000))
	if err != nil {
		return "", errors.New("proxy_session_generation_failed")
	}
	return n.Add(n, big.NewInt(1000000000)).String(), nil
}

func proxyWithSession(raw, session string) (string, error) {
	if !SupportsSessionRotation(raw) || len(session) != 10 || strings.Trim(session, "0123456789") != "" {
		return "", errors.New("unsupported_proxy_session")
	}
	u, _ := security.ParseProxyURL(raw)
	username := u.User.Username()
	match := ipdeepSession.FindStringSubmatchIndex(username)
	username = username[:match[2]] + session + username[match[3]:]
	if password, ok := u.User.Password(); ok {
		u.User = url.UserPassword(username, password)
	} else {
		u.User = url.User(username)
	}
	return u.String(), nil
}

func routeReservationKey(ref ProxyRef, proxyURL string) string {
	// Generated sessions must still share the base gateway's concurrency budget.
	if ref.Dynamic || ref.RotateSession {
		return ref.URLHash
	}
	if ref.LastTestIP != "" {
		return Hash("ip:" + ref.LastTestIP)
	}
	return Hash(proxyURL)
}
