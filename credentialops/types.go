// Package credentialops validates and encrypts login configuration for the
// native database-backed credential operations service.
package credentialops

import "errors"

var ErrEncryptionUnavailable = errors.New("credential encryption is unavailable")

type LoginConfigInput struct {
	AccountID                             int64
	Email, Mode, Engine, ProxySource      string
	Password, TOTPSecret, OTPURL          string
	ClearPassword, ClearTOTP, ClearOTPURL bool
}

type LoginConfig struct {
	AccountID                                            int64
	Email, Mode, Engine, ProxySource                     string
	PasswordCiphertext, TOTPCiphertext, OTPURLCiphertext string
}
