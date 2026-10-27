package proxy

import (
	"net/http"
	"strings"
	"testing"

	"github.com/codex2api/auth"
)

func TestWindowsDesktopAttestationIsUnavailableWithoutToken(t *testing.T) {
	headers := http.Header{}
	headers.Set("User-Agent", "Codex Desktop/0.164.0 (Windows 10.0.19045; x86_64) Codex Desktop (Codex Desktop; 26.1021.31345)")
	ApplyWindowsDesktopAttestation(headers, &auth.Account{DBID: 78})
	got := headers.Get("X-Oai-Attestation")
	if got != `{"v":1,"s":1}` {
		t.Fatalf("attestation = %s, want unavailable without a token", got)
	}
	if strings.Contains(got, `"t"`) || strings.Contains(got, "error_code") {
		t.Fatalf("unavailable attestation still carries a token: %s", got)
	}

	mac := http.Header{}
	mac.Set("User-Agent", "Codex Desktop/0.164.0 (Mac OS 26.5.2; arm64) Codex Desktop (Codex Desktop; 26.1021.31345)")
	ApplyWindowsDesktopAttestation(mac, &auth.Account{DBID: 78})
	if mac.Get("X-Oai-Attestation") != "" {
		t.Fatal("macOS desktop fabricated an attestation")
	}

	tui := http.Header{}
	tui.Set("User-Agent", "codex-tui/0.164.0 (Ubuntu 22.4.0; x86_64) xterm-256color (codex-tui; 0.164.0)")
	ApplyWindowsDesktopAttestation(tui, &auth.Account{DBID: 78})
	if tui.Get("X-Oai-Attestation") != "" {
		t.Fatal("tui fabricated an attestation")
	}

	passthrough := http.Header{"X-Oai-Attestation": []string{"v1.real-devicecheck-token"}}
	passthrough.Set("User-Agent", headers.Get("User-Agent"))
	ApplyWindowsDesktopAttestation(passthrough, &auth.Account{DBID: 78})
	if passthrough.Get("X-Oai-Attestation") != "v1.real-devicecheck-token" {
		t.Fatal("downstream attestation was replaced")
	}
}
