package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// WorkstationCredentialKey 是账号凭据里的工作站快照。
// 它只记录这台账号对外已经在用的身份和出口，不重新抽设备、不改 UA。
const WorkstationCredentialKey = "workstation"

// WorkstationHoldReason 是出站身份或出口与工作站记录对不上时的暂停原因。
// 调度把这种账号当成错误态退出放量，不改管理员的启用开关。
const WorkstationHoldReason = "workstation identity diverged"

// Workstation 是一个 Codex 官方账号对外的一台电脑。
// 字段一旦写入就固定；后续请求、版本同步和代理池都不能把它换成另一套。
type Workstation struct {
	Schema            int    `json:"schema"`
	FingerprintMode   string `json:"fingerprint_mode"`
	InstallationID    string `json:"installation_id,omitempty"`
	DeviceID          string `json:"device_id,omitempty"`
	Timezone          string `json:"timezone,omitempty"`
	UserAgent         string `json:"user_agent,omitempty"`
	Originator        string `json:"originator,omitempty"`
	Version           string `json:"version,omitempty"`
	EgressKind        string `json:"egress_kind,omitempty"`
	EgressHost        string `json:"egress_host,omitempty"`
	PinnedFingerprint bool   `json:"pinned_fingerprint"`
	PinnedEgress      bool   `json:"pinned_egress"`
	// VersionAdoptedAtUnix 是这台工作站采用当前 Version 的时刻。
	// 版本同步按账号把升级摊开，避免整池在同一秒换成新版本。
	VersionAdoptedAtUnix int64 `json:"version_adopted_at_unix,omitempty"`
}

func workstationFromCredential(raw string) Workstation {
	ws, _ := ParseWorkstation(raw)
	return ws
}

// StoredWorkstation 返回已落库的工作站记录。
func (a *Account) StoredWorkstation() (Workstation, bool) {
	if a == nil {
		return Workstation{}, false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.WorkstationKnown {
		return Workstation{}, false
	}
	return a.Workstation, true
}

// RememberWorkstation 把刚写入的工作站记录同步到内存，避免本次进程再对空记录放行。
func (a *Account) RememberWorkstation(ws Workstation) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.Workstation = ws
	a.WorkstationKnown = true
	a.mu.Unlock()
}

// ParseWorkstation 解析已落库的工作站记录。没有记录或记录损坏时返回空值和 false。
func ParseWorkstation(raw string) (Workstation, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Workstation{}, false
	}
	var ws Workstation
	if err := json.Unmarshal([]byte(raw), &ws); err != nil || ws.Schema == 0 {
		return Workstation{}, false
	}
	return ws, true
}

const workstationSchema = 1

// WorkstationRequiresDedicatedEgress 报告这个官方 Codex 账号是否必须自带出口。
// 中转、Claude、Grok 不走这条门。显式 off 是透传档，保留原出口策略。
func (a *Account) WorkstationRequiresDedicatedEgress() bool {
	if a.PinnedIPv6IP() != "" {
		return true
	}
	if a == nil || a.IsRelayStyle() {
		return false
	}
	return a.EffectiveCodexFingerprintMode() != CodexFingerprintModeOff
}

// HasDedicatedEgress 报告账号是否有一条不会退回宿主机直连、也不会借别的池的出口。
// 账号代理、已开启的原生 IPv6、已登记的固定出口都算。分组代理、全局代理和 Resin 不算。
func (a *Account) HasDedicatedEgress() bool {
	if a == nil {
		return false
	}
	if strings.TrimSpace(a.GetProxyURL()) != "" {
		return true
	}
	_, ipv6 := a.IPv6EgressSelection()
	if ipv6 {
		return true
	}
	if a.HostIPv4Egress() {
		return false
	}
	return fixedEgressEnrolled != nil && fixedEgressEnrolled(a.ID())
}

// fixedEgressEnrolled 由 fixedegress 包在初始化时挂上，避免 auth 反向依赖它。
var fixedEgressEnrolled func(accountID int64) bool

// SetFixedEgressEnrollment 注册「账号是否已加入固定出口」的查询。
func SetFixedEgressEnrollment(fn func(accountID int64) bool) {
	fixedEgressEnrolled = fn
}

// dedicatedEgressRequired 由账号管理器注册，供出口层按账号编号判断要不要拒绝直连。
var dedicatedEgressRequired func(accountID int64) bool

// SetDedicatedEgressGate 注册「这个账号是否必须守住专属出口」。
func SetDedicatedEgressGate(fn func(accountID int64) bool) {
	dedicatedEgressRequired = fn
}

// DedicatedEgressRequired 报告账号编号对应的运行时账号是否必须守住专属出口。
func DedicatedEgressRequired(accountID int64) bool {
	return dedicatedEgressRequired != nil && accountID != 0 && dedicatedEgressRequired(accountID)
}

// workstationHolder 由账号管理器注册，出站对账失败时调用。
var workstationHolder func(account *Account, reason string)

// SetWorkstationHolder 注册对账失败后的暂停动作。
func SetWorkstationHolder(fn func(account *Account, reason string)) {
	workstationHolder = fn
}

// HoldDivergedWorkstation 暂停对账失败的账号。未注册时只留给调用方返回错误。
func HoldDivergedWorkstation(account *Account, reason string) {
	if workstationHolder != nil {
		workstationHolder(account, reason)
	}
}

// WorkstationSnapshot 从账号当前对外身份生成一份不改值的快照。
func (a *Account) WorkstationSnapshot() Workstation {
	ws := Workstation{Schema: workstationSchema}
	if a == nil {
		return ws
	}
	a.mu.RLock()
	mode := NormalizeCodexFingerprintMode(a.CodexFingerprintMode)
	accountID := strings.TrimSpace(a.AccountID)
	dbID := a.DBID
	tz := strings.TrimSpace(a.Timezone)
	proxy := strings.TrimSpace(a.ProxyURL)
	ipv6 := a.IPv6EgressEnabled && proxy == ""
	a.mu.RUnlock()

	ws.FingerprintMode = mode
	ws.PinnedFingerprint = mode != CodexFingerprintModeOff
	ws.Timezone = tz
	if ws.PinnedFingerprint && !a.isRelayStyleLocked() {
		ws.InstallationID = deriveStableWorkstationUUID(dbID)
	}
	if a.isClaudeOAuthLocked() {
		ws.DeviceID = deriveStableWorkstationDevice(accountID, dbID)
	}
	switch {
	case proxy != "":
		ws.EgressKind = "account_proxy"
		ws.EgressHost = proxyHost(proxy)
		ws.PinnedEgress = true
	case ipv6:
		ws.EgressKind = "local_ipv6"
		ws.PinnedEgress = true
	case fixedEgressEnrolled != nil && fixedEgressEnrolled(dbID):
		ws.EgressKind = "fixed"
		ws.PinnedEgress = true
	default:
		ws.EgressKind = "unbound"
	}
	return ws
}

func deriveStableWorkstationUUID(accountID int64) string {
	sum := sha256.Sum256([]byte("codex2api:codex-install-id:v1:" + itoa(accountID)))
	sum[6] = (sum[6] & 0x0f) | 0x40
	sum[8] = (sum[8] & 0x3f) | 0x80
	b := sum[:16]
	return hex.EncodeToString(b[0:4]) + "-" + hex.EncodeToString(b[4:6]) + "-" + hex.EncodeToString(b[6:8]) + "-" + hex.EncodeToString(b[8:10]) + "-" + hex.EncodeToString(b[10:16])
}

func deriveStableWorkstationDevice(accountUUID string, dbID int64) string {
	seed := strings.TrimSpace(accountUUID)
	if seed == "" {
		seed = "db-" + itoa(dbID)
	}
	sum := sha256.Sum256([]byte("claude-code-device:" + seed))
	return hex.EncodeToString(sum[:])
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func proxyHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if i := strings.Index(raw, "://"); i >= 0 {
		raw = raw[i+3:]
	}
	if i := strings.Index(raw, "@"); i >= 0 {
		raw = raw[i+1:]
	}
	if i := strings.Index(raw, "/"); i >= 0 {
		raw = raw[:i]
	}
	return raw
}
