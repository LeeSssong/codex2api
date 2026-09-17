package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/tidwall/gjson"
)

func TestVerifiedStateInjectionPreservesInputAndNativeState(t *testing.T) {
	account := &auth.Account{DBID: 42, AccessToken: "token"}
	SetStatePoolResolver(func(a *auth.Account, model, effort, proxy, native string) (string, error) {
		if a != account || model != "gpt-5.6-sol" || effort != "high" {
			t.Fatal("wrong binding")
		}
		if native != "" {
			return "", nil
		}
		return "verified-state", nil
	})
	t.Cleanup(func() { SetStatePoolResolver(nil) })
	body := []byte(`{"model":"gpt-5.6-sol","reasoning":{"effort":"high"},"client_metadata":{"other":"retained"}}`)
	headers := http.Header{"Existing": []string{"retained"}}
	updated, outgoing, err := applyVerifiedState(context.Background(), account, body, headers, "")
	if err != nil || outgoing.Get(codexTurnStateHeader) != "verified-state" || gjson.GetBytes(updated, "client_metadata.x-codex-turn-state").String() != "verified-state" {
		t.Fatalf("injection failed: %v", err)
	}
	if headers.Get(codexTurnStateHeader) != "" || gjson.GetBytes(body, "client_metadata.x-codex-turn-state").Exists() || gjson.GetBytes(updated, "client_metadata.other").String() != "retained" {
		t.Fatal("mutated caller state")
	}
	headers.Set(codexTurnStateHeader, "native")
	_, outgoing, err = applyVerifiedState(context.Background(), account, body, headers, "")
	if err != nil || outgoing.Get(codexTurnStateHeader) != "native" {
		t.Fatal("native header replaced")
	}
	updated, outgoing, err = applyVerifiedState(WithoutStatePool(context.Background()), account, body, nil, "")
	if err != nil || outgoing.Get(codexTurnStateHeader) != "" || gjson.GetBytes(updated, "client_metadata.x-codex-turn-state").Exists() {
		t.Fatal("probe bypass did not work")
	}
}

func TestVerifiedStateStrictErrorIsNotRetried(t *testing.T) {
	SetStatePoolResolver(func(*auth.Account, string, string, string, string) (string, error) {
		return "", errors.New("verified_state_unavailable")
	})
	t.Cleanup(func() { SetStatePoolResolver(nil) })
	_, _, err := applyVerifiedState(context.Background(), &auth.Account{DBID: 1}, []byte(`{"model":"gpt-5.6-sol"}`), nil, "")
	var typed *Error
	if !errors.As(err, &typed) || typed.HTTPStatus != 503 || typed.Retryable {
		t.Fatalf("wrong strict failure: %v", err)
	}
}

func TestTransient429RespectsLongServerMinimum(t *testing.T) {
	now := time.Now()
	decision := transientAccountRateLimitDecision(nil, &http.Response{Header: http.Header{"Retry-After": []string{"900"}}}, now)
	if decision.Cooldown < 15*time.Minute {
		t.Fatalf("server minimum truncated: %v", decision.Cooldown)
	}
}

func TestVerifiedStateReachesWebsocketExecutor(t *testing.T) {
	previousWS := WebsocketExecuteFunc
	t.Cleanup(func() { WebsocketExecuteFunc = previousWS; SetStatePoolResolver(nil) })
	SetStatePoolResolver(func(*auth.Account, string, string, string, string) (string, error) {
		return "verified-state", nil
	})
	called := false
	WebsocketExecuteFunc = func(ctx context.Context, account *auth.Account, body []byte, sessionID, proxyURL, key string, cfg *DeviceProfileConfig, headers http.Header, poolKey string) (*http.Response, error) {
		called = true
		if headers.Get(codexTurnStateHeader) != "verified-state" || gjson.GetBytes(body, "client_metadata.x-codex-turn-state").String() != "verified-state" {
			t.Fatal("state missing at websocket transport boundary")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	body := []byte(`{"model":"gpt-5.6-sol","reasoning":{"effort":"high"},"input":[]}`)
	resp, err := ExecuteRequest(context.Background(), &auth.Account{DBID: 42, AccessToken: "token"}, body, "session", "", "", nil, nil, true)
	if err != nil || !called {
		t.Fatalf("websocket dispatch failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
}
