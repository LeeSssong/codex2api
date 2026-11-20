package siwc

import (
	"crypto/rsa"
	"crypto/subtle"
	"errors"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type Identity struct {
	Subject string
	Email   string
}

type identityClaims struct {
	jwt.RegisteredClaims
	Nonce           string `json:"nonce"`
	Email           string `json:"email"`
	AuthorizedParty string `json:"azp"`
}

func (input ExchangeInput) VerifyIdentity(raw, scope string, keys map[string]*rsa.PublicKey, expected Identity, now time.Time) (Identity, error) {
	if input.Nonce == "" {
		return Identity{}, errors.New("missing SIWC authorization nonce")
	}
	return verifyIdentity(input.ClientID, input.Nonce, raw, scope, keys, expected, now)
}

func VerifyRefreshIdentity(clientID, raw, scope string, keys map[string]*rsa.PublicKey, bound Identity, now time.Time) (Identity, error) {
	if strings.TrimSpace(bound.Subject) == "" || strings.TrimSpace(bound.Email) == "" {
		return Identity{}, errors.New("missing bound SIWC identity")
	}
	if raw == "" {
		if clientID == "" || clientID == "dynamic_agent_client" || !hasDirectPermission(scope) {
			return Identity{}, errors.New("invalid SIWC refresh client or permission")
		}
		return bound, nil
	}
	return verifyIdentity(clientID, "", raw, scope, keys, bound, now)
}

func hasDirectPermission(scope string) bool {
	for _, permission := range strings.Fields(scope) {
		if permission == "chatgpt.tokens.use.direct" {
			return true
		}
	}
	return false
}

func verifyIdentity(clientID, nonce, raw, scope string, keys map[string]*rsa.PublicKey, expected Identity, now time.Time) (Identity, error) {
	invalid := errors.New("invalid SIWC identity or permission")
	if !hasDirectPermission(scope) || clientID == "" || clientID == "dynamic_agent_client" || len(raw) > 32768 {
		return Identity{}, invalid
	}
	claims := new(identityClaims)
	_, err := jwt.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) {
		kid, ok := token.Header["kid"].(string)
		key := keys[kid]
		if !ok || kid == "" || key == nil || key.N == nil || key.N.BitLen() < 2048 {
			return nil, invalid
		}
		return key, nil
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer("https://auth.openai.com"), jwt.WithAudience(clientID), jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithTimeFunc(func() time.Time { return now }))
	if err != nil || (nonce != "" && subtle.ConstantTimeCompare([]byte(nonce), []byte(claims.Nonce)) != 1) || strings.TrimSpace(claims.Subject) == "" || strings.TrimSpace(claims.Email) == "" {
		return Identity{}, invalid
	}
	if (len(claims.Audience) > 1 || claims.AuthorizedParty != "") && claims.AuthorizedParty != clientID {
		return Identity{}, invalid
	}
	if (expected.Subject != "" && claims.Subject != expected.Subject) || (expected.Email != "" && !strings.EqualFold(strings.TrimSpace(claims.Email), strings.TrimSpace(expected.Email))) {
		return Identity{}, invalid
	}
	return Identity{Subject: claims.Subject, Email: strings.TrimSpace(claims.Email)}, nil
}
