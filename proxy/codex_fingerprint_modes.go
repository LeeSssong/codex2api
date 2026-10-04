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

// singleMachineIdentity deliberately uses one namespace for thread references:
// a child's parent_thread_id must equal the parent's rewritten thread_id.
// It is independent of request timing, account token refreshes and concurrency.
func singleMachineIdentity(accountID int64, original string) string {
	if original == "" {
		return ""
	}
	seed := fmt.Sprintf("codex2api:single-machine-identity:v1:%d:%s", accountID, original)
	return deriveStableCodexUUIDv7(seed, seededIdentityUnixMilli(seed))
}

func convergeFingerprintLineageValue(ids *codexFingerprintIDs, key, original string) string {
	if ids.mode == auth.CodexFingerprintModeSingleMachineMultiWindow && key != "context_window_id" {
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
	sessionID, threadID := extractClientCodexIdentity(headers)
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

// FinalizeCodexSingleMachineIdentity applies the cache-key seed, if any, and
// rewrites body metadata so it matches the headers. prompt_cache_key is left
// untouched. Other fingerprint modes return the inputs unchanged.
func FinalizeCodexSingleMachineIdentity(account *auth.Account, headers http.Header, body []byte, cacheKey string) (http.Header, []byte) {
	if account == nil || account.EffectiveCodexFingerprintMode() != auth.CodexFingerprintModeSingleMachineMultiWindow {
		return headers, body
	}
	headers = AttachCodexFingerprintCacheSeed(account, headers, cacheKey)
	return headers, ApplyCodexFingerprintToBody(body, account, headers)
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

// ScopeCodexFingerprintTransportKey keeps pooled WS handshakes consistent with
// per-frame identities. Retain the existing key/API-key partition as a prefix.
// This is a local pool key, never an upstream session or prompt_cache_key.
// An installation-only identity (no session yet) stays on the original key so
// anonymous handshakes are not collapsed onto one suffix.
func ScopeCodexFingerprintTransportKey(key string, account *auth.Account, headers http.Header) string {
	if key == "" || account == nil || account.EffectiveCodexFingerprintMode() != auth.CodexFingerprintModeSingleMachineMultiWindow {
		return key
	}
	ids := resolveCodexFingerprintIDs(account, headers)
	if ids == nil || ids.sessionID == "" || ids.threadID == "" {
		return key
	}
	return key + ":fingerprint:" + deriveStableCodexUUID(ids.sessionID+":"+ids.threadID)
}
