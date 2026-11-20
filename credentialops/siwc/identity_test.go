package siwc

import (
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestRefreshIdentityWithoutNewIDToken(t *testing.T) {
	bound := Identity{Subject: "original", Email: "person@example.com"}
	now := time.Now()
	got, err := VerifyRefreshIdentity("issued", "", "chatgpt.tokens.use.direct", nil, bound, now)
	if err != nil || got != bound {
		t.Fatal("refresh did not preserve bound identity")
	}
	if _, err := VerifyRefreshIdentity("issued", "", "openid", nil, bound, now); err == nil {
		t.Fatal("refresh accepted removed permission")
	}
	if _, err := VerifyRefreshIdentity("issued", "invalid", "chatgpt.tokens.use.direct", nil, bound, now); err == nil {
		t.Fatal("refresh ignored invalid replacement ID token")
	}
	if _, err := VerifyRefreshIdentity("issued", "", "chatgpt.tokens.use.direct", nil, Identity{}, now); err == nil {
		t.Fatal("refresh created a binding without identity")
	}
}

func TestBrowserIdentityVerification(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1800000000, 0)
	input := ExchangeInput{ClientID: "issued", Nonce: "nonce"}
	keys := map[string]*rsa.PublicKey{"test": &key.PublicKey}
	for _, name := range []string{"valid", "expired", "wrong issuer", "wrong client", "wrong nonce", "wrong email", "wrong subject", "missing permission", "unknown key", "ambiguous audience", "invalid signature"} {
		t.Run(name, func(t *testing.T) {
			claims := identityClaims{RegisteredClaims: jwt.RegisteredClaims{Issuer: "https://auth.openai.com", Subject: "subscriber", Audience: jwt.ClaimStrings{"issued"}, ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour))}, Nonce: "nonce", Email: "person@example.com"}
			scope, kid := "resource.invoke chatgpt.tokens.use.direct", "test"
			switch name {
			case "expired":
				claims.ExpiresAt = jwt.NewNumericDate(now)
			case "wrong issuer":
				claims.Issuer = "https://example.com"
			case "wrong client":
				claims.Audience = jwt.ClaimStrings{"other"}
			case "wrong nonce":
				claims.Nonce = "other"
			case "wrong email":
				claims.Email = "other@example.com"
			case "wrong subject":
				claims.Subject = "other"
			case "missing permission":
				scope = "chatgpt.tokens.use.direct.extra"
			case "unknown key":
				kid = "other"
			case "ambiguous audience":
				claims.Audience = append(claims.Audience, "other")
			}
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
			token.Header["kid"] = kid
			raw, err := token.SignedString(key)
			if err != nil {
				t.Fatal(err)
			}
			if name == "invalid signature" {
				raw += "corrupt"
			}
			id, err := input.VerifyIdentity(raw, scope, keys, Identity{Subject: "subscriber", Email: "PERSON@example.com"}, now)
			if name == "valid" {
				if err != nil || id.Subject != "subscriber" {
					t.Fatal("valid identity rejected", err)
				}
			} else if err == nil {
				t.Fatal("invalid identity accepted")
			}
		})
	}
}
