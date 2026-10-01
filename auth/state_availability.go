package auth

import (
	"sync/atomic"
	"time"
)

// StateAvailability reports durable dispatch gates at a single observation time.
// Busy request slots are intentionally excluded; the scheduler still owns them.
func (a *Account) StateAvailability(model string, now time.Time) (bool, string, time.Time) {
	if a == nil {
		return false, "account_missing", time.Time{}
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if atomic.LoadInt32(&a.Disabled) != 0 || a.healthTierLocked() == HealthTierBanned {
		if a.CooldownUtil.After(now) {
			return false, "unauthorized", a.CooldownUtil
		}
		return false, "unauthorized", time.Time{}
	}
	if atomic.LoadInt32(&a.DispatchPaused) != 0 {
		return false, "disabled", time.Time{}
	}
	if a.Status == StatusError {
		return false, "error", time.Time{}
	}
	if !a.hasDispatchCredentialLocked() {
		return false, "credential_unavailable", time.Time{}
	}
	if a.Status == StatusCooldown && now.Before(a.CooldownUtil) {
		reason := a.CooldownReason
		if reason == "" {
			reason = "cooldown"
		}
		return false, reason, a.CooldownUtil
	}
	if !a.isAvailableLocked(now) {
		var until time.Time
		if a.usageWindowBlocksFreshDispatchLocked(now) || a.quotaAutoPausedLocked(now) {
			if a.UsagePercent5hValid && a.Reset5hAt.After(now) && (a.UsagePercent5h >= 100 || quotaAutoPausedByWindow(a.UsagePercent5h, true, a.Reset5hAt, a.effectiveAutoPause5h, a.AutoPause5hDisabled, now)) {
				until = a.Reset5hAt
			}
			if a.UsagePercent7dValid && a.Reset7dAt.After(now) && (a.UsagePercent7d >= 100 || quotaAutoPausedByWindow(a.UsagePercent7d, true, a.Reset7dAt, a.effectiveAutoPause7d, a.AutoPause7dDisabled, now)) && a.Reset7dAt.After(until) {
				until = a.Reset7dAt
			}
		}
		return false, a.runtimeStatusLocked(now), until
	}
	if cooldown, ok := a.ModelCooldowns[normalizeModelCooldownKey(model)]; ok && cooldown.ResetAt.After(now) {
		return false, cooldown.Reason, cooldown.ResetAt
	}
	return true, "", time.Time{}
}
