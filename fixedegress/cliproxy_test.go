package fixedegress

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCliProxyStartFormatsAndNoBlindRetry(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/start" || r.URL.Query().Get("type") != "3" || r.URL.Query().Get("num") != "1" || r.URL.Query().Get("port") != "10443" {
			t.Error("unexpected request", r.URL.Path)
		}
		w.Write([]byte("user:password@203.0.113.10:10443\n"))
	}))
	defer srv.Close()
	client := newCliProxyClient(srv.URL, srv.Client())
	got, err := client.Start(context.Background(), CliProxyRequest{Key: "secret", Port: 10443, Country: "US", Protocol: "http", Format: 3})
	if err != nil || len(got) != 1 || got[0].ProxyURL != "http://user:password@203.0.113.10:10443" || calls != 1 {
		t.Fatalf("got=%#v err=%v calls=%d", got, err, calls)
	}
}

func TestCliProxyErrorsNeverExposeKeyAndRedirectNotFollowed(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Redirect(w, r, "/elsewhere?key=private", 302)
	}))
	defer srv.Close()
	_, err := newCliProxyClient(srv.URL, srv.Client()).Online(context.Background(), "private", "http", 3)
	if err == nil || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), srv.URL) || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestCliProxyOnlineAcceptsExplicitFormatsRejectsUnknownAndOversize(t *testing.T) {
	for _, body := range []string{`["u:p@203.0.113.10:10443"]`, `{"data":[{"host":"203.0.113.10","port":10443,"username":"u","password":"p"}]}`, "u:p@203.0.113.10:10443\r\n"} {
		got, err := ParseCliProxyInventory([]byte(body), "socks5", 3)
		if err != nil || len(got) != 1 || !strings.HasPrefix(got[0].ProxyURL, "socks5://") {
			t.Fatalf("body=%s got=%#v err=%v", body, got, err)
		}
	}
	for _, body := range []string{`{"success":true}`, `{"error":"private"}`, `<html>bad</html>`, strings.Repeat("x", (1<<20)+1)} {
		if _, err := ParseCliProxyInventory([]byte(body), "http", 3); err == nil {
			t.Fatalf("accepted unknown/oversize inventory")
		}
	}
}

func TestCliProxyStopRequiresIndividualPort(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("port") == "" {
			t.Fatal("stop all")
		}
		w.Write([]byte("success"))
	}))
	defer srv.Close()
	c := newCliProxyClient(srv.URL, srv.Client())
	if c.Stop(context.Background(), "private", 0) == nil || calls != 0 {
		t.Fatal("stop all allowed")
	}
	if c.Stop(context.Background(), "private", 10443) != nil || calls != 1 {
		t.Fatal("individual stop failed")
	}
}

func TestNormalizeProxyLineExplicitFormatsAndPortValidation(t *testing.T) {
	cases := []struct {
		raw, protocol, want string
		format              int
	}{
		{"[2001:db8::1]:10443:u:p:a@ss", "http", "http://u:p%3Aa%40ss@[2001:db8::1]:10443", 2},
		{"u:p:a@ss@203.0.113.1:10443", "socks5", "socks5://u:p%3Aa%40ss@203.0.113.1:10443", 3},
		{"203.0.113.1:10443", "https", "https://203.0.113.1:10443", 1},
	}
	for _, tc := range cases {
		got, e := NormalizeProxyLine(tc.raw, tc.protocol, tc.format)
		if e != nil || got != tc.want {
			t.Fatalf("got=%s err=%v want=%s", got, e, tc.want)
		}
	}
	for _, raw := range []string{"host:0", "host:65536", "host:abc", "host:443/path", "host:443?key=foo"} {
		if _, e := NormalizeProxyLine(raw, "http", 1); e == nil {
			t.Fatal("accepted invalid address")
		}
	}
}

func TestCliProxyManagementBaseValidation(t *testing.T) {
	for _, base := range []string{"http://ipapi.cliproxy.com", "https://127.0.0.1", "https://user:password@example.com", "https://example.com?key=private", "https://example.com/path"} {
		if _, err := NewCliProxyClient(base); err == nil {
			t.Fatal("accepted unsafe provider base")
		}
	}
	if _, err := NewCliProxyClient("https://ipapi.cliproxy.com"); err != nil {
		t.Fatal(err)
	}
}

func TestCliProxyRejectsProtocolFormatMismatchAndFalseStopSuccess(t *testing.T) {
	for _, raw := range []string{"socks5://u:p@203.0.113.1:10443", "http://u:p@203.0.113.1:10443"} {
		if _, e := NormalizeProxyLine(raw, "http", 1); e == nil {
			t.Fatal("accepted scheme/format mismatch")
		}
	}
	for _, body := range []string{`{"code":0,"error":"failed"}`, `{"code":200,"success":false}`, `{"code":200}`, `{"success":true,"error":"failed"}`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
		err := newCliProxyClient(srv.URL, srv.Client()).Stop(context.Background(), "key", 10443)
		srv.Close()
		if err == nil {
			t.Fatalf("accepted false/ambiguous stop result %s", body)
		}
	}
}
