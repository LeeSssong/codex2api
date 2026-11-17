package auth

import (
	"context"
	"log"
	"time"
)

// persistUsageCooldown retries a failed usage-owned cooldown write without
// treating the retry as a new failure or changing the runtime deadline.
func (s *Store) persistUsageCooldown(acc *Account) {
	s.persistAccountCooldownState(acc, false, false)
}

// All native account cooldown writers/clearers publish through this critical
// section. Read current state only after acquiring the publication lock: a
// delayed frame, 401, or clear must never publish a captured older state.
// Never hold this lock while acquiring acc.usageSnapshotPersistMu.
func (s *Store) persistAccountCooldownState(acc *Account, force, clearError bool) {
	if s == nil || acc == nil {
		return
	}
	s.credentialRecoveryMu.RLock()
	defer s.credentialRecoveryMu.RUnlock()
	if acc.credentialRuntimeRetired.Load() {
		return
	}
	acc.usageCooldownPersistMu.Lock()
	defer acc.usageCooldownPersistMu.Unlock()
	acc.mu.RLock()
	pending := acc.usageCooldownPersistPending
	clearError = clearError || acc.usageCooldownClearErrorPending
	status, errorMsg := acc.Status, acc.ErrorMsg
	reason, until := acc.CooldownReason, acc.CooldownUtil
	generation := acc.CredentialGeneration
	// Accounts created before generation tracking belong to DB generation 1.
	if generation <= 0 {
		generation = 1
	}
	acc.mu.RUnlock()
	// A failed clear must also be retried: if the DB write of a runtime clear is
	// lost, the stale persisted cooldown survives and any later scheduler-outbox
	// reload (issue #813) resurrects it, blocking a healthy account with 429s.
	// The guard therefore only requires a pending write, for any runtime status.
	if !force && !pending {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var err error
	applied := true
	if s.db != nil {
		state := "clear_cooldown"
		if status == StatusCooldown {
			state = "cooldown"
		} else if status == StatusError {
			state = "error"
		} else if clearError {
			state = "clear_error"
		}
		var deadline *time.Time
		if status == StatusCooldown {
			deadline = &until
		}
		applied, err = s.db.UpdateAccountRuntimeStateForGeneration(ctx, acc.DBID, generation, state, errorMsg, reason, deadline)
	}
	if err != nil {
		log.Printf("[账号 %d] 持久化用量冷却失败: %v", acc.DBID, err)
		// Keep a failed write pending so a later usage-frame publication
		// retries it (issue #813). Otherwise a lost clear leaves the stale
		// cooldown in the database forever.
		acc.mu.Lock()
		acc.usageCooldownPersistPending = true
		// Preserve the operation as well as the dirty bit. ClearCooldown does
		// not remove a persisted error, so it cannot retry a failed ClearError.
		// The status switch above still gives newer errors/cooldowns priority.
		acc.usageCooldownClearErrorPending = clearError && status != StatusError && status != StatusCooldown
		acc.mu.Unlock()
		return
	}
	if !applied {
		return
	}
	if status == StatusCooldown {
		s.setCachedAccountCooldown(acc.DBID, reason, until, generation)
	} else {
		s.deleteCachedAccountCooldown(acc.DBID)
	}
	acc.mu.Lock()
	if acc.Status == status && acc.ErrorMsg == errorMsg && acc.CooldownReason == reason && acc.CooldownUtil.Equal(until) {
		acc.usageCooldownPersistPending = false
		acc.usageCooldownClearErrorPending = false
	}
	acc.mu.Unlock()
}
