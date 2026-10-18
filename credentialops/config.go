package credentialops

import (
	"encoding/base32"
	"errors"
	"net/mail"
	"net/url"
	"strings"
)

// PrepareConfig merges edits into a durable encrypted snapshot. Empty secrets
// preserve existing values; explicit clear operations never preserve them.
func PrepareConfig(in LoginConfigInput, old LoginConfig) (LoginConfig, error) {
	out := old
	out.AccountID = in.AccountID
	if in.ProxyID > 0 {
		out.ProxyID = in.ProxyID
	}
	if in.Email != "" {
		out.Email = strings.ToLower(strings.TrimSpace(in.Email))
	}
	if in.Mode != "" {
		out.Mode = in.Mode
	}
	if out.Mode == "" {
		out.Mode = "password_totp"
	}
	if in.Engine != "" {
		out.Engine = in.Engine
	}
	if out.Engine == "" {
		out.Engine = "inherit"
	}
	if in.ProxySource != "" {
		out.ProxySource = in.ProxySource
	}
	if out.ProxySource == "" {
		out.ProxySource = "account"
	}
	if out.ProxySource != "proxy" {
		out.ProxyID = 0
	}
	address, err := mail.ParseAddress(out.Email)
	if err != nil || address.Address != out.Email {
		return out, errors.New("valid login email is required")
	}
	if out.Mode != "password_totp" && out.Mode != "email_otp_url" {
		return out, errors.New("invalid credential mode")
	}
	if out.Engine != "local_worker" && out.Engine != "session_studio" && out.Engine != "inherit" {
		return out, errors.New("invalid login engine")
	}
	if out.Mode == "email_otp_url" && out.Engine == "inherit" {
		out.Engine = "local_worker"
	}
	if out.Mode == "email_otp_url" && out.Engine != "local_worker" {
		return out, errors.New("email OTP requires local worker")
	}
	if out.Engine == "session_studio" && out.ProxySource != "direct" {
		return out, errors.New("Session Studio requires direct proxy mode")
	}
	if out.ProxySource != "account" && out.ProxySource != "global" && out.ProxySource != "direct" && out.ProxySource != "proxy" && out.ProxySource != "mihomo" {
		return out, errors.New("invalid proxy source")
	}
	if in.TOTPSecret != "" {
		secret := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(in.TOTPSecret), " ", ""))
		raw, e := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.TrimRight(secret, "="))
		if e != nil || len(raw) < 10 {
			return out, errors.New("invalid TOTP secret")
		}
		in.TOTPSecret = secret
	}
	if in.OTPURL != "" {
		if u, e := url.Parse(in.OTPURL); e != nil || u.Scheme != "https" || u.Host == "" {
			return out, errors.New("invalid OTP URL")
		}
	}
	if out.ProxySource == "proxy" && out.ProxyID <= 0 {
		return out, errors.New("proxy selection is required")
	}
	enc, _ := EnvCrypto()
	// Check availability even if edits contain no secret.
	if _, err := enc("credential-ops-check"); err != nil {
		return out, err
	}
	if in.ClearPassword {
		out.PasswordCiphertext = ""
	}
	if in.ClearTOTP {
		out.TOTPCiphertext = ""
	}
	if in.ClearOTPURL {
		out.OTPURLCiphertext = ""
	}
	if in.Password != "" {
		out.PasswordCiphertext, err = enc(in.Password)
		if err != nil {
			return out, err
		}
	}
	if in.TOTPSecret != "" {
		out.TOTPCiphertext, err = enc(in.TOTPSecret)
		if err != nil {
			return out, err
		}
	}
	if in.OTPURL != "" {
		out.OTPURLCiphertext, err = enc(in.OTPURL)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}
