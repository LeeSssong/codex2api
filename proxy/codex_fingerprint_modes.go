package proxy

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/codex2api/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// singleMachineOutboundWindows is how many upstream sessions one account
// advertises. Threads are not folded: a parent and its subagents share a
// session and keep distinct thread ids.
const singleMachineOutboundWindows = 4

// singleMachineIdentity uses one namespace so a child's parent_thread_id equals
// the parent's rewritten thread_id. Pass the raw thread id. Passing a window
// slot collapses different threads that happen to hash into the same slot.
// It is independent of request timing, account token refreshes and concurrency.
func singleMachineIdentity(accountID int64, original string) string {
	if original == "" {
		return ""
	}
	seed := fmt.Sprintf("codex2api:single-machine-identity:v1:%d:%s", accountID, original)
	// oneshot 不落盘，避免一次性请求堆成共享线程。
	return deriveStableCodexUUIDv7(seed, conversationFirstSeenUnixMilli(seed, !strings.HasPrefix(original, "oneshot:")))
}

// singleMachineWindowSlot folds an inbound conversation seed onto one of the
// account's fixed outbound windows. The same seed always lands on the same
// window, and different inbound clients share that small set.
func singleMachineWindowSlot(accountID int64, original string) string {
	if accountID <= 0 || original == "" || strings.HasPrefix(original, "oneshot:") {
		return original
	}
	slot := int(codexUAHash(fmt.Sprintf("codex2api:single-machine-window:v1:%d:%s", accountID, original)) % singleMachineOutboundWindows)
	return fmt.Sprintf("window:%d", slot)
}

func convergeFingerprintLineageValue(ids *codexFingerprintIDs, key, original string) string {
	if ids.mode == auth.CodexFingerprintModeSingleMachineMultiWindow && key != "context_window_id" {
		// parent_thread_id / forked_from_thread_id 指向另一条线程，必须用和
		// thread_id 相同的派生，不能再折进窗口。折进窗口会让子代理和父线程变成同一个 id。
		return singleMachineIdentity(ids.accountID, original)
	}
	return convergeCodexLineageValue(ids.accountID, key, original)
}

func convergeFingerprintLineageMetadata(raw string, ids *codexFingerprintIDs) (string, bool) {
	if ids.mode != auth.CodexFingerprintModeSingleMachineMultiWindow {
		return convergeCodexLineageMetadata(raw, ids.accountID)
	}
	changed := false
	for _, key := range codexLineageMetadataPaths {
		v := gjson.Get(raw, key)
		if v.Type != gjson.String || strings.TrimSpace(v.String()) == "" {
			continue
		}
		next, err := sjson.Set(raw, key, convergeFingerprintLineageValue(ids, key, strings.TrimSpace(v.String())))
		if err == nil && next != raw {
			raw = next
			changed = true
		}
	}
	return raw, changed
}

func rewriteSingleMachineBodyLineage(body []byte, ids *codexFingerprintIDs) []byte {
	for _, key := range codexLineageMetadataPaths {
		path := "client_metadata." + key
		v := gjson.GetBytes(body, path)
		if v.Type == gjson.String && strings.TrimSpace(v.String()) != "" {
			body = setExistingJSONString(body, path, convergeFingerprintLineageValue(ids, key, strings.TrimSpace(v.String())))
		}
	}
	body = setExistingJSONString(body, "client_metadata.window_id", ids.windowID)
	return body
}

// ensureSingleMachineClientMetadata fills device identity inside an existing
// client_metadata object. It does not create the object. Session fields are
// written only when this request already has a conversation seed.
func ensureSingleMachineClientMetadata(body []byte, ids *codexFingerprintIDs) []byte {
	if ids == nil || ids.mode != auth.CodexFingerprintModeSingleMachineMultiWindow {
		return body
	}
	if !gjson.GetBytes(body, "client_metadata").IsObject() {
		return body
	}
	body = setJSONString(body, "client_metadata.installation_id", ids.installationID)
	if ids.sessionID == "" {
		return body
	}
	body = setJSONString(body, "client_metadata.session_id", ids.sessionID)
	body = setJSONString(body, "client_metadata.thread_id", ids.threadID)
	body = setJSONString(body, "client_metadata.window_id", ids.windowID)
	body = setJSONString(body, "client_metadata.x-codex-window-id", ids.windowID)
	return body
}

// PrepareCodexFingerprintHeaders makes body-only identities available to all
// consumers BEFORE any body rewrite. Caller headers are never mutated. This is
// only enabled for the new mode; old modes retain their exact historical input.
// Do not recover identity from prompt_cache_key or user content. A cache key
// may be supplied later, explicitly, by AttachCodexFingerprintCacheSeed.
func PrepareCodexFingerprintHeaders(account *auth.Account, headers http.Header, body []byte) http.Header {
	if account == nil || account.EffectiveCodexFingerprintMode() != auth.CodexFingerprintModeSingleMachineMultiWindow {
		return headers
	}
	metadata := gjson.GetBytes(body, "client_metadata")
	if !metadata.IsObject() {
		return headers
	}
	out := headers.Clone()
	if out == nil {
		out = make(http.Header)
	}
	embedded := metadata.Get("x-codex-turn-metadata")
	if out.Get(codexTurnMetadataHeader) == "" && embedded.Type == gjson.String && gjson.Valid(embedded.String()) && gjson.Parse(embedded.String()).IsObject() {
		out.Set(codexTurnMetadataHeader, embedded.String())
	}
	session, thread := extractClientCodexIdentity(out)
	if session == "" {
		if v := metadata.Get("session_id"); v.Type == gjson.String && strings.TrimSpace(v.String()) != "" {
			out.Set(codexSessionIDHeader, strings.TrimSpace(v.String()))
		}
	}
	if thread == "" {
		if v := metadata.Get("thread_id"); v.Type == gjson.String && strings.TrimSpace(v.String()) != "" {
			out.Set(codexThreadIDHeader, strings.TrimSpace(v.String()))
		}
	}
	return out
}

// AttachCodexFingerprintCacheSeed copies a finalized prompt_cache_key onto a
// header clone when the client sent neither session nor thread. The clone is
// only a derivation input for singleMachineIdentity. The cache key itself is
// never written back, and a stateless websocket connection id is not a seed.
// Different seeds stay different conversations; one empty seed does not become
// one account-wide thread.
func AttachCodexFingerprintCacheSeed(account *auth.Account, headers http.Header, cacheKey string) http.Header {
	if account == nil || account.EffectiveCodexFingerprintMode() != auth.CodexFingerprintModeSingleMachineMultiWindow {
		return headers
	}
	cacheKey = strings.TrimSpace(cacheKey)
	if cacheKey == "" || IsStatelessWebsocketSessionID(cacheKey) {
		return headers
	}
	// 已有直接声明的会话头时，缓存键不再改这段身份。turn metadata 里的会话不算声明，
	// 否则它会挡住缓存键，同一次请求就出现两段身份。
	sessionID, threadID := explicitCodexSessionHeaders(headers)
	if sessionID != "" || threadID != "" {
		return headers
	}
	out := headers.Clone()
	if out == nil {
		out = make(http.Header)
	}
	out.Set(codexSessionIDHeader, cacheKey)
	out.Set(codexThreadIDHeader, cacheKey)
	return out
}

// FinalizeCodexSingleMachineIdentity 钉上对话种子，并把出站 prompt_cache_key
// 改写成同一段 thread。调用方传入的缓存种子优先于正文里同名字段，这样不同种子
// 不会被同一份未改写正文收成一段对话。无状态连接 ID 不能当种子；没有种子时并入
// 该账号的常驻窗口，不每笔新开上游会话。
func FinalizeCodexSingleMachineIdentity(account *auth.Account, headers http.Header, body []byte, cacheKey string) (http.Header, []byte) {
	if account == nil || account.EffectiveCodexFingerprintMode() != auth.CodexFingerprintModeSingleMachineMultiWindow {
		return headers, body
	}
	if sessionID, threadID := explicitCodexSessionHeaders(headers); sessionID == "" && threadID == "" {
		headers = AttachCodexFingerprintCacheSeed(account, headers, cacheKey)
	}
	headers = ensureSingleMachineConversationSeed(account, headers, body)
	sessionID, threadID := explicitCodexSessionHeaders(headers)
	if sessionID == "" && threadID == "" {
		// 无入站会话时并入该账号的常驻窗口，而不是每笔新开一个上游会话。
		one := fmt.Sprintf("window:%d", account.ID()%singleMachineOutboundWindows)
		if headers == nil {
			headers = make(http.Header)
		}
		headers.Set(codexSessionIDHeader, one)
		headers.Set(codexThreadIDHeader, one)
	}
	body = ApplyCodexFingerprintToBody(body, account, headers)
	ids := resolveCodexFingerprintIDs(account, headers)
	if ids != nil && ids.threadID != "" {
		if updated, err := sjson.SetBytes(body, "prompt_cache_key", ids.threadID); err == nil {
			body = updated
		}
	}
	return headers, body
}

// CodexSingleMachineBlockedCustomHeaders lists custom-header names that would
// override the converged device or session identity. Empty when none match.
func CodexSingleMachineBlockedCustomHeaders(headers map[string]string) []string {
	if len(headers) == 0 {
		return nil
	}
	names := make([]string, 0, len(headers))
	for name := range headers {
		if codexSingleMachineBlockedHeader(name) {
			names = append(names, strings.TrimSpace(name))
		}
	}
	sort.Strings(names)
	return names
}

func codexSingleMachineBlockedHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "user-agent", "originator", "version",
		"session-id", "session_id", "conversation_id", "thread-id",
		"x-client-request-id", "x-codex-installation-id", "x-codex-turn-metadata",
		"x-codex-window-id", "x-codex-parent-thread-id":
		return true
	default:
		return false
	}
}

// ScopeCodexFingerprintTransportKey keeps pooled WS handshakes apart for
// different inbound conversations. The upstream identity is folded into a few
// windows, but the local pool key stays on the inbound seed so those windows
// do not share one frozen handshake. This key never goes upstream.
// An installation-only identity (no session yet) stays on the original key so
// anonymous handshakes are not collapsed onto one suffix.
func ScopeCodexFingerprintTransportKey(key string, account *auth.Account, headers http.Header) string {
	if key == "" || account == nil || account.EffectiveCodexFingerprintMode() != auth.CodexFingerprintModeSingleMachineMultiWindow {
		return key
	}
	sessionID, threadID := explicitCodexSessionHeaders(headers)
	if sessionID == "" && threadID == "" {
		return key
	}
	if sessionID == "" {
		sessionID = threadID
	}
	if threadID == "" {
		threadID = sessionID
	}
	return key + ":fingerprint:" + deriveStableCodexUUID(sessionID+":"+threadID)
}
