package auth

import (
	"context"
	"github.com/codex2api/smartops"
	"sync/atomic"
	"testing"
)

func TestSmartOpsNativeSelectionAndDisabledFallback(t *testing.T) {
	first := newFastSchedulerTestAccount(1, HealthTierHealthy, 90, 4)
	second := newFastSchedulerTestAccount(2, HealthTierHealthy, 80, 4)
	store := &Store{accounts: []*Account{first, second}, maxConcurrency: 4}
	var on atomic.Bool
	on.Store(true)
	adapter := &smartOpsAdapter{gate: func(context.Context, string) bool { return on.Load() }}
	cfg := smartops.DefaultPriorityConfig()
	cfg.Enabled = true
	adapter.snapshot.Store(&smartOpsSnapshot{enabled: true, config: cfg, signals: map[int64]smartops.Signal{1: {Samples: 10, QualityPercent: 100, P90TTFTMs: 2900, Cost: 1}, 2: {Samples: 10, QualityPercent: 100, P90TTFTMs: 100, Cost: .1}}})
	store.smartOps.Store(adapter)
	chosen := store.NextExcludingWithDispatch(0, nil, nil, DispatchPolicyStandard)
	if chosen == nil || chosen.DBID != 2 {
		t.Fatalf("cached scoring ignored: %v", chosen)
	}
	store.Release(chosen)
	chosen = store.NextExcludingWithDispatch(0, map[int64]bool{2: true}, nil, DispatchPolicyStandard)
	if chosen == nil || chosen.DBID != 1 {
		t.Fatalf("exclusion ignored: %v", chosen)
	}
	store.Release(chosen)
	chosen = store.NextExcludingWithDispatch(0, nil, func(a *Account) bool { return a.DBID == 1 }, DispatchPolicyStandard)
	if chosen == nil || chosen.DBID != 1 {
		t.Fatalf("eligibility filter ignored: %v", chosen)
	}
	store.Release(chosen)
	on.Store(false)
	_, used := store.smartOpsAcquire(0, nil, nil, DispatchPolicyStandard)
	if used {
		t.Fatal("disabled module still intercepted native selection")
	}
}

func TestSmartOpsSessionProxyIsStableAndNativePoolOnly(t *testing.T) {
	s := &Store{proxyPool: []string{"http://127.0.0.1:31001", "http://127.0.0.1:31002"}}
	a := &Account{DBID: 1}
	first, e := s.ResolveSmartOpsBPSSessionProxy(a, "session")
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 10; i++ {
		next, e := s.ResolveSmartOpsBPSSessionProxy(a, "session")
		if e != nil || next != first {
			t.Fatalf("unstable proxy %q %v", next, e)
		}
	}
	s.proxyPool = nil
	if _, e = s.ResolveSmartOpsBPSSessionProxy(a, "session"); e == nil {
		t.Fatal("empty pool silently used an unconfigured proxy")
	}
}
func TestSmartOpsFastSchedulerRanksBeforeNativeAdmission(t *testing.T) {
	first := newFastSchedulerTestAccount(1, HealthTierHealthy, 90, 4)
	second := newFastSchedulerTestAccount(2, HealthTierHealthy, 80, 4)
	store := &Store{accounts: []*Account{first, second}, maxConcurrency: 4}
	adapter := &smartOpsAdapter{gate: func(context.Context, string) bool { return true }}
	cfg := smartops.DefaultPriorityConfig()
	cfg.Enabled = true
	adapter.snapshot.Store(&smartOpsSnapshot{enabled: true, config: cfg, signals: map[int64]smartops.Signal{1: {Samples: 10, QualityPercent: 100, P90TTFTMs: 2900, Cost: 1}, 2: {Samples: 10, QualityPercent: 100, P90TTFTMs: 100, Cost: .1}}})
	store.smartOps.Store(adapter)
	scheduler := store.BuildFastScheduler()
	chosen := scheduler.AcquireExcludingWithDispatch(0, nil, nil, DispatchPolicyStandard)
	if chosen == nil || chosen.DBID != 2 {
		t.Fatalf("fast scheduler ignored rank: %v", chosen)
	}
	store.Release(chosen)
	atomic.StoreInt32(&second.Disabled, 1)
	chosen = scheduler.AcquireExcludingWithDispatch(0, nil, nil, DispatchPolicyStandard)
	if chosen == nil || chosen.DBID != 1 {
		t.Fatalf("disabled candidate admitted: %v", chosen)
	}
	store.Release(chosen)
}
