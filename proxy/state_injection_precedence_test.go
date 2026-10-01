package proxy

import (
	"context"
	"net/http"
	"testing"

	"github.com/codex2api/auth"
	"github.com/tidwall/gjson"
)

func TestManagedStateTakesPrecedenceOverManualInjection(t *testing.T) {
	account := &auth.Account{DBID: 7, CodexTurnState: "manual"}
	for _, kind := range []string{"automatic", "verified", "probe"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			body := []byte(`{"model":"gpt-5.6-sol","reasoning":{"effort":"high"},"client_metadata":{"x-codex-turn-state":"saved"}}`)
			headers := http.Header{codexTurnStateHeader: {"saved"}}
			switch kind {
			case "automatic":
				SetIPv6StateProvider(&IPv6StateProvider{Applies: func(*auth.Account, string) bool { return true }})
				t.Cleanup(func() { SetIPv6StateProvider(nil) })
			case "verified":
				SetStatePoolResolver(func(*auth.Account, string, string, string, string) (string, error) { return "saved", nil },
					func(*auth.Account, string, string, string) bool { return true })
				t.Cleanup(func() { SetStatePoolResolver(nil) })
			case "probe":
				ctx = WithFreshStateProbe(ctx)
			}
			ctx, outgoing, gotHeaders := prepareCodexTurnStateInjection(ctx, account, body, headers, true)
			applyCodexTurnStateInjectionHeader(ctx, gotHeaders)
			if gotHeaders.Get(codexTurnStateHeader) != "saved" || gjson.GetBytes(outgoing, "client_metadata.x-codex-turn-state").String() != "saved" {
				t.Fatal("manual injection replaced managed or replay state")
			}
		})
	}
}

func TestCaptureBypassDoesNotInjectManualState(t *testing.T) {
	account := &auth.Account{DBID: 7, CodexTurnState: "manual"}
	body := []byte(`{"model":"gpt-5.6-sol"}`)
	ctx, outgoing, headers := prepareCodexTurnStateInjection(WithFreshStateProbe(context.Background()), account, body, nil, false)
	if headers.Get(codexTurnStateHeader) != "" || string(outgoing) != string(body) || CodexTurnStateInjectionFromContext(ctx) != "" {
		t.Fatal("manual state contaminated a fresh capture")
	}
}
