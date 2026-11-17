package proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/codex2api/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestSingleMachineFingerprintTopologyAndCarriers(t *testing.T) {
	t.Setenv("CODEX_SESSION_HEADER_MODE", "native")
	t.Setenv("CODEX_SESSION_HEADER_ALIGN_CONVERGED", "false")
	a := fingerprintAccount(t, auth.CodexFingerprintModeSingleMachineMultiWindow)
	parent := codexClientHeaders(`{"session_id":"root","thread_id":"root"}`, "root")
	parentIDs := resolveCodexFingerprintIDs(a, parent)
	raw := `{"installation_id":"device","session_id":"root","thread_id":"child","parent_thread_id":"root","forked_from_thread_id":"root","turn_id":"unchanged-turn","turn_started_at_unix_ms":123,"subagent_kind":"worker"}`
	h := codexClientHeaders(raw, "root")
	h.Set("Thread-Id", "child")
	h.Set(codexParentThreadIDHeader, "root")
	h.Set(codexClientRequestIDHeader, "child")
	body := []byte(`{"prompt_cache_key":"tenant-cache","previous_response_id":"resp-private","client_metadata":{"installation_id":"device","session_id":"root","thread_id":"child","parent_thread_id":"root","forked_from_thread_id":"root","x-codex-window-id":"child:0"}}`)
	body, _ = sjson.SetBytes(body, "client_metadata.x-codex-turn-metadata", raw)
	rewritten := ApplyCodexFingerprintToBody(body, a, h)
	req, _ := http.NewRequest(http.MethodPost, "https://example.test/responses", nil)
	applyCodexRequestHeaders(req, a, "token", "tenant-cache", "key", nil, h)
	m := gjson.Parse(req.Header.Get(codexTurnMetadataHeader))
	bm := gjson.GetBytes(rewritten, "client_metadata")
	ids := resolveCodexFingerprintIDs(a, h)
	if ids.threadID == parentIDs.threadID || ids.sessionID != parentIDs.sessionID {
		t.Fatal("child topology lost")
	}
	for _, key := range []string{"session_id", "thread_id", "parent_thread_id", "installation_id", "forked_from_thread_id"} {
		if m.Get(key).String() != bm.Get(key).String() {
			t.Fatalf("header/body %s mismatch", key)
		}
	}
	if m.Get("parent_thread_id").String() != parentIDs.threadID || req.Header.Get(codexParentThreadIDHeader) != parentIDs.threadID {
		t.Fatal("parent reference mismatch")
	}
	if req.Header.Get("Session-Id") != ids.sessionID || req.Header.Get("Thread-Id") != ids.threadID || req.Header.Get(codexClientRequestIDHeader) != ids.threadID {
		t.Fatal("identity headers differ", req.Header)
	}
	if m.Get("turn_id").String() != "unchanged-turn" || m.Get("turn_started_at_unix_ms").Int() != 123 || m.Get("subagent_kind").String() != "worker" {
		t.Fatal("turn/real subagent metadata changed")
	}
	if gjson.GetBytes(rewritten, "prompt_cache_key").String() != "tenant-cache" || gjson.GetBytes(rewritten, "previous_response_id").String() != "resp-private" {
		t.Fatal("cache/continuation changed")
	}
	if embedded := bm.Get("x-codex-turn-metadata").String(); embedded != m.Raw {
		t.Fatalf("embedded/header mismatch %s %s", embedded, m.Raw)
	}
	// 子代理和父线程必须是不同的上游线程，父引用指向父线程改写后的 id。
	if ids.threadID == singleMachineIdentity(a.ID(), singleMachineWindowSlot(a.ID(), "child")) {
		t.Fatal("child thread was folded into a window slot")
	}
	if ids.threadID != singleMachineIdentity(a.ID(), "child") {
		t.Fatal("child thread was not derived from its own id")
	}
	// Many inbound conversations stay stable and fold onto a few outbound sessions.
	// Their threads stay distinct.
	var wg sync.WaitGroup
	seen := make(chan string, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			h := codexClientHeaders("", fmt.Sprint(i))
			a1 := resolveCodexFingerprintIDs(a, h)
			a2 := resolveCodexFingerprintIDs(a, h)
			if !reflect.DeepEqual(a1, a2) || a1.sessionID == "" || a1.threadID == "" {
				t.Errorf("unstable window %d", i)
			}
			seen <- a1.threadID
		}(i)
	}
	wg.Wait()
	close(seen)
	threads := map[string]struct{}{}
	sessions := map[string]struct{}{}
	for id := range seen {
		threads[id] = struct{}{}
	}
	for i := 0; i < 24; i++ {
		got := resolveCodexFingerprintIDs(a, codexClientHeaders("", fmt.Sprint(i)))
		sessions[got.sessionID] = struct{}{}
	}
	if len(threads) != 24 {
		t.Fatalf("outbound threads = %d, want 24 distinct threads", len(threads))
	}
	if len(sessions) == 0 || len(sessions) > singleMachineOutboundWindows {
		t.Fatalf("outbound sessions = %d, want 1..%d", len(sessions), singleMachineOutboundWindows)
	}
}

func TestSingleMachineFingerprintMissingIdentityAndBodyFallback(t *testing.T) {
	a := fingerprintAccount(t, auth.CodexFingerprintModeSingleMachineMultiWindow)
	ids := resolveCodexFingerprintIDs(a, nil)
	again := resolveCodexFingerprintIDs(a, http.Header{})
	if ids == nil || again == nil || ids.installationID == "" || ids.installationID != again.installationID {
		t.Fatal("missing client identity lost the stable installation")
	}
	if ids.sessionID != "" || ids.threadID != "" || ids.windowID != "" {
		t.Fatal("missing client identity invented a shared thread")
	}
	h := http.Header{"User-Agent": []string{"plain-sdk"}}
	body := []byte(`{"prompt_cache_key":"not-a-session","client_metadata":{"session_id":"body-session","thread_id":"body-thread"}}`)
	prepared := PrepareCodexFingerprintHeaders(a, h, body)
	if h.Get("Session-Id") != "" || prepared.Get("Session-Id") != "body-session" {
		t.Fatal("mutated caller or lost body identity")
	}
	ids = resolveCodexFingerprintIDs(a, prepared)
	if ids == nil || ids.threadID != singleMachineIdentity(a.ID(), "body-thread") {
		t.Fatal("wrong identity")
	}
	if ids.sessionID != singleMachineIdentity(a.ID(), singleMachineWindowSlot(a.ID(), "body-session")) {
		t.Fatal("session was not folded onto a window")
	}
	for _, raw := range []string{`{}`, `{"client_metadata":null}`, `{"client_metadata":{"session_id":42}}`, `{"prompt_cache_key":"not-a-session"}`} {
		got := resolveCodexFingerprintIDs(a, PrepareCodexFingerprintHeaders(a, nil, []byte(raw)))
		if got == nil || got.sessionID != "" || got.threadID != "" || got.installationID != again.installationID {
			t.Fatalf("guessed a session from %s: %+v", raw, got)
		}
		if PrepareCodexFingerprintHeaders(a, nil, []byte(raw)).Get("Session-Id") != "" {
			t.Fatalf("prepare derived a session from %s", raw)
		}
	}
	if got := ApplyCodexFingerprintToBody([]byte(`{"input":"hello"}`), a, prepared); string(got) != `{"input":"hello"}` {
		t.Fatal("invented metadata")
	}
	for _, mode := range []string{auth.CodexFingerprintModeSingleMachineMultiWindow} {
		for _, provider := range []string{auth.UpstreamOpenAIResponses, auth.UpstreamGrok} {
			relay := &auth.Account{DBID: 1, UpstreamType: provider, BaseURL: "https://relay.example.test", APIKey: "relay-key", CodexFingerprintMode: mode}
			if resolveCodexFingerprintIDs(relay, prepared) != nil {
				t.Fatalf("relay %s rewritten", provider)
			}
		}
	}
	for _, mode := range []string{auth.CodexFingerprintModeOff, auth.CodexFingerprintModeDevice, auth.CodexFingerprintModeSession, auth.CodexFingerprintModeFull} {
		outbound := http.Header{}
		ApplyCodexFingerprintHeaders(outbound, fingerprintAccount(t, mode), nil)
		if outbound.Get(codexTurnMetadataHeader) != "" || outbound.Get(codexInstallationIDHeader) != "" {
			t.Fatalf("%s invented identity headers", mode)
		}
	}
}

func TestSingleMachineFingerprintPoolIdentityIsolation(t *testing.T) {
	a := fingerprintAccount(t, auth.CodexFingerprintModeSingleMachineMultiWindow)
	h1 := codexClientHeaders("", "session-a")
	h2 := codexClientHeaders("", "session-b")
	k1 := ScopeCodexFingerprintTransportKey("api-key-pool", a, h1)
	if k1 == ScopeCodexFingerprintTransportKey("api-key-pool", a, h2) {
		t.Fatal("different windows share frozen handshake")
	}
	if k1 != ScopeCodexFingerprintTransportKey("api-key-pool", a, h1) {
		t.Fatal("pool identity unstable")
	}
	if k1 == ScopeCodexFingerprintTransportKey("another-api-key", a, h1) {
		t.Fatal("key partition lost")
	}
	h1.Set("Thread-Id", "child")
	if k1 == ScopeCodexFingerprintTransportKey("api-key-pool", a, h1) {
		t.Fatal("child and parent share handshake")
	}
	if ScopeCodexFingerprintTransportKey("api-key-pool", a, nil) != "api-key-pool" {
		t.Fatal("installation-only handshake collapsed the shared pool")
	}
	a.CodexFingerprintMode = auth.CodexFingerprintModeDevice
	if ScopeCodexFingerprintTransportKey("legacy-pool", a, h1) != "legacy-pool" {
		t.Fatal("device-only pool changed")
	}
}

func TestSingleMachineFingerprintHTTPAndCompactWire(t *testing.T) {
	t.Setenv("CODEX_SESSION_HEADER_MODE", "native")
	for _, compact := range []bool{false, true} {
		t.Run(fmt.Sprintf("compact=%v", compact), func(t *testing.T) {
			a := fingerprintAccount(t, auth.CodexFingerprintModeSingleMachineMultiWindow)
			a.AccessToken = "test-token"
			// Body-only identity: real executor must share original IDs with headers.
			body := []byte(`{"model":"gpt-5.6-codex","input":[],"prompt_cache_key":"private-cache","client_metadata":{"installation_id":"device","session_id":"root","thread_id":"child","parent_thread_id":"root"}}`)
			h := http.Header{}
			expected := resolveCodexFingerprintIDs(a, PrepareCodexFingerprintHeaders(a, h, body))
			var gotBody []byte
			var gotHeaders http.Header
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotBody = readUpstreamRequestBody(r)
				gotHeaders = r.Header.Clone()
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{}`)
			}))
			defer server.Close()
			previous := resinCfg.Load()
			t.Cleanup(func() { resinCfg.Store(previous) })
			SetResinConfig(&ResinConfig{BaseURL: server.URL, PlatformName: "test"})
			clientPool.Delete(fmt.Sprintf("resin|%d", a.ID()))
			var resp *http.Response
			var err error
			if compact {
				resp, err = ExecuteCompactRequest(context.Background(), a, body, "private-cache", "", "test-key", nil, h)
			} else {
				resp, err = ExecuteRequest(context.Background(), a, body, "private-cache", "", "test-key", nil, h, false)
			}
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if gotHeaders.Get("Session-Id") != expected.sessionID || gotHeaders.Get("Thread-Id") != expected.threadID {
				t.Fatalf("wire headers differ: %v", gotHeaders)
			}
			if gjson.GetBytes(gotBody, "client_metadata.thread_id").String() != expected.threadID {
				t.Fatalf("wire body identity differs: %s", gotBody)
			}
			if gjson.GetBytes(gotBody, "prompt_cache_key").String() != expected.threadID {
				t.Fatalf("wire cache key differs: %s", gotBody)
			}
			if len(h) != 0 {
				t.Fatal("caller headers mutated")
			}
		})
	}
}

func TestSingleMachineCacheSeedDerivesSessionWithoutTouchingCacheKey(t *testing.T) {
	a := fingerprintAccount(t, auth.CodexFingerprintModeSingleMachineMultiWindow)
	metadata := `{"turn_id":"keep-turn","turn_started_at_unix_ms":9}`
	body, err := sjson.SetBytes([]byte(`{"prompt_cache_key":"conversation-a","client_metadata":{}}`), "client_metadata.x-codex-turn-metadata", metadata)
	if err != nil {
		t.Fatal(err)
	}
	firstHeaders, firstBody := FinalizeCodexSingleMachineIdentity(a, nil, body, "conversation-a")
	secondHeaders, _ := FinalizeCodexSingleMachineIdentity(a, http.Header{}, append([]byte(nil), body...), "conversation-a")
	otherHeaders, _ := FinalizeCodexSingleMachineIdentity(a, nil, body, "conversation-b")
	session, thread := ConvergedCodexSessionIdentity(a, firstHeaders)
	repeated, _ := ConvergedCodexSessionIdentity(a, secondHeaders)
	other, _ := ConvergedCodexSessionIdentity(a, otherHeaders)
	if session == "" || session != repeated || thread == "" || session == other {
		t.Fatalf("seed mapping unstable: %s %s %s", session, repeated, other)
	}
	if gjson.GetBytes(firstBody, "prompt_cache_key").String() != thread {
		t.Fatalf("prompt_cache_key = %s, want thread %s", gjson.GetBytes(firstBody, "prompt_cache_key").String(), thread)
	}
	embedded := gjson.GetBytes(firstBody, "client_metadata.x-codex-turn-metadata").String()
	if gjson.Get(embedded, "turn_id").String() != "keep-turn" || gjson.Get(embedded, "turn_started_at_unix_ms").Int() != 9 {
		t.Fatal("turn metadata changed")
	}
	if gjson.Get(embedded, "session_id").String() != session || gjson.GetBytes(firstBody, "client_metadata.session_id").String() != session {
		t.Fatal("header and body session differ")
	}
	windowID := gjson.Get(embedded, "window_id").String()
	if windowID == "" || gjson.GetBytes(firstBody, "client_metadata.window_id").String() != windowID {
		t.Fatal("body window_id does not match turn metadata")
	}
	if gjson.Get(embedded, "installation_id").String() == "" || gjson.GetBytes(firstBody, "client_metadata.installation_id").String() == "" {
		t.Fatal("installation was not filled inside existing metadata")
	}
	_, bare := FinalizeCodexSingleMachineIdentity(a, nil, []byte(`{"prompt_cache_key":"conversation-a","input":"hi"}`), "conversation-a")
	if gjson.GetBytes(bare, "client_metadata").Exists() {
		t.Fatal("invented client_metadata")
	}
	statelessHeaders, statelessBody := FinalizeCodexSingleMachineIdentity(a, nil, []byte(`{"client_metadata":{}}`), "stateless-pool-1")
	statelessSession, statelessThread := ConvergedCodexSessionIdentity(a, statelessHeaders)
	if statelessSession == "" || statelessThread == "" {
		t.Fatal("identity-less request dropped the session")
	}
	if statelessSession == singleMachineIdentity(a.ID(), "stateless-pool-1") {
		t.Fatal("stateless connection id became a session")
	}
	againHeaders, _ := FinalizeCodexSingleMachineIdentity(a, nil, []byte(`{"client_metadata":{}}`), "stateless-pool-2")
	againSession, _ := ConvergedCodexSessionIdentity(a, againHeaders)
	if againSession != statelessSession {
		t.Fatal("identity-less requests opened another upstream session")
	}
	if strings.HasPrefix(statelessSession, "oneshot:") {
		t.Fatal("standing window was marked oneshot")
	}
	if gjson.GetBytes(statelessBody, "prompt_cache_key").String() != statelessThread {
		t.Fatal("one-shot cache key is not the thread")
	}
	outbound := http.Header{}
	ApplyCodexFingerprintHeaders(outbound, a, firstHeaders)
	if outbound.Get(codexInstallationIDHeader) != "" {
		t.Fatal("invented X-Codex-Installation-Id")
	}
	if gjson.Get(outbound.Get(codexTurnMetadataHeader), "installation_id").String() == "" || gjson.Get(outbound.Get(codexTurnMetadataHeader), "session_id").String() != session {
		t.Fatalf("turn metadata header = %s", outbound.Get(codexTurnMetadataHeader))
	}
	if outbound.Get(codexWindowIDHeader) == "" || outbound.Get(codexWindowIDHeader) != gjson.Get(outbound.Get(codexTurnMetadataHeader), "window_id").String() {
		t.Fatalf("window header = %q metadata = %s", outbound.Get(codexWindowIDHeader), outbound.Get(codexTurnMetadataHeader))
	}
	if outbound.Get(codexParentThreadIDHeader) != "" {
		t.Fatal("invented a parent thread")
	}
}

func TestSingleMachineTurnMetadataFollowsCacheKey(t *testing.T) {
	a := fingerprintAccount(t, auth.CodexFingerprintModeSingleMachineMultiWindow)
	headers := http.Header{}
	headers.Set(codexTurnMetadataHeader, `{"installation_id":"foreign-installation","session_id":"raw-session","thread_id":"raw-thread","turn_id":"keep-turn"}`)
	body := []byte(`{"prompt_cache_key":"conversation-a","client_metadata":{"note":"keep"}}`)
	outHeaders, outBody := FinalizeCodexSingleMachineIdentity(a, headers, body, "conversation-a")
	seeded, thread := ConvergedCodexSessionIdentity(a, outHeaders)
	fromCache, fromThread := ConvergedCodexSessionIdentity(a, AttachCodexFingerprintCacheSeed(a, http.Header{}, "conversation-a"))
	if seeded == "" || seeded != fromCache || thread == "" || thread != fromThread || seeded == singleMachineIdentity(a.ID(), "raw-session") {
		t.Fatalf("session = %s thread = %s cache = %s/%s", seeded, thread, fromCache, fromThread)
	}
	if gjson.GetBytes(outBody, "prompt_cache_key").String() != thread {
		t.Fatalf("prompt_cache_key = %s, want thread %s", gjson.GetBytes(outBody, "prompt_cache_key").String(), thread)
	}
	if gjson.GetBytes(outBody, "client_metadata.note").String() != "keep" {
		t.Fatal("unrelated metadata dropped")
	}
	// 出站顺序与 applyCodexRequestHeaders 相同：先透传 turn metadata，再按已确定的会话种子改写。
	outbound := http.Header{}
	outbound.Set(codexTurnMetadataHeader, outHeaders.Get(codexTurnMetadataHeader))
	ApplyCodexFingerprintHeaders(outbound, a, outHeaders)
	metadata := outbound.Get(codexTurnMetadataHeader)
	if gjson.Get(metadata, "session_id").String() != seeded || gjson.Get(metadata, "thread_id").String() != thread {
		t.Fatalf("turn metadata session = %s", metadata)
	}
	if gjson.Get(metadata, "installation_id").String() == "foreign-installation" || gjson.Get(metadata, "installation_id").String() == "" {
		t.Fatalf("installation = %s", metadata)
	}
	if gjson.Get(metadata, "turn_id").String() != "keep-turn" {
		t.Fatal("turn id changed")
	}
	if strings.Contains(metadata, "raw-session") || strings.Contains(metadata, "raw-thread") {
		t.Fatalf("foreign session survived: %s", metadata)
	}
	if got := gjson.GetBytes(outBody, "client_metadata.window_id").String(); got == "" || got != gjson.Get(metadata, "window_id").String() {
		t.Fatalf("body window_id = %q metadata = %s", got, metadata)
	}
	if outbound.Get(codexWindowIDHeader) == "" || outbound.Get(codexWindowIDHeader) != gjson.Get(outbound.Get(codexTurnMetadataHeader), "window_id").String() {
		t.Fatalf("window header = %q", outbound.Get(codexWindowIDHeader))
	}
	if outbound.Get(codexInstallationIDHeader) != "" || outbound.Get(codexParentThreadIDHeader) != "" {
		t.Fatal("invented installation or parent header")
	}

	bare := http.Header{}
	bare.Set(codexTurnMetadataHeader, `{"session_id":"raw-session","thread_id":"raw-thread","turn_id":"keep-turn"}`)
	if got := resolveCodexFingerprintIDs(a, bare); got == nil || got.sessionID != "" || got.threadID != "" {
		t.Fatalf("turn metadata seeded a session: %+v", got)
	}
	outbound = http.Header{}
	outbound.Set(codexTurnMetadataHeader, bare.Get(codexTurnMetadataHeader))
	ApplyCodexFingerprintHeaders(outbound, a, bare)
	stripped := outbound.Get(codexTurnMetadataHeader)
	if gjson.Get(stripped, "session_id").Exists() || gjson.Get(stripped, "thread_id").Exists() || gjson.Get(stripped, "window_id").Exists() {
		t.Fatalf("foreign session survived: %s", stripped)
	}
	if gjson.Get(stripped, "turn_id").String() != "keep-turn" || gjson.Get(stripped, "installation_id").String() == "" {
		t.Fatalf("metadata = %s", stripped)
	}
	if outbound.Get(codexWindowIDHeader) != "" {
		t.Fatal("window header without a session")
	}
}

func TestSingleMachineIgnoresInstallationPinAndIdentityCustomHeaders(t *testing.T) {
	account := &auth.Account{
		DBID:                 42,
		CodexFingerprintMode: auth.CodexFingerprintModeSingleMachineMultiWindow,
		CustomHeaders: map[string]string{
			"X-Codex-Installation-Id": "pinned-device-id",
			"User-Agent":              "custom-ua",
			"X-Debug":                 "keep",
		},
	}
	ids := resolveCodexFingerprintIDs(account, nil)
	want := deriveStableCodexUUID(fmt.Sprintf("codex2api:codex-install-id:v1:%d", account.ID()))
	if ids == nil || ids.installationID != want || ids.sessionID != "" {
		t.Fatalf("pin leaked into identity: %+v", ids)
	}
	outbound := http.Header{}
	ApplyAccountCustomHeaders(outbound, account)
	if outbound.Get("X-Codex-Installation-Id") != "" || outbound.Get("User-Agent") != "" {
		t.Fatal("identity custom headers overrode the converged client")
	}
	if outbound.Get("X-Debug") != "keep" {
		t.Fatal("non-identity custom header was dropped")
	}
	device := &auth.Account{
		DBID:                 42,
		CodexFingerprintMode: auth.CodexFingerprintModeDevice,
		CustomHeaders:        map[string]string{"X-Codex-Installation-Id": "pinned-device-id"},
	}
	if got := resolveCodexFingerprintIDs(device, http.Header{}); got == nil || got.installationID != "pinned-device-id" {
		t.Fatal("device mode stopped honoring the installation pin")
	}
}

func TestSingleMachineForcesGeneratedClientHeaders(t *testing.T) {
	prev := CurrentRuntimeSettings()
	t.Cleanup(func() { ApplyRuntimeSettings(prev) })
	ApplyRuntimeSettings(RuntimeSettings{ClientCompatMode: ClientCompatModePreserve})
	downstream := http.Header{}
	downstream.Set("User-Agent", "codex-tui/0.153.3 (Linux Unknown; x86_64) xterm-256color (codex-tui; 0.153.3)")
	downstream.Set("Originator", "codex-tui")
	account := fingerprintAccount(t, auth.CodexFingerprintModeSingleMachineMultiWindow)
	userAgent, _, used := resolveCodexOutboundClientHeaders(account, "api-key", nil, downstream)
	if !used || userAgent == "" || userAgent == downstream.Get("User-Agent") {
		t.Fatalf("single machine leaked downstream UA %q used=%v", userAgent, used)
	}
	device := fingerprintAccount(t, auth.CodexFingerprintModeDevice)
	passed, _, used := resolveCodexOutboundClientHeaders(device, "api-key", nil, downstream)
	if used || passed != downstream.Get("User-Agent") {
		t.Fatalf("device mode changed UA %q used=%v", passed, used)
	}
}

func TestSingleMachineAnonymousHTTPWireUsesCacheSeed(t *testing.T) {
	t.Setenv("CODEX_SESSION_HEADER_MODE", "native")
	prev := CurrentRuntimeSettings()
	t.Cleanup(func() { ApplyRuntimeSettings(prev) })
	ApplyRuntimeSettings(RuntimeSettings{ClientCompatMode: ClientCompatModePreserve, CodexForceWebsocket: false})
	a := fingerprintAccount(t, auth.CodexFingerprintModeSingleMachineMultiWindow)
	a.AccessToken = "test-token"
	body := []byte(`{"model":"gpt-5.6-codex","input":[],"prompt_cache_key":"conversation-a","client_metadata":{}}`)
	var gotBody []byte
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody = readUpstreamRequestBody(r)
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	defer server.Close()
	previous := resinCfg.Load()
	t.Cleanup(func() { resinCfg.Store(previous) })
	SetResinConfig(&ResinConfig{BaseURL: server.URL, PlatformName: "test"})
	clientPool.Delete(fmt.Sprintf("resin|%d", a.ID()))
	resp, err := ExecuteRequest(context.Background(), a, body, "", "", "test-key", nil, http.Header{}, false)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gjson.GetBytes(gotBody, "prompt_cache_key").String() != gotHeaders.Get("Thread-Id") {
		t.Fatalf("cache key changed: %s", gotBody)
	}
	if !gjson.GetBytes(gotBody, "client_metadata").Exists() {
		t.Fatal("existing client_metadata disappeared")
	}
	session := gotHeaders.Get("Session-Id")
	if session == "" || session == "conversation-a" || gotHeaders.Get("Thread-Id") == "" {
		t.Fatalf("session headers = %v", gotHeaders)
	}
	if gjson.GetBytes(gotBody, "client_metadata.session_id").String() != session || gjson.GetBytes(gotBody, "client_metadata.installation_id").String() == "" {
		t.Fatalf("body identity = %s", gotBody)
	}
	if gotHeaders.Get(codexWindowIDHeader) == "" || gotHeaders.Get(codexWindowIDHeader) != gjson.GetBytes(gotBody, "client_metadata.window_id").String() {
		t.Fatalf("window header = %q body = %s", gotHeaders.Get(codexWindowIDHeader), gotBody)
	}
	if gotHeaders.Get("Conversation_id") != "" || gotHeaders.Get("Session_id") != "" || gotHeaders.Get(codexInstallationIDHeader) != "" {
		t.Fatalf("unexpected identity headers: %v", gotHeaders)
	}
	if gjson.Get(gotHeaders.Get(codexTurnMetadataHeader), "installation_id").String() == "" {
		t.Fatal("turn metadata missing installation")
	}
}
