package proxy

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"github.com/codex2api/auth"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

func TestAccountEgressTunnelNeverFallsBackOrChangesProxy(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(502) }))
	defer upstream.Close()
	account := &auth.Account{DBID: 987654, ProxyURL: upstream.URL}
	tunnel, closeTunnel, err := StartAccountEgressTunnel(account)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTunnel()
	u, _ := url.Parse(tunnel)
	attempt := func() {
		conn, err := net.DialTimeout("tcp", u.Host, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		pass, _ := u.User.Password()
		fmt.Fprintf(conn, "CONNECT auth.openai.com:443 HTTP/1.1\r\nHost: auth.openai.com:443\r\nProxy-Authorization: Basic %s\r\n\r\n", base64.StdEncoding.EncodeToString([]byte(u.User.Username()+":"+pass)))
		response, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 502 {
			t.Fatalf("unavailable route bypassed: %d", response.StatusCode)
		}
	}
	attempt()
	if calls.Load() != 1 {
		t.Fatalf("proxy attempts=%d", calls.Load())
	}
	account.Mu().Lock()
	account.ProxyURL = ""
	account.Mu().Unlock()
	attempt()
	if calls.Load() != 1 {
		t.Fatal("route changed mid-session")
	}
}
