package ipv6state

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/statepool"
)

func importForTest(t *testing.T, m *Manager, account *auth.Account, issued int64) string {
	t.Helper()
	identity, err := statepool.Snapshot(account, "")
	if err != nil {
		t.Fatal(err)
	}
	value := tokenAt(issued)
	if err := m.Import(context.Background(), Portable{Format: "codex2api-ipv6-292-v1", MemberHash: identity.MemberHash, WorkspaceHash: identity.WorkspaceHash, Model: m.config.Models[0], Value: value}); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestRequiredStateRejectsMissingExpiredWrongModelAndDisabledReuse(t *testing.T) {
	m, account, now := fixture(t, "member", false)
	if err := m.SetRequireValidState(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	model := m.config.Models[0]
	if _, active, err := m.Resolve(account, model); !active || !errors.Is(err, ErrStateRequired) {
		t.Fatal("missing state did not fail closed")
	}
	value := importForTest(t, m, account, now.Unix()-30)
	if got, active, err := m.Resolve(account, model); !active || err != nil || got != value {
		t.Fatal("valid state rejected")
	}
	strict, eligible := m.EligibleAccounts(model)
	if !strict || !eligible[account.ID()] {
		t.Fatal("valid account not eligible")
	}
	if _, _, err := m.Resolve(account, statepool.Models[1]); !errors.Is(err, ErrStateRequired) {
		t.Fatal("wrong model accepted")
	}
	config := m.Status().Config
	config.Enabled = false
	if err := m.Configure(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Resolve(account, model); !errors.Is(err, ErrStateRequired) {
		t.Fatal("disabled reuse bypassed strict policy")
	}
	if !m.AccountModels(account)[0].Valid {
		t.Fatal("disabled automation hid a still valid saved state")
	}
	*now = now.Add(time.Hour)
	if m.AccountModels(account)[0].Valid {
		t.Fatal("expired state remained green")
	}
	config.Enabled = true
	if err := m.Configure(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Resolve(account, model); !errors.Is(err, ErrStateRequired) {
		t.Fatal("expired state accepted")
	}
	if err := m.SetRequireValidState(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if _, active, err := m.Resolve(account, model); err != nil || !active {
		t.Fatal("non-strict fallback was lost")
	}
}

func TestRenewalStartsAtTenMinutesAndKeepsOldUntilReplacement(t *testing.T) {
	m, account, now := fixture(t, "member", false)
	old := importForTest(t, m, account, now.Unix()-2999)
	started, release := make(chan struct{}), make(chan struct{})
	m.execute = func(ctx context.Context, _ *auth.Account, _ string, _ Route) (*http.Response, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return &http.Response{StatusCode: 200, Header: http.Header{Header: {tokenAt(now.Unix())}}, Body: &unreadBody{t: t, ctx: ctx}}, nil
	}
	stepAndWait(m)
	select {
	case <-started:
		t.Fatal("renewal started before ten minute boundary")
	default:
	}
	*now = now.Add(time.Second)
	m.step()
	receive(t, started)
	if got, _, err := m.Resolve(account, m.config.Models[0]); err != nil || got != old {
		t.Fatal("old state lost during renewal")
	}
	if !m.Status().Entries[0].Refreshing || !m.AccountModels(account)[0].Valid {
		t.Fatal("renewing state not shown as valid")
	}
	close(release)
	m.workers.Wait()
	got, _, err := m.Resolve(account, m.config.Models[0])
	if err != nil || got == old || got != tokenAt(now.Unix()) {
		t.Fatal("fresh state was not installed")
	}
	if m.Status().Entries[0].ExpiresAt != now.Unix()+3600 {
		t.Fatal("replacement expiry was changed")
	}
}

func TestRenewalFailureAndRepeatedOldTokenPreserveOriginalExpiry(t *testing.T) {
	for _, status := range []int{200, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			m, account, now := fixture(t, "member", false)
			old := importForTest(t, m, account, now.Unix()-3100)
			m.execute = func(ctx context.Context, _ *auth.Account, _ string, _ Route) (*http.Response, error) {
				return &http.Response{StatusCode: status, Header: http.Header{Header: {old}, "Retry-After": {"60"}}, Body: &unreadBody{t: t, ctx: ctx}}, nil
			}
			stepAndWait(m)
			if got, _, err := m.Resolve(account, m.config.Models[0]); err != nil || got != old {
				t.Fatal("failed renewal removed usable state")
			}
			entry := m.Status().Entries[0]
			if entry.ExpiresAt != now.Unix()+500 || entry.RetryAt <= now.Unix() {
				t.Fatal("renewal extended expiry or omitted backoff")
			}
			if status == 200 && entry.Error != "state_not_newer" {
				t.Fatal("old token counted as renewed")
			}
			*now = now.Add(500 * time.Second)
			if got, _, _ := m.Resolve(account, m.config.Models[0]); got != "" {
				t.Fatal("expired old state reused")
			}
		})
	}
}

func TestStatusUsesCurrentCooldownInsteadOfStaleCaptureError(t *testing.T) {
	m, account, now := fixture(t, "member", false)
	identity, _ := statepool.Snapshot(account, "")
	m.entries[key(account.ID(), m.config.Models[0])] = Entry{AccountID: account.ID(), Model: m.config.Models[0], Identity: identity, Status: "retrying", Error: "upstream_429"}
	account.SetCooldownUntil(now.Add(time.Hour), "unauthorized")
	entry := m.Status().Entries[0]
	if entry.Error != "upstream_429" || entry.CooldownReason != "unauthorized" || entry.CapturePhase != "account_unavailable" || entry.CooldownUntil <= now.Unix() {
		t.Fatal("stale 429 masked current authorization failure")
	}
}
