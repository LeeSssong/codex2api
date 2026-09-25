package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/codex2api/auth"
	"github.com/codex2api/internal/basispoints"
	"github.com/gin-gonic/gin"
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

func TestBasispointsIgnoresRotatingNativeSessionIdentity(t *testing.T) {
	enableBasispointsForTest(t)
	account := &auth.Account{DBID: 91236, AccountID: "workspace", AccessToken: "test-token"}
	var bodies [][]byte
	installBasispointsTransport(t, account, func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, body)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\n"))}, nil
	})
	for _, sessionID := range []string{"native-request-one", "native-request-two"} {
		resp, err := ExecuteRequest(context.Background(), account, []byte(`{"model":"gpt-5.6-sol","input":"hello"}`), sessionID, "", "client-key", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, field := range []string{"metadata.task_id", "metadata.turn_id"} {
		if gjson.GetBytes(bodies[0], field).String() == "" || gjson.GetBytes(bodies[0], field).String() != gjson.GetBytes(bodies[1], field).String() {
			t.Fatalf("per-request native session changed %s", field)
		}
	}
	if gjson.GetBytes(bodies[0], "prompt_cache_key").Exists() {
		t.Fatal("native stateless session ID must not become a conversation cache key")
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

// Requests Basispoints cannot serve keep the original Codex channel: explicit web
// search, embedded images and structured output go to the native endpoint with
// their capabilities intact, while the default cached web_search declaration and
// ordinary client tools stay on Basispoints.
func TestBasispointsRoutesUnsupportedCapabilitiesToCodex(t *testing.T) {
	enableBasispointsForTest(t)
	settings := CurrentRuntimeSettings()
	settings.CodexForceWebsocket = false
	settings.CodexRequestCompression = false
	ApplyRuntimeSettings(settings)
	account := &auth.Account{DBID: 91250, AccountID: "workspace", AccessToken: "test-token"}
	var bpsCalls, nativeCalls int
	installBasispointsTransport(t, account, func(*http.Request) (*http.Response, error) {
		bpsCalls++
		return basispointsTestCompleted("resp_bps", nil), nil
	})
	var nativeBodies []string
	installClaudeBoundaryTransport(t, account, func(req *http.Request) (*http.Response, error) {
		nativeCalls++
		if req.URL.String() != CodexBaseURL+"/responses" || req.Header.Get("X-Basispoints-Auth-Mode") != "" || req.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("native fallback used the wrong upstream or identity: %s", req.URL)
		}
		body, _ := io.ReadAll(req.Body)
		nativeBodies = append(nativeBodies, string(body))
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\n"))}, nil
	})
	execute := func(body string) *http.Response {
		t.Helper()
		resp, err := ExecuteRequest(context.Background(), account, []byte(body), "", "", "client-key", nil, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp
	}
	native := map[string]string{
		basispoints.RouteWebSearch:       `{"model":"gpt-6-astra","input":"hi","tools":[{"type":"web_search","external_web_access":true}]}`,
		basispoints.RouteImageInput:      `{"model":"gpt-6-astra","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"look"},{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}]}`,
		basispoints.RouteOutputFormat:    `{"model":"gpt-6-astra","input":"hi","text":{"format":{"type":"json_schema","name":"answer","schema":{"type":"object"}}}}`,
		basispoints.RouteImageGeneration: `{"model":"gpt-6-astra","input":"hi","tools":[{"type":"image_generation"}]}`,
	}
	for reason, body := range native {
		resp := execute(body)
		if resp.Header.Get("X-Codex2API-Upstream") != "codex" || resp.Header.Get(basispointsBypassHeader) != reason {
			t.Fatalf("%s: native fallback did not label its response: %v", reason, resp.Header)
		}
	}
	if nativeCalls != len(native) || bpsCalls != 0 {
		t.Fatalf("native=%d bps=%d", nativeCalls, bpsCalls)
	}
	for _, body := range nativeBodies {
		if strings.Contains(body, "run_officejs") {
			t.Fatal("native fallback must not carry the Basispoints tool transport")
		}
	}
	if !strings.Contains(strings.Join(nativeBodies, "\n"), `"web_search"`) || !strings.Contains(strings.Join(nativeBodies, "\n"), `data:image/png;base64,AAAA`) {
		t.Fatal("native fallback dropped the capability the request needed")
	}
	for _, body := range []string{
		`{"model":"gpt-6-astra","input":"hi","tools":[{"type":"web_search","external_web_access":false}]}`,
		`{"model":"gpt-6-astra","input":"hi","tools":[{"type":"function","name":"shell","parameters":{"type":"object"}}]}`,
	} {
		resp := execute(body)
		if resp.Header.Get("X-Codex2API-Upstream") != "basispoints" || resp.Header.Get(basispointsBypassHeader) != "" {
			t.Fatalf("Basispoints-capable request left the pool route: %v", resp.Header)
		}
	}
	if bpsCalls != 2 || nativeCalls != len(native) {
		t.Fatalf("native=%d bps=%d after Basispoints-capable requests", nativeCalls, bpsCalls)
	}
	// Codex CLI shapes pass through ordinary ingress preparation, which whitelists
	// web_search fields for the Codex upstream: the cached default must survive it
	// and stay on Basispoints, while live/indexed search reaches Codex without the
	// routing-only field.
	cached, _ := PrepareResponsesBody([]byte(`{"model":"gpt-6-astra","input":"hi","tools":[{"type":"web_search","external_web_access":false,"search_content_types":["text"]}]}`))
	if resp := execute(string(cached)); resp.Header.Get("X-Codex2API-Upstream") != "basispoints" {
		t.Fatalf("prepared cached web_search declaration left Basispoints: %v", resp.Header)
	}
	live, _ := PrepareResponsesBody([]byte(`{"model":"gpt-6-astra","input":"hi","tools":[{"type":"web_search","external_web_access":true,"indexed_web_access":true}]}`))
	if resp := execute(string(live)); resp.Header.Get(basispointsBypassHeader) != basispoints.RouteWebSearch {
		t.Fatalf("prepared live web_search declaration stayed on Basispoints: %v", resp.Header)
	}
	sent := nativeBodies[len(nativeBodies)-1]
	if !strings.Contains(sent, `"web_search"`) || strings.Contains(sent, "external_web_access") || strings.Contains(sent, "indexed_web_access") {
		t.Fatalf("native fallback changed the Codex web_search wire shape: %s", sent)
	}
}

func TestWebSearchNormalizationKeepsRoutingFieldOnlyUnderBasispoints(t *testing.T) {
	tool := map[string]any{"type": "web_search_preview", "external_web_access": false, "indexed_web_access": true, "search_context_size": "low"}
	if got := normalizeCodexWebSearchTool(tool); got["external_web_access"] != nil || got["indexed_web_access"] != nil || got["search_context_size"] != "low" || got["type"] != "web_search" {
		t.Fatalf("native normalization changed: %v", got)
	}
	enableBasispointsForTest(t)
	if got := normalizeCodexWebSearchTool(tool); got["external_web_access"] != false || got["indexed_web_access"] != nil || got["search_context_size"] != "low" {
		t.Fatalf("Basispoints normalization lost the routing field: %v", got)
	}
}

func TestBasispointsNativeFallbackCanBeDisabled(t *testing.T) {
	enableBasispointsForTest(t)
	t.Setenv(basispointsNativeFallbackEnv, "off")
	account := &auth.Account{DBID: 91251, AccountID: "workspace", AccessToken: "test-token"}
	installClaudeBoundaryTransport(t, account, func(*http.Request) (*http.Response, error) {
		t.Fatal("disabled fallback must not reach the Codex channel")
		return nil, nil
	})
	installBasispointsTransport(t, account, func(*http.Request) (*http.Response, error) {
		t.Fatal("embedded images must fail locally when the fallback is off")
		return nil, nil
	})
	// Without an image host, an embedded image cannot become an HTTPS link, so the
	// rejection explains the hosting prerequisite instead of the image format.
	SetBasispointsImageHost(nil)
	_, err := ExecuteRequest(context.Background(), account, []byte(`{"model":"gpt-6-astra","input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}]}`), "", "", "client-key", nil, nil, false)
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != ErrorCodeBasispointsInvalidRequest || typed.HTTPStatus != 400 {
		t.Fatalf("expected a local Basispoints rejection: %v", err)
	}
	if !strings.Contains(typed.Message, "图片托管不可用") || !strings.Contains(typed.Message, "IMAGE_ASSET_PUBLIC_BASE_URL") || !strings.Contains(typed.Message, "image host is not configured") {
		t.Fatalf("image rejection lacks the Chinese explanation: %s", typed.Message)
	}
	if got := basispointsPreparationCategory(typed); got != "image_hosting" {
		t.Fatalf("category = %s", got)
	}
	// Images the proxy cannot host (a file ID) still fail as unsupported image input.
	_, err = ExecuteRequest(context.Background(), account, []byte(`{"model":"gpt-6-astra","input":[{"type":"message","role":"user","content":[{"type":"input_image","file_id":"file_1"}]}]}`), "", "", "client-key", nil, nil, false)
	if !errors.As(err, &typed) || basispointsPreparationCategory(typed) != "image_input" || !strings.Contains(typed.Message, "Basispoints 渠道无法使用这种图片") {
		t.Fatalf("file ID rejection changed: %v", err)
	}
}

func TestBasispointsCompactFollowsNativeRoute(t *testing.T) {
	enableBasispointsForTest(t)
	settings := CurrentRuntimeSettings()
	settings.CodexRequestCompression = false
	ApplyRuntimeSettings(settings)
	account := &auth.Account{DBID: 91252, AccountID: "workspace", AccessToken: "test-token"}
	installBasispointsTransport(t, account, func(*http.Request) (*http.Response, error) {
		t.Fatal("compaction of an image conversation must stay on the Codex channel")
		return nil, nil
	})
	installClaudeBoundaryTransport(t, account, func(req *http.Request) (*http.Response, error) {
		if !strings.HasPrefix(req.URL.String(), CodexBaseURL+"/responses") {
			t.Fatalf("compact fallback used the wrong upstream: %s", req.URL)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"resp_compact","output":[]}`))}, nil
	})
	resp, err := ExecuteCompactRequest(context.Background(), account, []byte(`{"model":"gpt-6-astra","input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}]}`), "", "", "key", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.Header.Get(basispointsBypassHeader) != basispoints.RouteImageInput {
		t.Fatalf("compact fallback did not label its response: %v", resp.Header)
	}
}

func TestBasispointsRelaysNativeRouteHeaders(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	response := &http.Response{Header: make(http.Header)}
	markBasispointsNativeRoute(response, basispoints.RouteWebSearch)
	relayBasispointsResponseHeaders(c, response)
	if recorder.Header().Get("X-Codex2API-Upstream") != "codex" || recorder.Header().Get(basispointsBypassHeader) != basispoints.RouteWebSearch {
		t.Fatalf("native route headers were not relayed: %v", recorder.Header())
	}
	relayBasispointsResponseHeaders(c, &http.Response{Header: make(http.Header)})
	if recorder.Header().Get("X-Codex2API-Upstream") != "" || recorder.Header().Get(basispointsBypassHeader) != "" {
		t.Fatal("plain Codex responses must not carry Basispoints headers")
	}
}

func TestBasispointsModelRejectionIsExplainedInChinese(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	(&Handler{}).sendUpstreamError(c, 403, []byte(`{"error":{"code":"basispoints_model_access_changed","message":"Model access has changed."}}`))
	message := gjson.GetBytes(recorder.Body.Bytes(), "error.message").String()
	if recorder.Code != 403 || gjson.GetBytes(recorder.Body.Bytes(), "error.code").String() != "basispoints_model_access_changed" || !strings.Contains(message, "当前模型在 Basispoints 渠道不可用") || !strings.Contains(message, "Model access has changed.") {
		t.Fatalf("model rejection lost its identity or explanation: %s", recorder.Body.String())
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
