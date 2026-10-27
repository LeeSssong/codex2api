package auth

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestIntelligencePauseFencesBusinessButAllowsBoundedProbe(t *testing.T) {
	s := NewStore(nil, nil, nil)
	s.SetMaxConcurrency(1)
	a := &Account{DBID: 1, AccessToken: "test", Status: StatusReady, HealthTier: HealthTierHealthy}
	s.AddAccount(a)
	atomic.StoreInt32(&a.IntelligencePaused, 1)
	if !accountDispatchBlocked(a) || a.ModelCatalogEligible() {
		t.Fatal("detection pause must block business/catalog")
	}
	_, _, _, _, available := a.fastSchedulerSnapshot(4, time.Now())
	if available {
		t.Fatal("fast scheduler ignored detection pause")
	}
	if !s.TryAcquireIntelligenceCheck(a) {
		t.Fatal("detection must continue during its own pause")
	}
	if s.TryAcquireIntelligenceCheck(a) {
		t.Fatal("hard default capacity exceeded")
	}
	s.Release(a)
	atomic.StoreInt32(&a.DispatchPaused, 1)
	atomic.StoreInt32(&a.IntelligencePaused, 0)
	if !accountDispatchBlocked(a) {
		t.Fatal("clearing quality pause must not enable manually disabled account")
	}
}

func TestAutomaticIntelligenceCheckAvailabilityUsesLiveQuotaWithoutChangingSwitches(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name  string
		setup func(*Account)
		want  bool
	}{
		{"ready", func(*Account) {}, true},
		{"cooling", func(a *Account) { a.Status = StatusCooldown; a.CooldownUtil = now.Add(time.Hour) }, false},
		{"expired cooldown", func(a *Account) { a.Status = StatusCooldown; a.CooldownUtil = now }, true},
		{"full five hour", func(a *Account) {
			a.PlanType = "plus"
			a.UsagePercent5hValid = true
			a.UsagePercent5h = 100
			a.Reset5hAt = now.Add(time.Hour)
		}, false},
		{"full seven day", func(a *Account) {
			a.UsagePercent7dValid = true
			a.UsagePercent7d = 100
			a.Reset7dAt = now.Add(time.Hour)
		}, false},
		{"expired seven day", func(a *Account) { a.UsagePercent7dValid = true; a.UsagePercent7d = 100; a.Reset7dAt = now }, true},
		{"continuation override is not new probe permission", func(a *Account) {
			a.ignoreUsageLimitStatus = true
			a.UsagePercent7dValid = true
			a.UsagePercent7d = 100
			a.Reset7dAt = now.Add(time.Hour)
		}, false},
		{"quality recovery remains probeable", func(a *Account) { a.IntelligencePaused = 1 }, true},
		{"disabled", func(a *Account) { a.Disabled = 1 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &Account{Status: StatusReady}
			tc.setup(a)
			status, cooldown, reason, disabled, paused := a.Status, a.CooldownUtil, a.CooldownReason, a.Disabled, a.IntelligencePaused
			if got := a.AutomaticIntelligenceCheckAvailable(now); got != tc.want {
				t.Fatalf("available=%v want=%v", got, tc.want)
			}
			if a.Status != status || a.CooldownUtil != cooldown || a.CooldownReason != reason || a.Disabled != disabled || a.IntelligencePaused != paused {
				t.Fatal("availability check changed account state")
			}
		})
	}
	if (*Account)(nil).AutomaticIntelligenceCheckAvailable(now) {
		t.Fatal("nil account was available")
	}
}
