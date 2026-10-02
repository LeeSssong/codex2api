package credentialops

import (
	"errors"
	"strings"
	"testing"
)

func TestCredentialOpsConfigEncryptionAndExplicitClears(t *testing.T) {
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "")
	if _, err := PrepareConfig(LoginConfigInput{Email: "a@example.com", Password: "test-password"}, LoginConfig{}); !errors.Is(err, ErrEncryptionUnavailable) {
		t.Fatalf("missing key %v", err)
	}
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", strings.Repeat("k", 32))
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
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", strings.Repeat("x", 32))
	_, dec = EnvCrypto()
	if _, err = dec(cfg.PasswordCiphertext); err == nil {
		t.Fatal("wrong key decrypted secret")
	}
}
func TestCredentialOpsConfigEngineValidation(t *testing.T) {
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", strings.Repeat("k", 32))
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
