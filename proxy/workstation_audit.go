package proxy

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/codex2api/auth"
	"github.com/tidwall/gjson"
)

// workstationHoldsDirect 报告这个账号的绑定出口失败后不能改走宿主机。
func workstationHoldsDirect(accountID int64) bool {
	return auth.DedicatedEgressRequired(accountID)
}

// checkWorkstationOutbound 对比这次即将发出的身份和已保存的工作站记录。
// 没有记录时放行。记录里钉住的字段不一致时返回原因，调用方不得发出这次请求。
func checkWorkstationOutbound(account *auth.Account, headers http.Header, body []byte, egressKind, egressHost string) string {
	ws, ok := account.StoredWorkstation()
	if !ok || !ws.PinnedFingerprint {
		return ""
	}
	if headers == nil {
		headers = http.Header{}
	}
	var reasons []string
	if ws.UserAgent != "" && strings.TrimSpace(headers.Get("User-Agent")) != ws.UserAgent {
		reasons = append(reasons, "user_agent")
	}
	if ws.Originator != "" && strings.TrimSpace(headers.Get("Originator")) != ws.Originator {
		reasons = append(reasons, "originator")
	}
	if ws.Version != "" && headerVersion(headers) != "" && headerVersion(headers) != ws.Version {
		reasons = append(reasons, "version")
	}
	if ws.InstallationID != "" {
		sent := sentInstallationID(headers, body)
		if sent != "" && sent != ws.InstallationID {
			reasons = append(reasons, "installation")
		}
	}
	if ws.Timezone != "" {
		if sent := sentTimezone(body); sent != "" && sent != ws.Timezone {
			reasons = append(reasons, "timezone")
		}
	}
	if ws.PinnedEgress {
		switch egressKind {
		case "", "direct", "resin", "unbound":
			reasons = append(reasons, "egress")
		default:
			if ws.EgressHost != "" && egressHost != "" && !sameEgressHost(egressHost, ws.EgressHost) {
				reasons = append(reasons, "egress")
			}
		}
	}
	return strings.Join(reasons, ",")
}

func sameEgressHost(sent, recorded string) bool {
	sent = strings.TrimSpace(sent)
	recorded = strings.TrimSpace(recorded)
	return strings.EqualFold(sent, recorded) || strings.EqualFold(hostWithoutPort(sent), hostWithoutPort(recorded))
}

func hostWithoutPort(host string) string {
	if i := strings.LastIndex(host, ":"); i > 0 && !strings.Contains(host, "]") {
		return host[:i]
	}
	return host
}

func headerVersion(headers http.Header) string {
	if headers == nil {
		return ""
	}
	if version := strings.TrimSpace(headers.Get("Version")); version != "" {
		return version
	}
	ua := strings.TrimSpace(headers.Get("User-Agent"))
	if i := strings.Index(ua, "/"); i > 0 {
		rest := ua[i+1:]
		if j := strings.IndexAny(rest, " \t"); j > 0 {
			return rest[:j]
		}
	}
	return ""
}

func sentInstallationID(headers http.Header, body []byte) string {
	if headers != nil {
		if id := strings.TrimSpace(headers.Get(codexInstallationIDHeader)); id != "" {
			return id
		}
		turn := gjson.Parse(headers.Get(codexTurnMetadataHeader))
		if id := strings.TrimSpace(turn.Get("installation_id").String()); id != "" {
			return id
		}
	}
	if id := strings.TrimSpace(gjson.GetBytes(body, "client_metadata.installation_id").String()); id != "" {
		return id
	}
	return strings.TrimSpace(gjson.GetBytes(body, "client_metadata.x-codex-installation-id").String())
}

func sentTimezone(body []byte) string {
	text := gjson.GetBytes(body, "input").Raw
	if text == "" {
		text = string(body)
	}
	const open = "<timezone>"
	const closeTag = "</timezone>"
	start := strings.Index(text, open)
	if start < 0 {
		return ""
	}
	start += len(open)
	end := strings.Index(text[start:], closeTag)
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(text[start : start+end])
}

// EnforceWorkstationRequest 在请求字节发出前对账。对不上时暂停账号并返回错误。
func EnforceWorkstationRequest(account *auth.Account, egress CodexEgress, headers http.Header, body []byte) error {
	host := ""
	if egress.DialProxyURL != "" {
		if parsed, err := urlParseHost(egress.DialProxyURL); err == nil {
			host = parsed
		}
	}
	return enforceWorkstationOutbound(account, headers, body, string(egress.Kind), host)
}

func urlParseHost(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil {
		return "", err
	}
	return u.Host, nil
}

// enforceWorkstationOutbound 在请求字节发出前对账。
// 对不上时暂停该账号并返回错误，调用方据此放弃这次发送。
func enforceWorkstationOutbound(account *auth.Account, headers http.Header, body []byte, egressKind, egressHost string) error {
	reason := checkWorkstationOutbound(account, headers, body, egressKind, egressHost)
	if reason == "" {
		return nil
	}
	auth.HoldDivergedWorkstation(account, auth.WorkstationHoldReason+": "+reason)
	return errWorkstationDiverged(reason)
}

type errWorkstationDiverged string

func (e errWorkstationDiverged) Error() string {
	return "workstation identity diverged: " + string(e)
}
