package credentialops

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCredentialOpsConfigEncryptionAndExplicitClears(t *testing.T) {
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY_FILE", filepath.Join(t.TempDir(), "missing.key"))
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "")
	if _, err := PrepareConfig(LoginConfigInput{Email: "a@example.com", Password: "test-password"}, LoginConfig{}); !errors.Is(err, ErrEncryptionUnavailable) {
		t.Fatalf("missing key %v", err)
	}
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	cfg, err := PrepareConfig(LoginConfigInput{Email: "A@example.com", Password: "test-password", TOTPSecret: "JBSWY3DPEHPK3PXP"}, LoginConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Email != "a@example.com" || !strings.HasPrefix(cfg.PasswordCiphertext, "enc:v1:") {
		t.Fatal("secret stored without encryption")
	}
	preserved, err := PrepareConfig(LoginConfigInput{}, cfg)
	if err != nil || preserved.PasswordCiphertext != cfg.PasswordCiphertext {
		t.Fatal("empty edit discarded secret")
	}
	cleared, err := PrepareConfig(LoginConfigInput{ClearPassword: true, ClearTOTP: true}, cfg)
	if err != nil || cleared.PasswordCiphertext != "" || cleared.TOTPCiphertext != "" {
		t.Fatal("explicit clear preserved secret")
	}
	_, dec := EnvCrypto()
	plain, err := dec(cfg.PasswordCiphertext)
	if err != nil || plain != "test-password" {
		t.Fatal("secret cannot be decrypted")
	}
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHg=")
	_, dec = EnvCrypto()
	if _, err = dec(cfg.PasswordCiphertext); err == nil {
		t.Fatal("wrong key decrypted secret")
	}
}
func TestCredentialOpsConfigEngineValidation(t *testing.T) {
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	invalid := []LoginConfigInput{{Email: "a@example.com", TOTPSecret: "invalid*"}, {Email: "a@example.com", Mode: "unknown"}, {Email: "a@example.com", Engine: "session_studio", ProxySource: "account"}, {Email: "a@example.com", Mode: "email_otp_url", Engine: "session_studio", ProxySource: "direct"}, {Email: "a@example.com", OTPURL: "http://unsafe.example/inbox"}}
	for _, in := range invalid {
		if _, err := PrepareConfig(in, LoginConfig{}); err == nil {
			t.Fatalf("accepted invalid config %#v", in)
		}
	}
	cfg, err := PrepareConfig(LoginConfigInput{Email: "a@example.com", Mode: "email_otp_url", OTPURL: "https://mail.example/inbox"}, LoginConfig{})
	if err != nil || cfg.OTPURLCiphertext == "" {
		t.Fatal(err)
	}
	cleared, err := PrepareConfig(LoginConfigInput{ClearOTPURL: true}, cfg)
	if err != nil || cleared.OTPURLCiphertext != "" {
		t.Fatal("OTP URL clear failed")
	}
}

func TestCredentialOperationsExcelProfileChecks(t *testing.T) {
	claims := func(v map[string]any) string {
		raw, _ := json.Marshal(v)
		return "e30." + base64.RawURLEncoding.EncodeToString(raw) + ".signature"
	}
	credentials := map[string]any{"client_id": ExcelClientID, "refresh_token": "rt", "access_token": claims(map[string]any{"client_id": ExcelClientID, "exp": time.Now().Add(time.Hour).Unix(), "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "ws", "chatgpt_user_id": "user"}}), "id_token": claims(map[string]any{"aud": ExcelClientID, "email": "excel@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_user_id": "user"}})}
	email, workspace, e := ValidateExcelCredentials(credentials, "excel@example.com", "ws")
	if e != nil || email != "excel@example.com" || workspace != "ws" {
		t.Fatal(e)
	}
	credentials["client_id"] = "codex"
	if _, _, e = ValidateExcelCredentials(credentials, "excel@example.com", "ws"); e == nil {
		t.Fatal("Codex grant accepted as Excel")
	}
}

func TestCredentialOperationsProbeClassificationDoesNotReloginForPlanOrNetworkFailures(t *testing.T) {
	for _, tc := range []struct {
		message string
		want    string
	}{{"codex models upstream status 401: expired", "auth"}, {"codex models upstream status 403: model unauthorized for plan", "transient"}, {"codex models upstream status 403: Cloudflare challenge", "transient"}, {"codex models upstream status 403: invalid_token", "auth"}, {"429 rate limit", "transient"}, {"proxy timeout", "transient"}, {"status 500 upstream unavailable", "transient"}} {
		if got := ClassifyProbe(true, errors.New(tc.message)); got != tc.want {
			t.Fatalf("%s = %s, want %s", tc.message, got, tc.want)
		}
	}
	if got := ClassifyProbe(false, errors.New("network")); got != "auth" {
		t.Fatal("missing token not classified as auth")
	}
}

func TestCredentialOperationsCodexCallbackRejectsExcelClient(t *testing.T) {
	token := func(v map[string]any) string {
		raw, _ := json.Marshal(v)
		return "e30." + base64.RawURLEncoding.EncodeToString(raw) + ".signature"
	}
	good := map[string]any{"access_token": token(map[string]any{"client_id": "app_EMoamEEZ73f0CkXaXp7hrann"}), "id_token": token(map[string]any{"aud": "app_EMoamEEZ73f0CkXaXp7hrann"})}
	if e := ValidateCodexClient(good); e != nil {
		t.Fatal(e)
	}
	good["access_token"] = token(map[string]any{"client_id": ExcelClientID})
	if e := ValidateCodexClient(good); e == nil {
		t.Fatal("Excel access token accepted for Codex profile")
	}
}

func TestCredentialOperationsSwitchingProxySourceClearsStaticSelection(t *testing.T) {
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	old, e := PrepareConfig(LoginConfigInput{Email: "proxy@example.test", Password: "secret", ProxySource: "proxy", ProxyID: 9}, LoginConfig{})
	if e != nil {
		t.Fatal(e)
	}
	for _, source := range []string{"direct", "account", "mihomo", "global"} {
		changed, e := PrepareConfig(LoginConfigInput{ProxySource: source}, old)
		if e != nil {
			t.Fatal(e)
		}
		if changed.ProxySource != source || changed.ProxyID != 0 {
			t.Fatalf("source %s retained stale static selector: %#v", source, changed)
		}
	}
}
