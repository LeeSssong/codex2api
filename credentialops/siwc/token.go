package siwc

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const TokenURL = "https://auth.openai.com/api/accounts/oauth/token"
const KeysURL = "https://auth.openai.com/.well-known/jwks.json"

type Grant struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	IDToken      string    `json:"id_token"`
	Scope        string    `json:"scope"`
	ClientID     string    `json:"client_id"`
	HostID       string    `json:"host_id"`
	Subject      string    `json:"subject"`
	Email        string    `json:"email"`
	Workspace    string    `json:"workspace"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func (grant Grant) Credentials() map[string]any {
	raw, _ := json.Marshal(grant)
	var result map[string]any
	_ = json.Unmarshal(raw, &result)
	return result
}

func DecodeGrant(value map[string]any) (Grant, error) {
	var grant Grant
	raw, err := json.Marshal(value)
	if err == nil {
		err = json.Unmarshal(raw, &grant)
	}
	if err != nil || grant.AccessToken == "" || grant.RefreshToken == "" || grant.Subject == "" || grant.Email == "" || grant.ClientID == "" || grant.ClientID == "dynamic_agent_client" || !hasDirectPermission(grant.Scope) || grant.ExpiresAt.IsZero() {
		return Grant{}, errors.New("SIWC credentials are incomplete")
	}
	return grant, nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	Scope        string `json:"scope"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

func readJSON(ctx context.Context, client *http.Client, method, endpoint string, form url.Values, result any) error {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return errors.New("SIWC request creation failed")
	}
	request.Header.Set("Accept", "application/json")
	if form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	local := *client
	local.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := local.Do(request)
	if err != nil {
		return errors.New("SIWC authorization network failure")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("SIWC authorization rejected; renew authorization if revoked")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024+1))
	if err != nil || len(body) > 1024*1024 || json.Unmarshal(body, result) != nil {
		return errors.New("invalid SIWC authorization response")
	}
	return nil
}

func signingKeys(ctx context.Context, client *http.Client) (map[string]*rsa.PublicKey, error) {
	var document struct {
		Keys []struct{ Kid, Kty, Alg, Use, N, E string }
	}
	if err := readJSON(ctx, client, http.MethodGet, KeysURL, nil, &document); err != nil {
		return nil, err
	}
	keys := make(map[string]*rsa.PublicKey)
	for _, item := range document.Keys {
		if item.Kid == "" || item.Kty != "RSA" || (item.Alg != "" && item.Alg != "RS256") || (item.Use != "" && item.Use != "sig") {
			continue
		}
		modulus, err := base64.RawURLEncoding.DecodeString(item.N)
		exponent, expErr := base64.RawURLEncoding.DecodeString(item.E)
		if err != nil || expErr != nil || len(modulus) < 256 || len(modulus) > 1024 || len(exponent) == 0 || len(exponent) > 4 {
			continue
		}
		value := new(big.Int).SetBytes(exponent).Int64()
		if value < 3 || value > 2147483647 || value%2 == 0 {
			continue
		}
		if _, exists := keys[item.Kid]; exists {
			return nil, errors.New("ambiguous SIWC signing key")
		}
		keys[item.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(modulus), E: int(value)}
	}
	if len(keys) == 0 {
		return nil, errors.New("SIWC signing keys unavailable")
	}
	return keys, nil
}

func Exchange(ctx context.Context, client *http.Client, input ExchangeInput, hostID string, expected Identity) (Grant, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	keys, err := signingKeys(ctx, client)
	if err != nil {
		return Grant{}, err
	}
	var response tokenResponse
	err = readJSON(ctx, client, http.MethodPost, TokenURL, url.Values{"grant_type": {"authorization_code"}, "client_id": {input.ClientID}, "code": {input.Code}, "code_verifier": {input.Verifier}, "redirect_uri": {redirectURI}}, &response)
	if err != nil {
		return Grant{}, err
	}
	identity, err := input.VerifyIdentity(response.IDToken, response.Scope, keys, expected, time.Now())
	if err != nil {
		return Grant{}, err
	}
	if response.AccessToken == "" || response.RefreshToken == "" || response.ExpiresIn <= 0 || response.ExpiresIn > 31536000 || !strings.EqualFold(response.TokenType, "Bearer") {
		return Grant{}, errors.New("incomplete SIWC token response")
	}
	return Grant{AccessToken: response.AccessToken, RefreshToken: response.RefreshToken, IDToken: response.IDToken, Scope: response.Scope, ClientID: input.ClientID, HostID: hostID, Subject: identity.Subject, Email: identity.Email, ExpiresAt: time.Now().Add(time.Duration(response.ExpiresIn) * time.Second)}, nil
}

func Refresh(ctx context.Context, client *http.Client, previous Grant) (Grant, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	keys, err := signingKeys(ctx, client)
	if err != nil {
		return Grant{}, err
	}
	var response tokenResponse
	err = readJSON(ctx, client, http.MethodPost, TokenURL, url.Values{"grant_type": {"refresh_token"}, "client_id": {previous.ClientID}, "refresh_token": {previous.RefreshToken}}, &response)
	if err != nil {
		return Grant{}, err
	}
	scope := response.Scope
	if scope == "" {
		scope = previous.Scope
	}
	_, err = VerifyRefreshIdentity(previous.ClientID, response.IDToken, scope, keys, Identity{Subject: previous.Subject, Email: previous.Email}, time.Now())
	if err != nil {
		return Grant{}, err
	}
	if response.AccessToken == "" || response.ExpiresIn <= 0 || response.ExpiresIn > 31536000 || !strings.EqualFold(response.TokenType, "Bearer") {
		return Grant{}, errors.New("incomplete SIWC refresh response")
	}
	previous.AccessToken = response.AccessToken
	previous.ExpiresAt = time.Now().Add(time.Duration(response.ExpiresIn) * time.Second)
	previous.Scope = scope
	if response.RefreshToken != "" {
		previous.RefreshToken = response.RefreshToken
	}
	if response.IDToken != "" {
		previous.IDToken = response.IDToken
	}
	return previous, nil
}
