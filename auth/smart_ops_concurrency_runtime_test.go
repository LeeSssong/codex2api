package auth

import (
	"context"
	"testing"
	"time"

	"github.com/codex2api/smartops"
)

func TestBeginSmartOpsConcurrencyObservationCapturesRequestStartSnapshot(t *testing.T) {
	store := &Store{}
	adapter := &smartOpsAdapter{
		observe: make(chan smartops.ConcurrencyObservation, 1),
		gate:    func(context.Context, string) bool { return true },
	}
	adapter.auto.Store(&smartOpsAutoSnapshot{enabled: true, config: smartops.OAuthAutoConfig{
		UpgradeEnabled: true,
		Revision:       "revision-at-start",
	}})
	adapter.autoEpoch.Store(23)
	store.smartOps.Store(adapter)

	acc := &Account{DBID: 41, BaseConcurrencyEffective: 7}
	before := time.Now()
	observation := store.BeginSmartOpsConcurrencyObservation(acc)

	if observation.AccountID != 41 || observation.Revision != "revision-at-start" || observation.Epoch != 23 || observation.CurrentConcurrency != 7 {
		t.Fatalf("request-start observation = %+v", observation)
	}
	if observation.StartedAt.Before(before) || observation.StartedAt.After(time.Now()) {
		t.Fatalf("observation start time %v was not captured at request start", observation.StartedAt)
	}
}

func TestSmartOpsConcurrencyUpgradeDoesNotDependOnAccountCreationDefaults(t *testing.T) {
	store := &Store{}
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		store.WaitSmartOps()
	}()
	store.StartSmartOps(ctx, func(context.Context, string) bool { return true }, func(context.Context) (smartops.OAuthAutoConfig, smartops.PriorityConfig, error) {
		return smartops.OAuthAutoConfig{UpgradeEnabled: true, Revision: "upgrade-only"}, smartops.DefaultPriorityConfig(), nil
	})

	acc := &Account{DBID: 42, BaseConcurrencyEffective: 3}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if observation := store.BeginSmartOpsConcurrencyObservation(acc); observation.Revision == "upgrade-only" {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("upgrade-only configuration did not enable concurrency observation")
}

func TestReportSmartOpsConcurrencyObservationPausesWhenQueueIsFull(t *testing.T) {
	store := &Store{}
	adapter := &smartOpsAdapter{
		observe: make(chan smartops.ConcurrencyObservation, 1),
		gate:    func(context.Context, string) bool { return true },
	}
	adapter.auto.Store(&smartOpsAutoSnapshot{enabled: true, config: smartops.OAuthAutoConfig{Enabled: true, UpgradeEnabled: true, Revision: "one"}})
	store.smartOps.Store(adapter)

	store.ReportSmartOpsConcurrencyObservation(smartops.ConcurrencyObservation{AccountID: 1, Revision: "one"})
	store.ReportSmartOpsConcurrencyObservation(smartops.ConcurrencyObservation{AccountID: 2, Revision: "one"})

	paused, reason := store.SmartOpsConcurrencyStatus()
	if !paused || reason == "" {
		t.Fatalf("queue overflow must pause progression, got paused=%v reason=%q", paused, reason)
	}
}

func TestSmartOpsRestartMarkersPauseNewAccountsAtCapacityWithoutResettingKnownAccounts(t *testing.T) {
	adapter := &smartOpsAdapter{}
	seen := make(map[int64]bool, smartOpsRestartSeenLimit)
	for id := int64(1); id <= smartOpsRestartSeenLimit; id++ {
		seen[id] = true
	}

	known, ok := adapter.prepareSmartOpsConcurrencyObservation(smartops.ConcurrencyObservation{AccountID: 1}, seen)
	if !ok || known.Reset {
		t.Fatalf("known account must continue without a synthetic reset: %+v ok=%v", known, ok)
	}
	unknown, ok := adapter.prepareSmartOpsConcurrencyObservation(smartops.ConcurrencyObservation{AccountID: smartOpsRestartSeenLimit + 1}, seen)
	if ok || unknown.Reset {
		t.Fatalf("new account must not be processed after marker capacity: %+v ok=%v", unknown, ok)
	}
	if !adapter.blocked.Load() {
		t.Fatal("marker capacity must pause progression")
	}
	adapter.blockMu.RLock()
	reason := adapter.blockReason
	adapter.blockMu.RUnlock()
	if reason == "" {
		t.Fatal("marker capacity pause must expose a reason")
	}
}
