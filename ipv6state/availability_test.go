package ipv6state

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex2api/auth"
)

func TestUnavailableAccountsPauseCaptureWithoutLosingSavedState(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		block  func(*Manager, *auth.Account, time.Time)
	}{
		{"cooldown", "rate_limited", func(_ *Manager, a *auth.Account, now time.Time) {
			a.SetCooldownUntil(now.Add(time.Minute), "rate_limited")
		}},
		{"model_cooldown", "rate_limited", func(m *Manager, a *auth.Account, now time.Time) {
			a.SetModelCooldownUntil(m.config.Models[0], "rate_limited", now.Add(time.Minute))
		}},
		{"usage_5h", "rate_limited", func(_ *Manager, a *auth.Account, now time.Time) {
			a.Mu().Lock()
			defer a.Mu().Unlock()
			a.UsagePercent5h, a.UsagePercent5hValid, a.Reset5hAt = 100, true, now.Add(time.Hour)
		}},
		{"usage_7d", "rate_limited", func(_ *Manager, a *auth.Account, now time.Time) {
			a.Mu().Lock()
			defer a.Mu().Unlock()
			a.UsagePercent7d, a.UsagePercent7dValid, a.Reset7dAt = 100, true, now.Add(time.Hour)
		}},
		{"free_exhausted", "usage_exhausted", func(_ *Manager, a *auth.Account, now time.Time) {
			a.Mu().Lock()
			defer a.Mu().Unlock()
			a.PlanType = "free"
			a.UsagePercent7d, a.UsagePercent7dValid, a.Reset7dAt = 100, true, now.Add(time.Hour)
		}},
		{"quota_pause", "quota_paused", func(m *Manager, a *auth.Account, now time.Time) {
			a.Mu().Lock()
			a.UsagePercent5h, a.UsagePercent5hValid, a.Reset5hAt = 80, true, now.Add(time.Hour)
			a.Mu().Unlock()
			m.store.SetGlobalAutoPauseThresholds(0.8, 0)
		}},
		{"disabled", "disabled", func(_ *Manager, a *auth.Account, _ time.Time) {
			atomic.StoreInt32(&a.DispatchPaused, 1)
		}},
		{"unauthorized", "unauthorized", func(_ *Manager, a *auth.Account, _ time.Time) {
			atomic.StoreInt32(&a.Disabled, 1)
		}},
		{"account_error", "error", func(_ *Manager, a *auth.Account, _ time.Time) {
			a.Mu().Lock()
			defer a.Mu().Unlock()
			a.Status = auth.StatusError
		}},
	}
	for _, tc := range cases {
		for _, phase := range []string{"before_selection", "before_upstream", "in_flight"} {
			t.Run(tc.name+"/"+phase, func(t *testing.T) {
				m, account, now := fixture(t, "capture-availability", false)
				old := importForTest(t, m, account, now.Unix()-3000)
				expires := m.Status().Entries[0].ExpiresAt
				var calls atomic.Int32
				started := make(chan struct{}, 1)
				m.execute = func(ctx context.Context, _ *auth.Account, _ string, _ Route) (*http.Response, error) {
					calls.Add(1)
					if phase == "in_flight" {
						started <- struct{}{}
						<-ctx.Done()
					}
					return &http.Response{StatusCode: 200, Header: http.Header{Header: {tokenAt(now.Unix())}}}, nil
				}
				switch phase {
				case "before_selection":
					tc.block(m, account, *now)
				case "before_upstream":
					m.config.CaptureMode, m.config.ProxyIDs = "proxy", []int64{1}
					m.route = func(context.Context, Config, int64) (Route, error) {
						tc.block(m, account, *now)
						return Route{ProxyID: 1}, nil
					}
				}
				m.step()
				if phase == "in_flight" {
					receive(t, started)
					tc.block(m, account, *now)
					m.step()
				}
				m.workers.Wait()
				stepAndWait(m)
				wantCalls := int32(0)
				if phase == "in_flight" {
					wantCalls = 1
				}
				if calls.Load() != wantCalls || account.GetActiveRequests() != 0 {
					t.Fatalf("restricted capture sent requests or leaked slots: calls=%d slots=%d", calls.Load(), account.GetActiveRequests())
				}
				status := m.Status()
				entry := status.Entries[0]
				if entry.CapturePhase != "account_unavailable" || entry.CooldownReason != tc.reason || !entry.Valid || entry.ExpiresAt != expires {
					t.Fatalf("wrong saved validity or current restriction: %+v", entry)
				}
				if value, _, err := m.Resolve(account, m.config.Models[0]); err != nil || value != old {
					t.Fatal("restriction deleted or replaced the saved State")
				}
				if status.Summary.ReuseAccounts != 1 || status.Summary.Models[0].ReuseAccounts != 1 || status.Summary.AvailableAccounts != 0 {
					t.Fatal("saved coverage was conflated with account availability")
				}
			})
		}
	}
}

func TestCaptureResumesAtCooldownBoundary(t *testing.T) {
	m, account, now := fixture(t, "capture-resume", false)
	old := importForTest(t, m, account, now.Unix()-3000)
	until := now.Add(time.Minute)
	account.SetCooldownUntil(until, "rate_limited")
	var calls int
	m.execute = func(context.Context, *auth.Account, string, Route) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{Header: {tokenAt(now.Unix())}}}, nil
	}
	*now = until.Add(-time.Second)
	stepAndWait(m)
	if calls != 0 {
		t.Fatal("capture resumed before cooldown expiry")
	}
	*now = until
	stepAndWait(m)
	if value, _, err := m.Resolve(account, m.config.Models[0]); err != nil || calls != 1 || value == old || value == "" {
		t.Fatal("capture did not resume at the cooldown boundary")
	}
}

func TestRestrictedModelDoesNotEscalateAccountBudgets(t *testing.T) {
	m, account, now := fixture(t, "capture-budget", false)
	m.config.Models = []string{"gpt-5.6-sol", "gpt-5.6-luna"}
	m.config.RefreshBeforeMinutes, m.config.StagedConcurrency = 30, true
	m.config.Concurrency, m.config.EarlyConcurrency, m.config.ExpiredConcurrency = 20, 1, 10
	m.config.UrgentBusinessConcurrency = 1
	importForTest(t, m, account, now.Unix()-2400)
	account.SetModelCooldownUntil(m.config.Models[1], "rate_limited", now.Add(time.Hour))
	m.updateBusinessBudgets()
	if limit, _ := m.captureBudget(account); limit != 1 {
		t.Fatalf("restricted missing model increased early capture budget to %d", limit)
	}
	for range 2 {
		acquired := m.store.TakePreferredAccountWithFilter(account.ID(), 0, nil, nil)
		if acquired == nil {
			t.Fatal("restricted model reduced other models' business concurrency")
		}
		t.Cleanup(func() { m.store.Release(acquired) })
	}
}
