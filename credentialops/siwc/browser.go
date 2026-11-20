package siwc

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"
)

const redirectURI = "http://127.0.0.1:1455/auth/callback"

type BrowserSession struct {
	mu                                           sync.Mutex
	state, nonce, verifier, owner, routeRevision string
	expires                                      time.Time
	consumed                                     bool
	clientID                                     string
}

type ExchangeInput struct {
	Code, Verifier, Nonce, ClientID string
}

func randomValue() string {
	var value [32]byte
	rand.Read(value[:])
	return base64.RawURLEncoding.EncodeToString(value[:])
}

func BeginBrowser(clientID, hostID, owner, routeRevision string, now time.Time) (*BrowserSession, string, error) {
	for _, required := range []string{clientID, hostID, owner, routeRevision} {
		if strings.TrimSpace(required) == "" {
			return nil, "", errors.New("missing SIWC registration, owner or route")
		}
	}
	session := &BrowserSession{state: randomValue(), nonce: randomValue(), verifier: randomValue(), owner: owner, routeRevision: routeRevision, expires: now.Add(30 * time.Minute), clientID: clientID}
	digest := sha256.Sum256([]byte(session.verifier))
	params := url.Values{"client_id": {clientID}, "response_type": {"code"}, "redirect_uri": {redirectURI}, "resource": {"https://api.openai.com/v1"}, "scope": {"openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"}, "state": {session.state}, "nonce": {session.nonce}, "ext_agent_host_id": {hostID}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(digest[:])}}
	if clientID == "dynamic_agent_client" {
		params.Set("agent_name_hint", "Codex2API")
	}
	return session, "https://auth.openai.com/api/accounts/authorize?" + params.Encode(), nil
}

func (session *BrowserSession) TakeCallback(raw, owner, routeRevision string, now time.Time) (ExchangeInput, error) {
	session.mu.Lock()
	defer session.mu.Unlock()
	invalid := errors.New("invalid or expired SIWC callback")
	if session.consumed || !now.Before(session.expires) || session.owner != owner || session.routeRevision != routeRevision || len(raw) > 16384 {
		return ExchangeInput{}, invalid
	}
	callback, err := url.Parse(raw)
	if err != nil || callback.Scheme != "http" || callback.Host != "127.0.0.1:1455" || callback.EscapedPath() != "/auth/callback" || callback.User != nil || strings.Contains(raw, "#") {
		return ExchangeInput{}, invalid
	}
	params, err := url.ParseQuery(callback.RawQuery)
	if err != nil || len(params["state"]) != 1 || subtle.ConstantTimeCompare([]byte(params.Get("state")), []byte(session.state)) != 1 {
		return ExchangeInput{}, invalid
	}
	for _, key := range []string{"code", "client_id", "error"} {
		if len(params[key]) > 1 {
			return ExchangeInput{}, invalid
		}
	}
	if params.Get("error") != "" {
		session.consumed = true
		return ExchangeInput{}, errors.New("SIWC authorization denied")
	}
	if len(params["code"]) != 1 || strings.TrimSpace(params.Get("code")) == "" {
		return ExchangeInput{}, invalid
	}
	issued := session.clientID
	if params.Has("client_id") {
		issued = params.Get("client_id")
		if len(params["client_id"]) != 1 || strings.TrimSpace(issued) == "" || (session.clientID != "dynamic_agent_client" && issued != session.clientID) {
			return ExchangeInput{}, invalid
		}
	}
	if issued == "dynamic_agent_client" {
		return ExchangeInput{}, invalid
	}
	session.consumed = true
	return ExchangeInput{Code: params.Get("code"), Verifier: session.verifier, Nonce: session.nonce, ClientID: issued}, nil
}
