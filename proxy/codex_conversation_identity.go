package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/codex2api/auth"
)

// 会话身份的 v7 时间戳取这段种子第一次出现的时刻，并落盘，避免重启后回到 2025-01-01。
// oneshot 种子只活在这一次请求里，不写入共享线程表。

type codexConversationStampFile struct {
	Stamps    map[string]int64  `json:"stamps"`
	Responses map[string]string `json:"responses"`
}

type codexConversationIdentityState struct {
	mu        sync.Mutex
	stamps    map[string]int64
	responses map[string]string
	loaded    bool
}

var codexConversationIdentity = &codexConversationIdentityState{}

func resetCodexConversationIdentity() {
	codexConversationIdentity.mu.Lock()
	codexConversationIdentity.stamps = nil
	codexConversationIdentity.responses = nil
	codexConversationIdentity.loaded = false
	codexConversationIdentity.mu.Unlock()
}

func codexIdentityStorePath() string {
	if value := strings.TrimSpace(os.Getenv("CODEX_IDENTITY_STORE")); value != "" {
		if value == "memory" {
			return ""
		}
		return value
	}
	return filepath.Join(codexPersistentStateDir(), "codex-conversation-identity.json")
}

// Operation and OS/decoder error only: omit configured paths and stored values.
func logCodexIdentityStoreError(operation string, err error) {
	var pathErr *os.PathError
	var linkErr *os.LinkError
	if errors.As(err, &pathErr) {
		err = pathErr.Err
	} else if errors.As(err, &linkErr) {
		err = linkErr.Err
	}
	// JSON decoder type errors may embed a stored numeric/string value.
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &syntaxErr) || errors.As(err, &typeErr) {
		err = errors.New("invalid stored JSON")
	}
	log.Printf("[CODEX-IDENTITY] store %s failed: %v", operation, err)
}

func (s *codexConversationIdentityState) loadLocked() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.stamps = map[string]int64{}
	s.responses = map[string]string{}
	path := codexIdentityStorePath()
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			logCodexIdentityStoreError("read", err)
		}
		return
	}
	var file codexConversationStampFile
	if err := json.Unmarshal(raw, &file); err != nil {
		logCodexIdentityStoreError("decode", err)
		return
	}
	if file.Stamps != nil {
		s.stamps = file.Stamps
	}
	if file.Responses != nil {
		s.responses = file.Responses
	}
}

func (s *codexConversationIdentityState) saveLocked() {
	path := codexIdentityStorePath()
	if path == "" {
		return
	}
	payload, err := json.Marshal(codexConversationStampFile{Stamps: s.stamps, Responses: s.responses})
	if err != nil {
		logCodexIdentityStoreError("encode", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		logCodexIdentityStoreError("mkdir", err)
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o600); err != nil {
		logCodexIdentityStoreError("write", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		logCodexIdentityStoreError("rename", err)
	}
}

func conversationFirstSeenUnixMilli(seed string, persist bool) int64 {
	seed = strings.TrimSpace(seed)
	if seed == "" {
		return time.Now().UnixMilli()
	}
	s := codexConversationIdentity
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	if ms, ok := s.stamps[seed]; ok && ms > 0 {
		return ms
	}
	ms := time.Now().UnixMilli()
	if !persist {
		return ms
	}
	s.stamps[seed] = ms
	s.saveLocked()
	return ms
}

func codexConversationResponseKey(accountID int64, responseID string) string {
	return strings.TrimSpace(responseID) + "@" + strconv.FormatInt(accountID, 10)
}

func rememberCodexConversationResponse(accountID int64, responseID, seed string) {
	responseID = strings.TrimSpace(responseID)
	seed = strings.TrimSpace(seed)
	if accountID <= 0 || responseID == "" || seed == "" || strings.HasPrefix(seed, "oneshot:") {
		return
	}
	s := codexConversationIdentity
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	key := codexConversationResponseKey(accountID, responseID)
	if s.responses[key] == seed {
		return
	}
	s.responses[key] = seed
	s.saveLocked()
}

func codexRawConversationSeed(headers http.Header) string {
	sessionID, threadID := explicitCodexSessionHeaders(headers)
	if sessionID != "" {
		return sessionID
	}
	return threadID
}

// codexConversationOwner belongs to one outbound attempt, never to a mutable
// affinity binding. A concurrent attempt may choose another account or thread.
type codexConversationOwner struct {
	accountID   int64
	scope       string
	sessionSeed string
	threadSeed  string
	threadID    string
}
type codexConversationOwnerKey struct{}

func codexConversationOwnerFromContext(ctx context.Context) (codexConversationOwner, bool) {
	if ctx == nil {
		return codexConversationOwner{}, false
	}
	owner, ok := ctx.Value(codexConversationOwnerKey{}).(codexConversationOwner)
	return owner, ok && owner.accountID > 0 && owner.threadID != ""
}

func (owner codexConversationOwner) responseKey(id string) string {
	return codexConversationResponseKey(owner.accountID, id) + "|scope:" + owner.scope
}

type codexConversationSeeds struct {
	Session string `json:"session"`
	Thread  string `json:"thread"`
}

func noteCodexConversationResponseFromContext(ctx context.Context, responseID string) {
	owner, ok := codexConversationOwnerFromContext(ctx)
	if !ok || strings.TrimSpace(responseID) == "" {
		return
	}
	raw, err := json.Marshal(codexConversationSeeds{owner.sessionSeed, owner.threadSeed})
	if err != nil {
		return
	}
	s := codexConversationIdentity
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	key := owner.responseKey(strings.TrimSpace(responseID))
	if s.responses[key] != string(raw) {
		s.responses[key] = string(raw)
		s.saveLocked()
	}
}

// prepareCodexConversationIdentity preserves the account's small upstream window
// set, but namespaces thread/cache/transport identities by downstream API key.
// All raw lineage references use that same namespace before fingerprinting.
func prepareCodexConversationIdentity(ctx context.Context, account *auth.Account, headers http.Header, body []byte, apiKey string) (context.Context, http.Header, []byte) {
	if account == nil || account.EffectiveCodexFingerprintMode() != auth.CodexFingerprintModeSingleMachineMultiWindow {
		return ctx, headers, body
	}
	if ctx == nil {
		ctx = context.Background()
	}
	headers = PrepareCodexFingerprintHeaders(account, headers, body).Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	scope := ""
	if apiKey != "" {
		scope = deriveStableCodexUUID("codex2api:conversation-scope:v1:" + apiKey)
	}
	owner := codexConversationOwner{accountID: account.ID(), scope: scope}
	session, thread := explicitCodexSessionHeaders(headers)
	recovered := false
	if session == "" && thread == "" && clientPromptCacheKey(body) == "" {
		if prev := strings.TrimSpace(gjson.GetBytes(body, "previous_response_id").String()); prev != "" {
			s := codexConversationIdentity
			s.mu.Lock()
			s.loadLocked()
			raw := s.responses[owner.responseKey(prev)]
			s.mu.Unlock()
			var seeds codexConversationSeeds
			if json.Unmarshal([]byte(raw), &seeds) == nil && seeds.Session != "" && seeds.Thread != "" {
				headers.Set(codexSessionIDHeader, seeds.Session)
				headers.Set(codexThreadIDHeader, seeds.Thread)
				recovered = true
			}
		}
	}
	headers = ensureSingleMachineConversationSeed(account, headers, body, false)
	if scope != "" {
		scoped := func(raw string) string {
			if raw == "" {
				return ""
			}
			return deriveStableCodexUUID("codex2api:conversation-seed:v1:" + scope + ":" + raw)
		}
		if !recovered {
			session, thread = explicitCodexSessionHeaders(headers)
			headers.Set(codexSessionIDHeader, scoped(session))
			headers.Set(codexThreadIDHeader, scoped(thread))
		}
		for _, name := range []string{codexParentThreadIDHeader, codexClientRequestIDHeader} {
			if value := headers.Get(name); value != "" {
				headers.Set(name, scoped(value))
			}
		}
		scopeMetadata := func(raw string) string {
			for _, key := range codexLineageMetadataPaths {
				if v := gjson.Get(raw, key); v.Type == gjson.String && v.String() != "" {
					raw, _ = sjson.Set(raw, key, scoped(v.String()))
				}
			}
			return raw
		}
		if raw := headers.Get(codexTurnMetadataHeader); gjson.Valid(raw) {
			headers.Set(codexTurnMetadataHeader, scopeMetadata(raw))
		}
		body = bytes.Clone(body)
		for _, key := range codexLineageMetadataPaths {
			path := "client_metadata." + key
			if v := gjson.GetBytes(body, path); v.Type == gjson.String && v.String() != "" {
				body, _ = sjson.SetBytes(body, path, scoped(v.String()))
			}
		}
		if v := gjson.GetBytes(body, "client_metadata.x-codex-turn-metadata"); v.Type == gjson.String && gjson.Valid(v.String()) {
			body, _ = sjson.SetBytes(body, "client_metadata.x-codex-turn-metadata", scopeMetadata(v.String()))
		}
	}
	owner.sessionSeed, owner.threadSeed = explicitCodexSessionHeaders(headers)
	_, owner.threadID = ConvergedCodexSessionIdentity(account, headers)
	return context.WithValue(ctx, codexConversationOwnerKey{}, owner), headers, body
}

func lookupCodexConversationResponse(accountID int64, responseID string) string {
	responseID = strings.TrimSpace(responseID)
	if accountID <= 0 || responseID == "" {
		return ""
	}
	s := codexConversationIdentity
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	return s.responses[codexConversationResponseKey(accountID, responseID)]
}

func upstreamContentSeed(body []byte) string {
	// Incremental continuation input is not the opening of a new conversation.
	return deriveContentSessionSeed(body)
}

func clientPromptCacheKey(body []byte) string {
	return strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String())
}

// ensureSingleMachineConversationSeed 给这次请求钉上一段对话种子。
// 顺序：显式 session/thread，客户端自己的 prompt_cache_key，previous_response_id
// 找回，内容种子，否则并入该账号的常驻窗口。出站路径在调用前完成 API Key 隔离的续链找回。
func ensureSingleMachineConversationSeed(account *auth.Account, headers http.Header, body []byte, legacyRecall ...bool) http.Header {
	if account == nil || account.EffectiveCodexFingerprintMode() != auth.CodexFingerprintModeSingleMachineMultiWindow {
		return headers
	}
	out := headers.Clone()
	if out == nil {
		out = make(http.Header)
	}
	sessionID, threadID := explicitCodexSessionHeaders(out)
	if sessionID != "" || threadID != "" {
		if sessionID == "" {
			out.Set(codexSessionIDHeader, threadID)
		}
		if threadID == "" {
			out.Set(codexThreadIDHeader, sessionID)
		}
		return out
	}
	if key := clientPromptCacheKey(body); key != "" && !IsStatelessWebsocketSessionID(key) {
		out.Set(codexSessionIDHeader, key)
		out.Set(codexThreadIDHeader, key)
		return out
	}
	if prev := strings.TrimSpace(gjson.GetBytes(body, "previous_response_id").String()); prev != "" && (len(legacyRecall) == 0 || legacyRecall[0]) {
		if recovered := lookupCodexConversationResponse(account.ID(), prev); recovered != "" {
			out.Set(codexSessionIDHeader, recovered)
			out.Set(codexThreadIDHeader, recovered)
			return out
		}
	}
	if seed := upstreamContentSeed(body); seed != "" {
		out.Set(codexSessionIDHeader, seed)
		out.Set(codexThreadIDHeader, seed)
		return out
	}
	standing := fmt.Sprintf("window:%d", account.ID()%singleMachineOutboundWindows)
	out.Set(codexSessionIDHeader, standing)
	out.Set(codexThreadIDHeader, standing)
	return out
}

// AttachCodexMaintenanceThread 给账号维护探针钉一段稳定后台线程。
// 没有种子的真实用户请求并入该账号的常驻窗口，不每笔新开上游会话。
func AttachCodexMaintenanceThread(account *auth.Account, headers http.Header) http.Header {
	if account == nil || account.EffectiveCodexFingerprintMode() != auth.CodexFingerprintModeSingleMachineMultiWindow {
		return headers
	}
	if headers == nil {
		headers = make(http.Header)
	} else {
		headers = headers.Clone()
	}
	if sessionID, threadID := explicitCodexSessionHeaders(headers); sessionID != "" || threadID != "" {
		return headers
	}
	seed := "maintenance:" + strconv.FormatInt(account.ID(), 10)
	headers.Set(codexSessionIDHeader, seed)
	headers.Set(codexThreadIDHeader, seed)
	return headers
}

type codexResponseRecallBody struct {
	io.ReadCloser
	ctx  context.Context
	buf  []byte
	done bool
}

func watchCodexConversationResponse(ctx context.Context, resp *http.Response) {
	if resp == nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return
	}
	if _, ok := codexConversationOwnerFromContext(ctx); !ok {
		return
	}
	// HTTP response headers belong to this attempt even when a retry heartbeat has
	// already committed downstream headers. Never recover ownership via affinity.
	noteCodexThreadTurnStateFromContext(ctx, resp.Header.Get(codexTurnStateHeader))
	if resp.Body != nil {
		resp.Body = &codexResponseRecallBody{ReadCloser: resp.Body, ctx: ctx}
	}
}

func (b *codexResponseRecallBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 && !b.done {
		b.buf = append(b.buf, p[:n]...)
		if len(b.buf) > 1<<20 {
			b.buf = b.buf[len(b.buf)-1<<20:]
		}
		b.scan()
	}
	if err != nil {
		b.scan()
	}
	return n, err
}

func (b *codexResponseRecallBody) scan() {
	if b.done {
		return
	}
	if id := responseIDFromRecallBuffer(b.buf); id != "" {
		noteCodexConversationResponseFromContext(b.ctx, id)
		b.done = true
	}
}

func responseIDFromRecallBuffer(buf []byte) string {
	if id := strings.TrimSpace(gjson.GetBytes(buf, "id").String()); id != "" && gjson.GetBytes(buf, "status").String() == "completed" {
		return id
	}
	for _, marker := range [][]byte{[]byte(`"type":"response.completed"`), []byte(`"type": "response.completed"`)} {
		idx := bytes.LastIndex(buf, marker)
		if idx < 0 {
			continue
		}
		start := bytes.LastIndex(buf[:idx], []byte{'{'})
		if start < 0 {
			continue
		}
		if id := strings.TrimSpace(gjson.GetBytes(buf[start:], "response.id").String()); id != "" {
			return id
		}
	}
	return ""
}
