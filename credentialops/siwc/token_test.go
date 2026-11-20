package siwc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type tokenTransport func(*http.Request) (*http.Response, error)

func (transport tokenTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestExchangeRefreshAndProviderBoundary(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	claims := identityClaims{RegisteredClaims: jwt.RegisteredClaims{Issuer: "https://auth.openai.com", Subject: "subject", Audience: jwt.ClaimStrings{"issued"}, ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour))}, Nonce: "nonce", Email: "test@example.com"}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "key"
	idToken, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	var forms []url.Values
	client := &http.Client{Transport: tokenTransport(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
			t.Fatal("upstream credentials leaked into authorization request")
		}
		var data any
		switch request.URL.String() {
		case KeysURL:
			data = map[string]any{"keys": []any{map[string]any{"kid": "key", "kty": "RSA", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}}
		case TokenURL:
			body, _ := io.ReadAll(request.Body)
			form, err := url.ParseQuery(string(body))
			if err != nil {
				t.Fatal(err)
			}
			forms = append(forms, form)
			if form.Get("grant_type") == "authorization_code" {
				data = tokenResponse{AccessToken: "access", RefreshToken: "refresh", IDToken: idToken, Scope: "chatgpt.tokens.use.direct", ExpiresIn: 3600, TokenType: "Bearer"}
			} else {
				data = tokenResponse{AccessToken: "next", RefreshToken: "rotated", ExpiresIn: 3600, TokenType: "Bearer"}
			}
		default:
			t.Fatalf("unexpected destination %s", request.URL)
		}
		raw, _ := json.Marshal(data)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw))), Header: make(http.Header)}, nil
	})}
	grant, err := Exchange(context.Background(), client, ExchangeInput{Code: "code", Verifier: "verifier", Nonce: "nonce", ClientID: "issued"}, "host", Identity{Email: "test@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	next, err := Refresh(context.Background(), client, grant)
	if err != nil {
		t.Fatal(err)
	}
	if next.RefreshToken != "rotated" || next.AccessToken != "next" || next.IDToken != grant.IDToken || next.Subject != grant.Subject || next.Scope != grant.Scope {
		t.Fatal("refresh lost authorization binding")
	}
	if len(forms) != 2 || forms[0].Get("code_verifier") != "verifier" || forms[1].Get("refresh_token") != "refresh" || forms[1].Get("client_id") != "issued" {
		t.Fatal("unexpected OAuth exchange")
	}
	if _, err := DecodeGrant(next.Credentials()); err != nil {
		t.Fatal(err)
	}
}

func TestAuthorizationRedirectDoesNotForwardSecrets(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: tokenTransport(func(request *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: 307, Header: http.Header{"Location": []string{"https://example.com/token"}}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
	})}
	err := readJSON(context.Background(), client, http.MethodPost, TokenURL, url.Values{"refresh_token": {"secret"}}, new(tokenResponse))
	if err == nil || requests != 1 || strings.Contains(err.Error(), "secret") {
		t.Fatal("redirect not rejected safely")
	}
}
