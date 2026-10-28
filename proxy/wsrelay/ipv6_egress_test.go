package wsrelay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/codex2api/egressipv6"
	"github.com/codex2api/proxy"
	"github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
)

func TestIPv6WebsocketRetriesOnlyBeforeOutput(t *testing.T) {
	for _, outputFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "pre-output", true: "after-output"}[outputFirst], func(t *testing.T) {
			ctx := context.Background()
			db, err := database.New("sqlite", filepath.Join(t.TempDir(), "ipv6.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			id, err := db.InsertAccountWithCredentials(ctx, "ipv6-ws-fixture", map[string]interface{}{"access_token": "fixture", "ipv6_egress_enabled": true}, "")
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := db.IPv6EgressConfig(ctx)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Enabled = true
			if err = db.SaveIPv6EgressConfig(ctx, cfg); err != nil {
				t.Fatal(err)
			}
			routes := egressipv6.New(db)
			routes.LocalIPs = func() ([]string, error) { return []string{"2604::1", "2604::2", "2604::3"}, nil }
			route, err := routes.Acquire(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				conn, err := (&websocket.Upgrader{}).Upgrade(w, req, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				if _, _, err = conn.ReadMessage(); err != nil {
					return
				}
				n := calls.Add(1)
				frames := []string{`{"type":"response.created"}`}
				if outputFirst || n > 1 {
					frames = append(frames, `{"type":"response.output_text.delta","delta":"hello"}`)
				}
				if n == 1 {
					frames = append(frames, `{"type":"response.failed","response":{"error":{"code":"server_error"}}}`)
				} else {
					frames = append(frames, `{"type":"response.completed","response":{"id":"fixture-response"}}`)
				}
				for _, frame := range frames {
					if err = conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
						return
					}
				}
				_, _, _ = conn.ReadMessage()
			}))
			defer server.Close()
			manager := NewManager()
			defer manager.Stop()
			account := &auth.Account{DBID: id, IPv6EgressEnabled: true, IPv6EgressConfigured: true}
			wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
			attempt := &ipv6WSAttempt{ctx: ctx, manager: routes, accountID: id, route: route}
			seenIPs := []string{}
			attempt.execute = func(ip string) (*WsResponse, error) {
				seenIPs = append(seenIPs, ip)
				wc, pr, err := manager.AcquireConnection(ctx, account, wsURL, ip, http.Header{}, "")
				if err != nil {
					return nil, err
				}
				if err = NewExecutorWithManager(manager).sendRequest(wc, []byte(`{"type":"response.create"}`), pr.RequestID); err != nil {
					return nil, err
				}
				return &WsResponse{conn: wc, pendingReq: pr, manager: manager, sessionID: ip}, nil
			}
			resp, err := attempt.run()
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Close()
			types := []string{}
			if err = resp.ReadStream(func(payload []byte) bool {
				types = append(types, gjson.GetBytes(payload, "type").String())
				return true
			}); err != nil {
				t.Fatal(err)
			}
			if outputFirst {
				if len(seenIPs) != 1 || types[len(types)-1] != "response.failed" {
					t.Fatalf("replayed output: %v %v", seenIPs, types)
				}
			} else {
				if len(seenIPs) != 2 || seenIPs[0] == seenIPs[1] || strings.Join(types, ",") != "response.created,response.output_text.delta,response.completed" {
					t.Fatalf("retry output: %v %v", seenIPs, types)
				}
			}
		})
	}
}

func TestStateDirectRouteBypassesOptionalIPv6Egress(t *testing.T) {
	ctx := context.Background()
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "state-direct.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg, err := db.IPv6EgressConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Enabled = true
	if err := db.SaveIPv6EgressConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	oldManager := egressipv6.Current()
	routes := egressipv6.New(db)
	calls := 0
	routes.LocalIPs = func() ([]string, error) { calls++; return []string{"2604::1"}, nil }
	egressipv6.Install(routes)
	t.Cleanup(func() { egressipv6.Install(oldManager) })
	executor := NewExecutorWithManager(NewManager())
	defer executor.manager.Stop()
	// An absent access token stops before dialing; the enabled IPv6 wrapper
	// would otherwise query/claim its source before reaching that validation.
	_, err = executor.ExecuteRequestViaWebsocket(ctx, &auth.Account{DBID: 778812},
		[]byte("{}"), "", "direct://ipv6-state", "", nil, http.Header{}, "")
	if err == nil || calls != 0 {
		t.Fatalf("State route entered dedicated IPv6 selection: calls=%d error=%v", calls, err)
	}
	route := proxy.ResolveCodexRequestEgress(ctx, &auth.Account{DBID: 778812}, "wss://chatgpt.com/backend-api/codex/responses", "direct://ipv6-state", true)
	if route.Err != nil || route.DialProxyURL != "" || calls != 0 {
		t.Fatalf("State direct egress changed: %+v calls=%d", route, calls)
	}
}

func TestIPv6WebsocketLeaseRetiresOnlyWhenConnectionCloses(t *testing.T) {
	ctx := context.Background()
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "lease.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id, err := db.InsertAccountWithCredentials(ctx, "ws-lease", map[string]interface{}{"access_token": "fixture", "ipv6_egress_enabled": true}, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := db.IPv6EgressConfig(ctx)
	cfg.Enabled = true
	if err = db.SaveIPv6EgressConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	routes := egressipv6.New(db)
	routes.LocalIPs = func() ([]string, error) { return []string{"2604::1"}, nil }
	previous := egressipv6.Current()
	egressipv6.Install(routes)
	defer egressipv6.Install(previous)
	route, release, err := routes.AcquireAccount(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	release()
	manager := NewManager()
	defer manager.Stop()
	account := &auth.Account{DBID: id, IPv6EgressEnabled: true, IPv6EgressConfigured: true}
	input := connectionDialRequest{account: account, url: "ws://127.0.0.1:1", proxyOverride: "local-ipv6://" + route.Binding.IP, headers: http.Header{}}
	// The real TCP6 source bind cannot dial this IPv4 fixture. Failure must still
	// clean up its durable lease and leave no reservation behind.
	if _, err = manager.createConnection(ctx, input); err == nil {
		t.Fatal("unexpected public source bind success")
	}
	if err = db.UpdateCredentials(ctx, id, map[string]interface{}{"ipv6_egress_enabled": false}); err != nil {
		t.Fatal(err)
	}
	if err = routes.RetireAccount(ctx, id); err != nil {
		t.Fatal(err)
	}
	bindings, _, err := db.IPv6EgressStatus(ctx, time.Now().Unix())
	if err != nil || len(bindings) != 0 {
		t.Fatalf("failed handshake leaked lease: %+v %v", bindings, err)
	}
	// Closing a live connection invokes its lease release once.
	db.UpdateCredentials(ctx, id, map[string]interface{}{"ipv6_egress_enabled": true})
	routes.LocalIPs = func() ([]string, error) { return []string{"2604::2"}, nil }
	_, release, err = routes.AcquireAccount(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	wc := NewWsConnection(nil, nil, "")
	wc.ipv6Release = release
	db.UpdateCredentials(ctx, id, map[string]interface{}{"ipv6_egress_enabled": false})
	routes.RetireAccount(ctx, id)
	bindings, _, _ = db.IPv6EgressStatus(ctx, time.Now().Unix())
	if len(bindings) != 1 {
		t.Fatal("retired before connection close")
	}
	wc.Close()
	wc.Close()
	bindings, _, _ = db.IPv6EgressStatus(ctx, time.Now().Unix())
	if len(bindings) != 0 {
		t.Fatal("connection close failed to release lease")
	}
}
