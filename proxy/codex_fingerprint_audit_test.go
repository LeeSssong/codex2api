package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
)

func TestCodexFingerprintObservationHashesIdentityAndFlagsDisagreement(t *testing.T) {
	const (
		sessionA    = "session-raw-a"
		sessionB    = "session-raw-b"
		thread      = "thread-raw"
		windowA     = "window-raw-a"
		windowB     = "window-raw-b"
		install     = "install-raw"
		attestation = "v1.SECRET-ATTESTATION-TOKEN"
		prompt      = "do-not-log-this-prompt"
	)
	headers := http.Header{}
	headers.Set("User-Agent", "Codex Desktop/0.160.1")
	headers.Set("Originator", "Codex Desktop")
	headers.Set("Version", "0.160.1")
	headers.Set("Session-Id", sessionA)
	headers.Set("Thread-Id", thread)
	headers.Set("X-Codex-Window-Id", windowA)
	headers.Set("X-Codex-Turn-Metadata", `{"session_id":"`+sessionB+`","thread_id":"`+thread+`","window_id":"`+windowA+`","installation_id":"`+install+`"}`)
	body := []byte(`{"input":"` + prompt + `","prompt_cache_key":"cache-raw","client_metadata":{"session_id":"` + sessionA + `","thread_id":"` + thread + `","window_id":"` + windowB + `","installation_id":"` + install + `"}}`)
	headers.Set("X-Oai-Attestation", attestation)

	obs := buildCodexFingerprintObservation("http", headers, body)
	raw := obs.jsonForUsageLog()
	if obs.Aligned {
		t.Fatal("aligned = true, want false")
	}
	if obs.Mismatch != "session,window" {
		t.Fatalf("mismatch = %q, want session,window", obs.Mismatch)
	}
	if !obs.Attestation || obs.AttestationHash == "" {
		t.Fatalf("attestation = %v hash %q, want present", obs.Attestation, obs.AttestationHash)
	}
	if len(raw) == 0 || len(raw) > maxCodexFingerprintLogLength {
		t.Fatalf("usage log json length = %d", len(raw))
	}
	for _, secret := range []string{sessionA, sessionB, thread, windowA, windowB, install, attestation, prompt, "cache-raw"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("usage log json contains raw value %q: %s", secret, raw)
		}
	}
	if obs.SessionHash == "" || obs.SessionHash == obs.TurnSessionHash {
		t.Fatalf("session hashes should differ, header %q turn %q", obs.SessionHash, obs.TurnSessionHash)
	}
	if obs.ThreadHash == "" || obs.ThreadHash != obs.TurnThreadHash || obs.ThreadHash != obs.BodyThreadHash {
		t.Fatalf("thread hashes = header %q turn %q body %q", obs.ThreadHash, obs.TurnThreadHash, obs.BodyThreadHash)
	}
}

func TestCodexFingerprintObservationTreatsMissingInstallationHeaderAsAligned(t *testing.T) {
	headers := http.Header{}
	headers.Set("Session-Id", "same-session")
	headers.Set("Thread-Id", "same-thread")
	headers.Set("X-Codex-Window-Id", "same-window")
	headers.Set("X-Codex-Turn-Metadata", `{"session_id":"same-session","thread_id":"same-thread","window_id":"same-window","installation_id":"account-install"}`)
	body := []byte(`{"client_metadata":{"session_id":"same-session","thread_id":"same-thread","window_id":"same-window","installation_id":"account-install"}}`)

	obs := buildCodexFingerprintObservation("ws-new", headers, body)
	if !obs.Aligned || obs.Mismatch != "" {
		t.Fatalf("aligned=%v mismatch=%q, want aligned with no installation header", obs.Aligned, obs.Mismatch)
	}
	if obs.InstallationHeader || obs.TurnInstallationHash == "" || obs.BodyInstallationHash != obs.TurnInstallationHash {
		t.Fatalf("installation header %v turn %q body %q", obs.InstallationHeader, obs.TurnInstallationHash, obs.BodyInstallationHash)
	}
}

func TestUsageLogReceivesCodexFingerprintAndResetClearsIt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("CODEX_FINGERPRINT_DEBUG", "")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "client/1.0")
	attachUserAgentAudit(c)

	headers := http.Header{}
	headers.Set("Session-Id", "visible-session")
	headers.Set("X-Oai-Attestation", "v1.visible-attestation")
	LogCodexFingerprintDebug(c.Request.Context(), "http", &auth.Account{DBID: 74}, "", headers, []byte(`{"prompt_cache_key":"visible-cache"}`))

	input := &database.UsageLogInput{}
	populateUserAgentMetaFromRequest(c, input)
	if !strings.Contains(input.CodexFingerprint, `"aligned":true`) || !strings.Contains(input.CodexFingerprint, `"attestation":true`) {
		t.Fatalf("CodexFingerprint = %s", input.CodexFingerprint)
	}
	if strings.Contains(input.CodexFingerprint, "visible-session") || strings.Contains(input.CodexFingerprint, "visible-attestation") || strings.Contains(input.CodexFingerprint, "visible-cache") {
		t.Fatalf("CodexFingerprint leaked raw identity: %s", input.CodexFingerprint)
	}

	resetUpstreamUserAgentAudit(c.Request.Context())
	cleared := &database.UsageLogInput{}
	populateUserAgentMetaFromRequest(c, cleared)
	if cleared.CodexFingerprint != "" {
		t.Fatalf("CodexFingerprint after reset = %s", cleared.CodexFingerprint)
	}
}
