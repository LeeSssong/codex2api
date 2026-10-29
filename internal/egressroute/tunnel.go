package egressroute

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// StartTunnel lets an external local credential executor use the same account
// dial policy as Go requests. Loopback only, per-attempt secret, HTTPS only;
// never a public/open forward proxy. Shutdown closes hijacked connections too.
func StartTunnel(dial func(context.Context, string) (net.Conn, error)) (string, func(), error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	var key [24]byte
	if _, err = rand.Read(key[:]); err != nil {
		listener.Close()
		return "", nil, err
	}
	secret := hex.EncodeToString(key[:])
	expected := "Basic " + base64.StdEncoding.EncodeToString([]byte("account:"+secret))
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	connections := map[net.Conn]bool{}
	closed := false
	track := func(c net.Conn) bool {
		mu.Lock()
		defer mu.Unlock()
		if closed {
			c.Close()
			return false
		}
		connections[c] = true
		return true
	}
	untrack := func(c net.Conn) { c.Close(); mu.Lock(); delete(connections, c); mu.Unlock() }
	server := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Proxy-Authorization")), []byte(expected)) != 1 {
			w.Header().Set("Proxy-Authenticate", "Basic realm=account")
			w.WriteHeader(407)
			return
		}
		host, port, e := net.SplitHostPort(r.Host)
		host = strings.ToLower(host)
		allowed := host == "openai.com" || strings.HasSuffix(host, ".openai.com") || host == "chatgpt.com" || strings.HasSuffix(host, ".chatgpt.com")
		if r.Method != http.MethodConnect || e != nil || port != "443" || !allowed {
			http.Error(w, "unsupported credential destination", 403)
			return
		}
		dialCtx, done := context.WithTimeout(ctx, 30*time.Second)
		target, e := dial(dialCtx, r.Host)
		done()
		if e != nil {
			http.Error(w, "account egress unavailable", 502)
			return
		}
		if !track(target) {
			return
		}
		defer untrack(target)
		client, buffer, e := w.(http.Hijacker).Hijack()
		if e != nil {
			return
		}
		if !track(client) {
			return
		}
		defer untrack(client)
		if _, e = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); e != nil {
			return
		}
		if e = buffer.Flush(); e != nil {
			return
		}
		doneCopy := make(chan struct{})
		go func() { io.Copy(target, buffer); target.Close(); close(doneCopy) }()
		io.Copy(client, target)
		client.Close()
		<-doneCopy
	})}
	go server.Serve(listener)
	var once sync.Once
	closeTunnel := func() {
		once.Do(func() {
			cancel()
			server.Close()
			mu.Lock()
			closed = true
			for c := range connections {
				c.Close()
			}
			mu.Unlock()
		})
	}
	timer := time.AfterFunc(30*time.Minute, closeTunnel)
	return "http://account:" + secret + "@" + listener.Addr().String(), func() { timer.Stop(); closeTunnel() }, nil
}
