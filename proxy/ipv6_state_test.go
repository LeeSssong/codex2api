package proxy

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/codex2api/auth"
	"github.com/tidwall/gjson"
)

func TestIPv6PluginOverridesBothStateLocationsAndClearsMissingState(t *testing.T) {
	account := &auth.Account{DBID: 7, ProxyURL: "http://business.invalid:8080"}
	value := "fixed-state"
	SetIPv6StateProvider(&IPv6StateProvider{
		Applies: func(a *auth.Account, model string) bool { return a == account && model == "gpt-5.6-sol" },
		Resolve: func(a *auth.Account, model string) (string, bool, error) {
			return value, a == account && model == "gpt-5.6-sol", nil
		},
	})
	t.Cleanup(func() { SetIPv6StateProvider(nil) })
	body := []byte(`{"model":"gpt-5.6-sol","reasoning":{"effort":"low"},"client_metadata":{"x-codex-turn-state":"client-state","other":"keep"}}`)
	headers := http.Header{"X-Codex-Turn-State": {"old-state"}, "x-codex-turn-state": {"noncanonical-state"}}
	updated, outgoing, err := applyVerifiedState(context.Background(), account, body, headers, "")
	if err != nil || outgoing.Get(codexTurnStateHeader) != value || gjson.GetBytes(updated, "client_metadata.x-codex-turn-state").String() != value {
		t.Fatal("state did not override both transports")
	}
	if _, exists := outgoing["x-codex-turn-state"]; exists || gjson.GetBytes(updated, "client_metadata.other").String() != "keep" || headers.Get(codexTurnStateHeader) != "old-state" {
		t.Fatal("header cloning or metadata preservation failed")
	}
	if !ipv6StateDirect(context.Background(), account, body) || ipv6StateDirect(WithoutStatePool(context.Background()), account, body) {
		t.Fatal("direct route scope or capture bypass failed")
	}
	value = ""
	updated, outgoing, err = applyVerifiedState(context.Background(), account, body, headers, "")
	if err != nil || outgoing.Get(codexTurnStateHeader) != "" || gjson.GetBytes(updated, "client_metadata.x-codex-turn-state").Exists() {
		t.Fatal("missing state forwarded a client token")
	}
	SetIPv6StateProvider(nil)
	updated, outgoing, _, err = applyIPv6State(context.Background(), account, body, headers)
	if err != nil || string(updated) != string(body) || outgoing.Get(codexTurnStateHeader) != "old-state" {
		t.Fatal("disabled plugin changed the request")
	}
}

func TestIPv6PluginDirectRouteDoesNotUseAccountProxy(t *testing.T) {
	account := &auth.Account{DBID: 7, ProxyURL: "http://business.invalid:8080"}
	httpEgress := ResolveCodexEgress(account, "https://chatgpt.com/backend-api/codex/responses", ipv6StateDirectRoute)
	wsEgress := ResolveCodexWebsocketEgress(account, "wss://chatgpt.com/backend-api/codex/responses", ipv6StateDirectRoute)
	for _, egress := range []CodexEgress{httpEgress, wsEgress} {
		if egress.Kind != CodexEgressDirect || egress.DialProxyURL != "" || egress.ProxyURL != "" {
			t.Fatal("plugin traffic retained a configured proxy")
		}
	}
	if CodexDialProxyURL(account, ipv6StateDirectRoute) != "" {
		t.Fatal("WebSocket socket retained a configured proxy")
	}
}

func TestIPv6PluginGuardsMetadataCaseVariants(t *testing.T) {
	SetIPv6StateProvider(&IPv6StateProvider{
		Guard: func(_ *auth.Account, _, state string) error {
			if state == "other-owner-state" {
				return errors.New("wrong owner")
			}
			return nil
		},
		Resolve: func(*auth.Account, string) (string, bool, error) {
			t.Fatal("resolve called before owner rejection")
			return "", false, nil
		},
	})
	t.Cleanup(func() { SetIPv6StateProvider(nil) })
	body := []byte(`{"model":"gpt-5.6-sol","client_metadata":{"X-Codex-Turn-State":"other-owner-state"}}`)
	if _, _, _, err := applyIPv6State(context.Background(), &auth.Account{}, body, nil); err == nil {
		t.Fatal("mixed-case metadata bypassed owner guard")
	}
}
