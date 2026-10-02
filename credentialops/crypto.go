package credentialops

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"strings"
)

func EnvCrypto() (func(string) (string, error), func(string) (string, error)) {
	keyText := strings.TrimSpace(os.Getenv("CODEX2API_CREDENTIAL_OPS_KEY"))
	if keyText == "" {
		return func(string) (string, error) { return "", ErrEncryptionUnavailable }, func(string) (string, error) { return "", ErrEncryptionUnavailable }
	}
	sum := sha256.Sum256([]byte(keyText))
	key := sum[:]
	enc := func(v string) (string, error) {
		b, e := aes.NewCipher(key)
		if e != nil {
			return "", e
		}
		g, e := cipher.NewGCM(b)
		if e != nil {
			return "", e
		}
		n := make([]byte, g.NonceSize())
		if _, e = io.ReadFull(rand.Reader, n); e != nil {
			return "", e
		}
		return "enc:v1:" + base64.RawURLEncoding.EncodeToString(g.Seal(n, n, []byte(v), nil)), nil
	}
	dec := func(v string) (string, error) {
		if !strings.HasPrefix(v, "enc:v1:") {
			return "", errors.New("invalid encrypted credential")
		}
		raw, e := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(v, "enc:v1:"))
		if e != nil {
			return "", e
		}
		b, e := aes.NewCipher(key)
		if e != nil {
			return "", e
		}
		g, e := cipher.NewGCM(b)
		if e != nil {
			return "", e
		}
		if len(raw) < g.NonceSize() {
			return "", errors.New("invalid encrypted credential")
		}
		plain, err := g.Open(nil, raw[:g.NonceSize()], raw[g.NonceSize():], nil)
		return string(plain), err
	}
	return enc, dec
}
