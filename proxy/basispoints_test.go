package proxy

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/codex2api/auth"
	"github.com/codex2api/internal/basispoints"
	"github.com/tidwall/gjson"
)

func enableBasispointsForTest(t *testing.T) {
	t.Helper()
	previous := CurrentRuntimeSettings()
	t.Cleanup(func() { ApplyRuntimeSettings(previous) })
	settings := DefaultRuntimeSettings()
	settings.CodexBasispointsEnabled = true
	settings.CodexForceWebsocket = true
	ApplyRuntimeSettings(settings)
	t.Setenv("CODEX_TRANSPORT_MODE", "standard")
}

func TestBasispointsRoutesEveryPoolAccountAndOverridesWebsocket(t *testing.T) {
	enableBasispointsForTest(t)
	previous := WebsocketExecuteFunc
	t.Cleanup(func() { WebsocketExecuteFunc = previous })
	WebsocketExecuteFunc = nil
	if NewHandler(nil, nil, nil, nil).shouldUseWebsocketForHTTP() {
		t.Fatal("Basispoints must take precedence over forced WebSocket")
	}
	for _, id := range []int64{91231, 91232} {
		account := &auth.Account{DBID: id, AccountID: "workspace", AccessToken: "test-token", CustomHeaders: map[string]string{"Content-Encoding": "zstd", "X-Codex-Turn-State": "old-codex-state"}}
		called := false
		installBasispointsTransport(t, account, func(req *http.Request) (*http.Response, error) {
			called = true
			if req.URL.String() != basispoints.ResponsesURL || req.Method != http.MethodPost {
				t.Fatalf("unexpected destination: %s %s", req.Method, req.URL)
			}
			for key, want := range map[string]string{"Authorization": "Bearer test-token", "Chatgpt-Account-Id": "workspace", "X-OpenAI-Account-Id": "workspace", "X-Basispoints-Auth-Mode": "chatgpt", "Content-Encoding": "", "X-Codex-Turn-State": ""} {
				if got := req.Header.Get(key); got != want {
					t.Errorf("%s = %q, want %q", key, got, want)
				}
			}
			body, _ := io.ReadAll(req.Body)
			if gjson.GetBytes(body, "reasoning_effort").String() != "xhigh" || gjson.GetBytes(body, "model").String() != "gpt-6-astra" || gjson.GetBytes(body, "tools").Exists() {
				t.Fatalf("wrong wire body: %s", body)
			}
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"output\":[],\"usage\":{\"total_tokens\":5}}}\n\n"))}, nil
		})
		// Exercise the normal preparation layer, including default tool injection policy.
		body, _ := PrepareResponsesBody([]byte(`{"model":"gpt-6-astra","input":"hello","reasoning":{"effort":"max"}}`))
		resp, err := ExecuteRequest(context.Background(), account, body, "session", "", "client-key", nil, nil, true)
		if err != nil {
			t.Fatal(err)
		}
		output, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil || !called || !strings.Contains(string(output), `"effort":"xhigh"`) || !strings.Contains(string(output), `"total_tokens":5`) {
			t.Fatalf("invalid response: %s, called=%v err=%v", output, called, err)
		}
		if effectiveReasoningEffortForAccount(account, "max") != "xhigh" {
			t.Fatal("usage must report effective effort")
		}
	}
}

func TestBasispointsDoesNotRequireCodexTurnState(t *testing.T) {
	enableBasispointsForTest(t)
	previous := ipv6StateProvider.Load()
	t.Cleanup(func() { SetIPv6StateProvider(previous) })
	SetIPv6StateProvider(&IPv6StateProvider{EligibleAccounts: func(string) (bool, map[int64]bool) {
		t.Fatal("Basispoints must not require a Codex-only state capture")
		return true, nil
	}})
	filter := withRequiredStateFilter("gpt-5.6-sol", func(account *auth.Account) bool { return account.ID() == 7 })
	if !filter(&auth.Account{DBID: 7}) || filter(&auth.Account{DBID: 8}) {
		t.Fatal("Basispoints must preserve the caller's account filter")
	}
}

func TestBasispointsDisabledRestoresCodexRoute(t *testing.T) {
	enableBasispointsForTest(t)
	settings := CurrentRuntimeSettings()
	settings.CodexBasispointsEnabled = false
	settings.CodexForceWebsocket = false
	settings.CodexRequestCompression = false
	ApplyRuntimeSettings(settings)
	account := &auth.Account{DBID: 91233, AccountID: "workspace", AccessToken: "test-token"}
	installClaudeBoundaryTransport(t, account, func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != CodexBaseURL+"/responses" || req.Header.Get("X-Basispoints-Auth-Mode") != "" {
			t.Fatalf("disabled switch did not restore Codex: %s", req.URL)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("done"))}, nil
	})
	resp, err := ExecuteRequest(context.Background(), account, []byte(`{"model":"gpt-5.6-sol","input":[]}`), "", "", "", nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if effectiveReasoningEffortForAccount(account, "max") != "max" {
		t.Fatal("disabled switch changed native effort")
	}
}

func TestBasispointsErrorsAndCompactUseSameEndpoint(t *testing.T) {
	enableBasispointsForTest(t)
	account := &auth.Account{DBID: 91234, AccountID: "workspace", AccessToken: "test-token"}
	for _, status := range []int{401, 403, 429, 500, 200} {
		installBasispointsTransport(t, account, func(req *http.Request) (*http.Response, error) {
			if req.URL.String() != basispoints.ResponsesURL {
				t.Fatalf("compact escaped Basispoints: %s", req.URL)
			}
			body, _ := io.ReadAll(req.Body)
			if gjson.GetBytes(body, "input.@reverse.0.type").String() != "compaction_trigger" {
				t.Fatalf("compact trigger missing: %s", body)
			}
			payload := `{"error":{"message":"upstream rejected request"}}`
			if status == 200 {
				payload = "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_compact\",\"output\":[{\"type\":\"compaction\",\"encrypted_content\":\"encrypted\"}]}}\n\n"
			}
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}, nil
		})
		resp, err := ExecuteCompactRequest(context.Background(), account, []byte(`{"model":"gpt-5.6-sol","input":"summarize"}`), "", "", "key", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil || resp.StatusCode != status || !gjson.ValidBytes(body) {
			t.Fatalf("compact status=%d body=%s error=%v", resp.StatusCode, body, err)
		}
		if status != 200 && gjson.GetBytes(body, "error.message").String() != "upstream rejected request" {
			t.Fatal("upstream errors must remain intact for account handling")
		}
	}
}

func installBasispointsTransport(t *testing.T, account *auth.Account, transport claudeBoundaryRoundTripper) {
	t.Helper()
	key := clientPoolKey(account, "", basispointsTransportMode)
	previous, existed := clientPool.Load(key)
	clientPool.Store(key, &poolEntry{client: &http.Client{Transport: transport}})
	t.Cleanup(func() {
		if existed {
			clientPool.Store(key, previous)
		} else {
			clientPool.Delete(key)
		}
	})
}

func TestBasispointsTransportIsIsolatedAndHonorsProxy(t *testing.T) {
	account := &auth.Account{DBID: 91235}
	const proxyURL = "http://127.0.0.1:9000"
	t.Cleanup(func() { recycleBasispointsClient(account, proxyURL) })
	client, err := getBasispointsClient(account, proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	transport := client.Transport.(*http.Transport)
	req, _ := http.NewRequest(http.MethodPost, basispoints.ResponsesURL, nil)
	proxy, err := transport.Proxy(req)
	if err != nil || proxy.String() != proxyURL || client.Timeout != 0 || transport.ResponseHeaderTimeout != 0 || transport.TLSHandshakeTimeout != 0 {
		t.Fatalf("invalid Basispoints transport: proxy=%v err=%v", proxy, err)
	}
	if _, exists := clientPool.Load(clientPoolKey(account, proxyURL, codexTransportModeFromEnv())); exists {
		t.Fatal("Basispoints must not share the Codex transport pool")
	}
	if transport.TLSNextProto["h2"] != nil {
		t.Fatal("Codex HTTP/2 PING transport must not be installed")
	}
}

func TestBasispointsRejectsOtherCredentialTypes(t *testing.T) {
	enableBasispointsForTest(t)
	account := &auth.Account{UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: "https://relay.example", APIKey: "sk-test"}
	if _, err := ExecuteRequest(context.Background(), account, []byte(`{}`), "", "", "", nil, nil); err == nil {
		t.Fatal("relay credentials must not reach Basispoints")
	}
	if effectiveReasoningEffortForAccount(account, "max") != "max" {
		t.Fatal("relay reasoning effort changed")
	}
	if _, err := ExecuteRequest(context.Background(), &auth.Account{AccessToken: "token"}, []byte(`{}`), "", "", "", nil, nil); err == nil {
		t.Fatal("missing ChatGPT account ID must be rejected")
	}
}
