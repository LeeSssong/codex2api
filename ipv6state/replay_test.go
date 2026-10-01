package ipv6state

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/statepool"
)

func savedReplayState(t *testing.T, m *Manager, account *auth.Account) Entry {
	t.Helper()
	identity, err := statepool.Snapshot(account, "")
	if err != nil {
		t.Fatal(err)
	}
	pack := Portable{Format: "codex2api-ipv6-292-v1", MemberHash: identity.MemberHash, WorkspaceHash: identity.WorkspaceHash, Model: m.config.Models[0], Value: tokenAt(m.now().Unix() - 600)}
	if err := m.Import(context.Background(), pack); err != nil {
		t.Fatal(err)
	}
	return m.entries[key(account.ID(), pack.Model)]
}

func rotateReplayCredential(account *auth.Account) {
	account.Mu().Lock()
	defer account.Mu().Unlock()
	account.AccessToken += "-new"
	account.CredentialGeneration++
}

func TestReloginReplaysBeforeCaptureAndKeepsOriginalLifetime(t *testing.T) {
	m, account, _ := fixture(t, "member", false)
	original := savedReplayState(t, m, account)
	rotateReplayCredential(account)
	m.config.Concurrency = 20
	started, release := make(chan struct{}, 1), make(chan struct{})
	m.execute = func(ctx context.Context, a *auth.Account, model string, route Route) (*http.Response, error) {
		if a != account || route.ReuseState != original.Value || route.SourceIP != "" || route.ProxyURL != "" || model != original.Model {
			t.Error("re-login collected a new state instead of replaying the original")
		}
		started <- struct{}{}
		<-release
		return &http.Response{StatusCode: 200, Header: http.Header{Header: {tokenAt(m.now().Unix())}}, Body: &unreadBody{t: t, ctx: ctx}}, nil
	}
	m.step()
	receive(t, started)
	if value, _, _ := m.Resolve(account, original.Model); value != "" {
		t.Error("state was authorized before replay completed")
	}
	if len(m.active) != 1 {
		t.Error("replay launched duplicate candidates")
	}
	close(release)
	m.workers.Wait()
	entry := m.entries[key(account.ID(), original.Model)]
	if entry.Value != original.Value || entry.ExpiresAt != original.ExpiresAt || entry.CapturedAt != original.CapturedAt || entry.IssuedAt != original.IssuedAt || entry.Identity == original.Identity {
		t.Fatal("replay changed the original value or lifetime, or failed to rebind credentials")
	}
	if value, _, err := m.Resolve(account, original.Model); err != nil || value != original.Value {
		t.Fatal("successful replay did not restore use")
	}
	m.execute = func(context.Context, *auth.Account, string, Route) (*http.Response, error) {
		t.Error("valid rebound state was unnecessarily recaptured")
		return nil, errors.New("unexpected capture")
	}
	stepAndWait(m)
	restarted := New(m.db, m.store, m.execute, nil, nil)
	restarted.now = m.now
	config := m.config
	config.Enabled = false
	if err := m.Configure(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer restarted.Stop()
	if got := restarted.entries[key(account.ID(), original.Model)]; got.Identity != entry.Identity || got.Value != original.Value || got.ExpiresAt != original.ExpiresAt {
		t.Fatal("rebound state did not survive restart")
	}
}

func TestReloginFailuresRetainStateUntilRetry(t *testing.T) {
	for _, status := range []int{0, 400, 401, 403, 429, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			m, account, now := fixture(t, "member", false)
			original := savedReplayState(t, m, account)
			rotateReplayCredential(account)
			m.execute = func(ctx context.Context, _ *auth.Account, _ string, route Route) (*http.Response, error) {
				if route.ReuseState != original.Value {
					t.Error("old state was not replayed")
				}
				if status == 0 {
					return nil, errors.New("offline")
				}
				return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"generic_error"}}`))}, nil
			}
			stepAndWait(m)
			entry := m.entries[key(account.ID(), original.Model)]
			if entry.Value != original.Value || entry.Identity != original.Identity || entry.ExpiresAt != original.ExpiresAt {
				t.Fatal("inconclusive failure discarded or authorized the saved state")
			}
			if value, _, _ := m.Resolve(account, original.Model); value != "" {
				t.Fatal("unverified credential could reuse the state")
			}
			*now = now.Add(2 * time.Minute)
			m.execute = func(ctx context.Context, _ *auth.Account, _ string, route Route) (*http.Response, error) {
				if route.ReuseState != original.Value {
					t.Error("retry recaptured instead of replaying")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: &unreadBody{t: t, ctx: ctx}}, nil
			}
			stepAndWait(m)
			if value, _, _ := m.Resolve(account, original.Model); value != original.Value {
				t.Fatal("retry did not restore the original state")
			}
		})
	}
}

func TestReloginExplicitStateRejectionAllowsRecapture(t *testing.T) {
	m, account, now := fixture(t, "member", false)
	original := savedReplayState(t, m, account)
	rotateReplayCredential(account)
	m.execute = func(context.Context, *auth.Account, string, Route) (*http.Response, error) {
		return &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_turn_state"}}`))}, nil
	}
	stepAndWait(m)
	if m.entries[key(account.ID(), original.Model)].Value != "" {
		t.Fatal("explicitly rejected state was retained")
	}
	*now = now.Add(2 * time.Minute)
	m.execute = func(ctx context.Context, _ *auth.Account, _ string, route Route) (*http.Response, error) {
		if route.ReuseState != "" || route.SourceIP == "" {
			t.Error("rejected state was replayed instead of collecting a replacement")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{Header: {tokenAt(now.Unix())}}, Body: &unreadBody{t: t, ctx: ctx}}, nil
	}
	stepAndWait(m)
	if value, _, _ := m.Resolve(account, original.Model); value == "" || value == original.Value {
		t.Fatal("replacement was not activated")
	}
}

func TestReloginDuringReplayDiscardsOldCredentialResult(t *testing.T) {
	m, account, _ := fixture(t, "member", false)
	original := savedReplayState(t, m, account)
	rotateReplayCredential(account)
	started, release := make(chan struct{}), make(chan struct{})
	m.execute = func(ctx context.Context, _ *auth.Account, _ string, _ Route) (*http.Response, error) {
		close(started)
		<-release
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: &unreadBody{t: t, ctx: ctx}}, nil
	}
	m.step()
	receive(t, started)
	rotateReplayCredential(account)
	close(release)
	m.workers.Wait()
	if value, _, _ := m.Resolve(account, original.Model); value != "" {
		t.Fatal("stale result authorized newly updated credentials")
	}
	if entry := m.entries[key(account.ID(), original.Model)]; entry.Value != original.Value || entry.ExpiresAt != original.ExpiresAt {
		t.Fatal("stale result discarded the original state")
	}
}

func TestNewLoginDoesNotInheritPreviousReplayCooldown(t *testing.T) {
	m, account, _ := fixture(t, "member", false)
	original := savedReplayState(t, m, account)
	rotateReplayCredential(account)
	m.execute = func(context.Context, *auth.Account, string, Route) (*http.Response, error) {
		return &http.Response{StatusCode: 401, Header: http.Header{"Retry-After": {"86400"}}}, nil
	}
	stepAndWait(m)
	if entry := m.entries[key(account.ID(), original.Model)]; entry.RetryAt <= m.now().Unix() {
		t.Fatal("rejected login has no retry gate")
	}
	rotateReplayCredential(account)
	m.execute = func(ctx context.Context, _ *auth.Account, _ string, route Route) (*http.Response, error) {
		if route.ReuseState != original.Value {
			t.Error("new login discarded the retained state")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: &unreadBody{t: t, ctx: ctx}}, nil
	}
	stepAndWait(m)
	if value, _, _ := m.Resolve(account, original.Model); value != original.Value {
		t.Fatal("old login's retry gate blocked the new credential")
	}
}

func TestReplayDoesNotCrossMemberWorkspaceOrExpiry(t *testing.T) {
	for _, change := range []string{"member", "workspace", "expiry"} {
		t.Run(change, func(t *testing.T) {
			m, account, now := fixture(t, "member", false)
			original := savedReplayState(t, m, account)
			rotateReplayCredential(account)
			switch change {
			case "member":
				account.AccessToken = "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"other-member","exp":9999999999}`)) + ".signature"
			case "workspace":
				account.AccountID = "another-workspace"
			case "expiry":
				*now = time.Unix(original.ExpiresAt, 0)
			}
			calls := 0
			m.execute = func(ctx context.Context, _ *auth.Account, _ string, route Route) (*http.Response, error) {
				calls++
				if route.ReuseState != "" {
					t.Error("replayed a state outside its original account or lifetime")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{Header: {tokenAt(now.Unix())}}, Body: &unreadBody{t: t, ctx: ctx}}, nil
			}
			stepAndWait(m)
			if calls != 1 {
				t.Fatal("ineligible state did not trigger replacement")
			}
		})
	}
}
