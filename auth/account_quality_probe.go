package auth

import (
	"sync/atomic"
	"time"
)

func (a *Account) QualityUsagePercent(now time.Time) (float64, bool) {
	s := a.GetAccountListRuntimeSnapshot()
	used, ok := 0.0, s.UsagePercent5hValid || s.UsagePercent7dValid
	if s.UsagePercent5hValid && (s.Reset5hAt.IsZero() || s.Reset5hAt.After(now)) {
		used = s.UsagePercent5h
	}
	if s.UsagePercent7dValid && (s.Reset7dAt.IsZero() || s.Reset7dAt.After(now)) && s.UsagePercent7d > used {
		used = s.UsagePercent7d
	}
	return used, ok
}

// TakeAccountQualityProbe reserves a native account slot for an explicit quality
// sample. Isolated accounts remain probeable, but the request still shares the
// account's configured concurrency budget and must be released with Release.
// It never clears Disabled, status, cooldowns, or another guard's ownership.
func (s *Store) TakeAccountQualityProbe(id int64) *Account {
	if s == nil {
		return nil
	}
	account := s.FindByID(id)
	if account == nil {
		return nil
	}
	account.mu.RLock()
	limit := account.effectiveBaseConcurrencyLocked(atomic.LoadInt64(&s.maxConcurrency))
	if account.DynamicConcurrencyLimit > 0 && account.DynamicConcurrencyLimit < limit {
		limit = account.DynamicConcurrencyLimit
	}
	account.mu.RUnlock()
	if limit <= 0 {
		return nil
	}
	account.stateAdmissionMu.Lock()
	defer account.stateAdmissionMu.Unlock()
	for {
		occupied := atomic.LoadInt64(&account.OccupiedRequests)
		if occupied >= limit || atomic.LoadInt64(&account.ActiveRequests) >= limit {
			return nil
		}
		if atomic.CompareAndSwapInt64(&account.OccupiedRequests, occupied, occupied+1) {
			atomic.AddInt64(&account.ActiveRequests, 1)
			return account
		}
	}
}
