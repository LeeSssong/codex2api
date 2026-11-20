package siwc

import (
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBrowserCallbackBindingAndSingleUse(t *testing.T) {
	now := time.Now()
	session, location, err := BeginBrowser("dynamic_agent_client", "host", "owner", "route-v1", now)
	if err != nil {
		t.Fatal(err)
	}
	authorize, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	params := authorize.Query()
	callback := redirectURI + "?code=example&state=" + params.Get("state")
	if _, err := session.TakeCallback(callback, "owner", "route-v1", now); err == nil {
		t.Fatal("accepted new registration without issued client ID")
	}
	callback += "&client_id=oaiapp_example"
	for _, bad := range []string{strings.Replace(callback, "127.0.0.1", "example.org", 1), callback + "&code=another", callback + "#fragment", callback + "&state=again"} {
		if _, err := session.TakeCallback(bad, "owner", "route-v1", now); err == nil {
			t.Fatal("accepted malformed callback")
		}
	}
	for _, input := range [][2]string{{"other", "route-v1"}, {"owner", "route-v2"}} {
		if _, err := session.TakeCallback(callback, input[0], input[1], now); err == nil {
			t.Fatal("accepted wrong binding")
		}
	}
	if _, err := session.TakeCallback(callback, "owner", "route-v1", now.Add(30*time.Minute)); err == nil {
		t.Fatal("accepted expired callback")
	}
	var accepted atomic.Int32
	var group sync.WaitGroup
	for range 12 {
		group.Go(func() {
			input, err := session.TakeCallback(callback, "owner", "route-v1", now)
			if err != nil {
				return
			}
			accepted.Add(1)
			digest := sha256.Sum256([]byte(input.Verifier))
			if params.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(digest[:]) || input.Nonce != params.Get("nonce") || input.Code != "example" {
				t.Error("callback not bound to original PKCE and nonce")
			}
		})
	}
	group.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("accepted %d callbacks", accepted.Load())
	}
}

func TestReturningClientConsentCallbacks(t *testing.T) {
	for _, scenario := range []struct {
		name, query string
		consumed    bool
	}{
		{"duplicate denial", "&error=access_denied&error=access_denied", false},
		{"duplicate client on denial", "&error=access_denied&client_id=first&client_id=second", false},
		{"duplicate code on denial", "&error=access_denied&code=first&code=second", false},
		{"changed returning client", "&code=test&client_id=other", false},
		{"declined consent", "&error=access_denied", true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			now := time.Now()
			session, location, err := BeginBrowser("issued-client", "host", "owner", "route", now)
			if err != nil {
				t.Fatal(err)
			}
			authURL, err := url.Parse(location)
			if err != nil {
				t.Fatal(err)
			}
			callback := redirectURI + "?state=" + authURL.Query().Get("state")
			if _, err := session.TakeCallback(callback+scenario.query, "owner", "route", now); err == nil {
				t.Fatal("invalid or denied callback accepted")
			}
			input, err := session.TakeCallback(callback+"&code=valid", "owner", "route", now)
			if scenario.consumed {
				if err == nil {
					t.Fatal("declined session reused")
				}
				return
			}
			if err != nil || input.ClientID != "issued-client" {
				t.Fatal("malformed callback invalidated original registration")
			}
		})
	}
}
