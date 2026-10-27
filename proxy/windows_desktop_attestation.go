package proxy

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/codex2api/auth"
)

const (
	windowsDesktopAttestationHeader = "X-Oai-Attestation"
	windowsDesktopVersion           = 1
	windowsDesktopUnavailableStatus = 1
)

// ApplyWindowsDesktopAttestation 为 Windows Codex Desktop 补上自洽的「不可用」证明。
// Windows 没有 DeviceCheck，所以不附带令牌。成功状态配一份网关伪造的令牌，比明确不可用更像伪装。
// 已有下游证明或账号自定义证明保持优先。macOS 桌面走真 DeviceCheck，这里不模拟。
func ApplyWindowsDesktopAttestation(headers http.Header, account *auth.Account) {
	if headers == nil || account == nil || strings.TrimSpace(headers.Get(windowsDesktopAttestationHeader)) != "" {
		return
	}
	userAgent := strings.TrimSpace(headers.Get("User-Agent"))
	if !strings.EqualFold(codexUserAgentClientName(userAgent), "Codex Desktop") || !strings.Contains(userAgent, "(Windows ") {
		return
	}
	header, err := json.Marshal(struct {
		Version int `json:"v"`
		Status  int `json:"s"`
	}{Version: windowsDesktopVersion, Status: windowsDesktopUnavailableStatus})
	if err == nil {
		headers.Set(windowsDesktopAttestationHeader, string(header))
	}
}
