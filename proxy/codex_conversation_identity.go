package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	if dir := strings.TrimSpace(os.Getenv("DATA_DIR")); dir != "" {
		return filepath.Join(dir, "codex-conversation-identity.json")
	}
	return ""
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
	if err != nil || len(raw) == 0 {
		return
	}
	var file codexConversationStampFile
	if json.Unmarshal(raw, &file) != nil {
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
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, payload, 0o600) != nil {
		return
	}
	_ = os.Rename(tmp, path)
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

type codexConversationRecall struct {
	accountID int64
	seed      string
}

var codexConversationRecalls sync.Map // affinityKey -> codexConversationRecall

// bindCodexConversationRecall 记住这次出站用的原始种子，响应里的 response id 才能找回同一段对话。
// 存收敛后的 UUID 会在下一轮被再哈希一次，续聊会漂到另一条线程。
func bindCodexConversationRecall(affinityKey string, accountID int64, seed string) {
	affinityKey = strings.TrimSpace(affinityKey)
	seed = strings.TrimSpace(seed)
	if affinityKey == "" || accountID <= 0 || seed == "" || strings.HasPrefix(seed, "oneshot:") {
		return
	}
	codexConversationRecalls.Store(affinityKey, codexConversationRecall{accountID: accountID, seed: seed})
}

func noteCodexConversationResponseFromContext(ctx context.Context, responseID string) {
	affinityKey := CodexTurnStateAffinityKeyFromContext(ctx)
	if affinityKey == "" {
		return
	}
	raw, ok := codexConversationRecalls.Load(affinityKey)
	if !ok {
		return
	}
	recall, ok := raw.(codexConversationRecall)
	if !ok {
		return
	}
	rememberCodexConversationResponse(recall.accountID, responseID, recall.seed)
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
	if strings.TrimSpace(gjson.GetBytes(body, "previous_response_id").String()) == "" {
		return deriveContentSessionSeed(body)
	}
	stripped, err := sjson.DeleteBytes(append([]byte(nil), body...), "previous_response_id")
	if err != nil {
		return ""
	}
	return deriveContentSessionSeed(stripped)
}

func clientPromptCacheKey(body []byte) string {
	return strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String())
}

// ensureSingleMachineConversationSeed 给这次请求钉上一段对话种子。
// 顺序：显式 session/thread，客户端自己的 prompt_cache_key，内容种子，
// previous_response_id 找回，否则并入该账号的常驻窗口。无状态连接 ID 和网关隔离 UUID 不在这里。
func ensureSingleMachineConversationSeed(account *auth.Account, headers http.Header, body []byte) http.Header {
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
	if seed := upstreamContentSeed(body); seed != "" {
		out.Set(codexSessionIDHeader, seed)
		out.Set(codexThreadIDHeader, seed)
		return out
	}
	if prev := strings.TrimSpace(gjson.GetBytes(body, "previous_response_id").String()); prev != "" {
		if recovered := lookupCodexConversationResponse(account.ID(), prev); recovered != "" {
			out.Set(codexSessionIDHeader, recovered)
			out.Set(codexThreadIDHeader, recovered)
			return out
		}
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
	accountID int64
	seed      string
	buf       []byte
	done      bool
}

func watchCodexConversationResponse(resp *http.Response, account *auth.Account, headers http.Header) {
	if resp == nil || resp.Body == nil || account == nil || account.EffectiveCodexFingerprintMode() != auth.CodexFingerprintModeSingleMachineMultiWindow {
		return
	}
	seed := codexRawConversationSeed(headers)
	if seed == "" || strings.HasPrefix(seed, "oneshot:") {
		return
	}
	resp.Body = &codexResponseRecallBody{ReadCloser: resp.Body, accountID: account.ID(), seed: seed}
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
		rememberCodexConversationResponse(b.accountID, id, b.seed)
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
