package proxy

import (
	"net/http"
	"testing"

	"github.com/codex2api/auth"
)

func TestWorkstationAuditHoldsDivergentIdentity(t *testing.T) {
	account := &auth.Account{
		DBID:             9,
		WorkstationKnown: true,
		Workstation: auth.Workstation{
			Schema:            1,
			FingerprintMode:   auth.CodexFingerprintModeSingleMachineMultiWindow,
			InstallationID:    "install-kept",
			UserAgent:         "codex-tui/0.160.0 (Linux Unknown; x86_64) xterm-256color (codex-tui; 0.160.0)",
			Originator:        "codex-tui",
			Version:           "0.160.0",
			Timezone:          "Asia/Shanghai",
			EgressKind:        "account_proxy",
			EgressHost:        "203.0.113.10:8080",
			PinnedFingerprint: true,
			PinnedEgress:      true,
		},
	}
	headers := http.Header{}
	headers.Set("User-Agent", account.Workstation.UserAgent)
	headers.Set("Originator", "codex-tui")
	headers.Set("Version", "0.160.0")
	headers.Set("X-Codex-Turn-Metadata", `{"installation_id":"install-kept"}`)
	body := []byte(`{"input":"<timezone>Asia/Shanghai</timezone>"}`)
	if reason := checkWorkstationOutbound(account, headers, body, "proxy", "203.0.113.10:8080"); reason != "" {
		t.Fatalf("matching workstation diverged: %s", reason)
	}
	headers.Set("User-Agent", "codex-tui/0.161.0 (Mac OS 15.5.0; arm64) xterm-256color (codex-tui; 0.161.0)")
	if reason := checkWorkstationOutbound(account, headers, body, "direct", ""); reason == "" {
		t.Fatal("changed user agent and direct egress were sent")
	}
	plain := &auth.Account{DBID: 10, CodexFingerprintMode: auth.CodexFingerprintModeOff}
	if reason := checkWorkstationOutbound(plain, headers, body, "direct", ""); reason != "" {
		t.Fatalf("account without a record was held: %s", reason)
	}
}
