package wsrelay

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/codex2api/auth"
	"github.com/codex2api/proxy"
	"github.com/tidwall/gjson"
)

// Exercise both the native executor and the actual WS handshake/frame pipeline.
func TestIdentityRepairWebsocketWireIsolationAndInjection(t *testing.T) {
	t.Setenv("CODEX_IDENTITY_STORE", "memory")
	t.Setenv("CODEX_PERSONA_STORE", "memory")
	t.Setenv("CODEX_TELEMETRY_ENABLED", "false")
	f := newLiteModeFixture(t, true)
	f.account.CodexFingerprintMode = auth.CodexFingerprintModeSingleMachineMultiWindow
	f.account.CodexTurnState = "configured-state"
	f.account.CodexTurnStateModels = "gpt-*"
	previous := proxy.WebsocketExecuteFunc
	t.Cleanup(func() { proxy.WebsocketExecuteFunc = previous })
	proxy.WebsocketExecuteFunc = func(ctx context.Context, a *auth.Account, b []byte, s, p, k string, d *proxy.DeviceProfileConfig, h http.Header, pool string) (*http.Response, error) {
		response, err := f.executor.ExecuteRequestViaWebsocket(ctx, a, b, s, p, k, d, h, pool)
		if err != nil {
			return nil, err
		}
		return websocketResponseToHTTP(ctx, response, http.StatusOK, nil), nil
	}
	frames := make([]liteModeObservation, 0, 3)
	for _, key := range []string{"isolation-tenant-a", "isolation-tenant-b", "isolation-tenant-a"} {
		response, err := proxy.ExecuteRequest(f.ctx, f.account, []byte(`{"model":"gpt-5.6-sol","input":"same prompt","client_metadata":{}}`), "", "", key, nil, nil, true)
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		frame := f.observation(t)
		if frame.headers.Get("X-Codex-Turn-State") != "configured-state" || gjson.GetBytes(frame.body, "client_metadata.x-codex-turn-state").String() != "configured-state" {
			t.Fatalf("handshake/frame injection mismatch: %v %s", frame.headers, frame.body)
		}
		if frame.headers.Get("Thread-Id") != gjson.GetBytes(frame.body, "prompt_cache_key").String() {
			t.Fatal("wire cache and thread disagree")
		}
		frames = append(frames, frame)
	}
	if frames[0].socket == frames[1].socket {
		t.Fatal("different API keys shared a socket")
	}
	if frames[0].socket != frames[2].socket {
		t.Fatal("same-key stable thread did not reuse its socket")
	}
}
