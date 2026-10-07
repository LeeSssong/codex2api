package proxy

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

const maxCodexFingerprintLogLength = 2048

// codexFingerprintObservation 是一次出站尝试的身份快照。
// 所有标识都只存短哈希，用量日志因此能证明同一次请求里会话、线程、窗口、
// 安装标识有没有互相矛盾，同时不落下原文、证明令牌或提示词。
type codexFingerprintObservation struct {
	Kind                 string `json:"kind"`
	UAHash               string `json:"ua_hash,omitempty"`
	Originator           string `json:"originator,omitempty"`
	Version              string `json:"version,omitempty"`
	SessionHash          string `json:"session_hash,omitempty"`
	ThreadHash           string `json:"thread_hash,omitempty"`
	WindowHash           string `json:"window_hash,omitempty"`
	RequestHash          string `json:"request_hash,omitempty"`
	ParentHash           string `json:"parent_hash,omitempty"`
	InstallationHeader   bool   `json:"installation_header"`
	TurnInstallationHash string `json:"turn_installation_hash,omitempty"`
	TurnSessionHash      string `json:"turn_session_hash,omitempty"`
	TurnThreadHash       string `json:"turn_thread_hash,omitempty"`
	TurnWindowHash       string `json:"turn_window_hash,omitempty"`
	TurnParentHash       string `json:"turn_parent_hash,omitempty"`
	BodyInstallationHash string `json:"body_installation_hash,omitempty"`
	BodySessionHash      string `json:"body_session_hash,omitempty"`
	BodyThreadHash       string `json:"body_thread_hash,omitempty"`
	BodyWindowHash       string `json:"body_window_hash,omitempty"`
	BodyParentHash       string `json:"body_parent_hash,omitempty"`
	CacheKeyHash         string `json:"cache_key_hash,omitempty"`
	Attestation          bool   `json:"attestation"`
	AttestationHash      string `json:"attestation_hash,omitempty"`
	LegacySession        bool   `json:"legacy_session"`
	LegacyConversation   bool   `json:"legacy_conversation"`
	Stainless            bool   `json:"stainless"`
	Aligned              bool   `json:"aligned"`
	Mismatch             string `json:"mismatch,omitempty"`
}

type codexFingerprintAuditContextKey struct{}

type codexFingerprintAudit struct {
	mu    sync.Mutex
	raw   string
	known bool
}

func withCodexFingerprintAudit(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if codexFingerprintAuditFromContext(ctx) != nil {
		return ctx
	}
	return context.WithValue(ctx, codexFingerprintAuditContextKey{}, &codexFingerprintAudit{})
}

func codexFingerprintAuditFromContext(ctx context.Context) *codexFingerprintAudit {
	if ctx == nil {
		return nil
	}
	audit, _ := ctx.Value(codexFingerprintAuditContextKey{}).(*codexFingerprintAudit)
	return audit
}

func resetCodexFingerprintAudit(ctx context.Context) {
	audit := codexFingerprintAuditFromContext(ctx)
	if audit == nil {
		return
	}
	audit.mu.Lock()
	audit.raw = ""
	audit.known = false
	audit.mu.Unlock()
}

func recordCodexFingerprintAudit(ctx context.Context, raw string) {
	audit := codexFingerprintAuditFromContext(ctx)
	if audit == nil {
		return
	}
	audit.mu.Lock()
	audit.raw = raw
	audit.known = true
	audit.mu.Unlock()
}

func codexFingerprintAuditValue(ctx context.Context) (string, bool) {
	audit := codexFingerprintAuditFromContext(ctx)
	if audit == nil {
		return "", false
	}
	audit.mu.Lock()
	defer audit.mu.Unlock()
	return audit.raw, audit.known
}

func populateCodexFingerprintFromRequest(ctx context.Context, assign func(string)) {
	if assign == nil {
		return
	}
	raw, ok := codexFingerprintAuditValue(ctx)
	if !ok {
		return
	}
	assign(raw)
}

// buildCodexFingerprintObservation 对比同一次出站请求里已经写好的头和请求体。
// 同一组里只要出现两个非空且不同的值，就记为对不上。某一侧没发（例如不补造
// 安装标识头）不算冲突，空哈希仍然留在对应字段里，方便事后分辨「没发」和「发了两套」。
func buildCodexFingerprintObservation(kind string, headers http.Header, body []byte) codexFingerprintObservation {
	if headers == nil {
		headers = http.Header{}
	}
	turn := gjson.Parse(headers.Get(codexTurnMetadataHeader))
	sessionHeader := strings.TrimSpace(headers.Get("Session-Id"))
	legacySession := strings.TrimSpace(headers.Get("Session_id"))
	threadHeader := strings.TrimSpace(headers.Get(codexThreadIDHeader))
	windowHeader := strings.TrimSpace(headers.Get(codexWindowIDHeader))
	parentHeader := strings.TrimSpace(headers.Get(codexParentThreadIDHeader))
	installationHeader := strings.TrimSpace(headers.Get(codexInstallationIDHeader))
	turnSession := strings.TrimSpace(turn.Get("session_id").String())
	turnThread := strings.TrimSpace(turn.Get("thread_id").String())
	turnWindow := strings.TrimSpace(turn.Get("window_id").String())
	turnParent := strings.TrimSpace(turn.Get("parent_thread_id").String())
	turnInstallation := strings.TrimSpace(turn.Get("installation_id").String())
	bodySession := strings.TrimSpace(gjson.GetBytes(body, "client_metadata.session_id").String())
	bodyThread := strings.TrimSpace(gjson.GetBytes(body, "client_metadata.thread_id").String())
	bodyWindow := strings.TrimSpace(gjson.GetBytes(body, "client_metadata.window_id").String())
	bodyWindowHeader := strings.TrimSpace(gjson.GetBytes(body, "client_metadata.x-codex-window-id").String())
	bodyParent := strings.TrimSpace(gjson.GetBytes(body, "client_metadata.parent_thread_id").String())
	bodyInstallation := strings.TrimSpace(gjson.GetBytes(body, "client_metadata.installation_id").String())
	bodyInstallationHeader := strings.TrimSpace(gjson.GetBytes(body, "client_metadata.x-codex-installation-id").String())
	attestation := strings.TrimSpace(headers.Get(windowsDesktopAttestationHeader))

	var mismatches []string
	if fingerprintValuesDisagree(sessionHeader, legacySession, turnSession, bodySession) {
		mismatches = append(mismatches, "session")
	}
	if fingerprintValuesDisagree(threadHeader, turnThread, bodyThread) {
		mismatches = append(mismatches, "thread")
	}
	if fingerprintValuesDisagree(windowHeader, turnWindow, bodyWindow, bodyWindowHeader) {
		mismatches = append(mismatches, "window")
	}
	if fingerprintValuesDisagree(installationHeader, turnInstallation, bodyInstallation, bodyInstallationHeader) {
		mismatches = append(mismatches, "installation")
	}
	if fingerprintValuesDisagree(parentHeader, turnParent, bodyParent) {
		mismatches = append(mismatches, "parent")
	}

	obs := codexFingerprintObservation{
		Kind:                 clipFingerprintLabel(kind, 32),
		UAHash:               shortHashForLog(headers.Get("User-Agent")),
		Originator:           clipFingerprintLabel(headers.Get("Originator"), 64),
		Version:              clipFingerprintLabel(headers.Get("Version"), 64),
		SessionHash:          shortHashForLog(firstNonEmpty(sessionHeader, legacySession)),
		ThreadHash:           shortHashForLog(threadHeader),
		WindowHash:           shortHashForLog(windowHeader),
		RequestHash:          shortHashForLog(headers.Get(codexClientRequestIDHeader)),
		ParentHash:           shortHashForLog(parentHeader),
		InstallationHeader:   installationHeader != "",
		TurnInstallationHash: shortHashForLog(turnInstallation),
		TurnSessionHash:      shortHashForLog(turnSession),
		TurnThreadHash:       shortHashForLog(turnThread),
		TurnWindowHash:       shortHashForLog(turnWindow),
		TurnParentHash:       shortHashForLog(turnParent),
		BodyInstallationHash: shortHashForLog(firstNonEmpty(bodyInstallation, bodyInstallationHeader)),
		BodySessionHash:      shortHashForLog(bodySession),
		BodyThreadHash:       shortHashForLog(bodyThread),
		BodyWindowHash:       shortHashForLog(firstNonEmpty(bodyWindow, bodyWindowHeader)),
		BodyParentHash:       shortHashForLog(bodyParent),
		CacheKeyHash:         shortHashForLog(gjson.GetBytes(body, "prompt_cache_key").String()),
		Attestation:          attestation != "",
		AttestationHash:      shortHashForLog(attestation),
		LegacySession:        legacySession != "",
		LegacyConversation:   strings.TrimSpace(headers.Get("Conversation_id")) != "",
		Stainless: headers.Get("X-Stainless-Package-Version") != "" ||
			headers.Get("X-Stainless-Runtime-Version") != "" ||
			headers.Get("X-Stainless-Os") != "" ||
			headers.Get("X-Stainless-Arch") != "",
		Aligned:  len(mismatches) == 0,
		Mismatch: strings.Join(mismatches, ","),
	}
	return obs
}

func fingerprintValuesDisagree(values ...string) bool {
	baseline := ""
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if baseline == "" {
			baseline = value
			continue
		}
		if value != baseline {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func clipFingerprintLabel(value string, maxRunes int) string {
	value = strings.ToValidUTF8(strings.TrimSpace(value), "")
	if maxRunes <= 0 || utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	count := 0
	for i := range value {
		if count == maxRunes {
			return value[:i]
		}
		count++
	}
	return value
}

func (obs codexFingerprintObservation) jsonForUsageLog() string {
	raw, err := json.Marshal(obs)
	if err != nil {
		return ""
	}
	if len(raw) <= maxCodexFingerprintLogLength {
		return string(raw)
	}
	fallback, err := json.Marshal(codexFingerprintObservation{
		Kind:     obs.Kind,
		Aligned:  obs.Aligned,
		Mismatch: obs.Mismatch,
	})
	if err != nil || len(fallback) > maxCodexFingerprintLogLength {
		return ""
	}
	return string(fallback)
}

// recordCodexFingerprintObservation 把本次出站身份写入请求上下文，供用量日志取走。
// 详细多字段行仍只在 CODEX_FINGERPRINT_DEBUG 打开时打印。对不上时另打一行短日志，
// 里面同样只有哈希和冲突项名称。握手观察会被随后的 ws-new / ws-reused 覆盖，
// 因此握手这一次不单独打漂移行，避免同一次请求打两遍。
func recordCodexFingerprintObservation(ctx context.Context, kind string, accountID int64, headers http.Header, body []byte) codexFingerprintObservation {
	obs := buildCodexFingerprintObservation(kind, headers, body)
	recordCodexFingerprintAudit(ctx, obs.jsonForUsageLog())
	if obs.Mismatch != "" && kind != "ws-handshake" && !codexFingerprintDebugEnabled() {
		log.Printf("[CodexFingerprint] drift account_id=%d kind=%s mismatch=%s", accountID, obs.Kind, obs.Mismatch)
	}
	return obs
}
