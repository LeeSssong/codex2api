package admin

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestIntelligenceTickRequeuesRuntimeAdministrativelyDisabledAccount(t *testing.T) {
	f := newIntelligenceHoldFixture(t)
	ctx := context.Background()
	ids, err := f.db.EnqueueIntelligenceChecks(ctx, []int64{f.id}, time.Now())
	if err != nil || len(ids) != 1 {
		t.Fatalf("enqueue=%v err=%v", ids, err)
	}
	// Model disablement reaching the runtime after durable claim eligibility.
	// The database remains enabled so this exercises the runner's final fence.
	atomic.StoreInt32(&f.account.DispatchPaused, 1)
	if err := f.h.intelligenceTick(ctx); err != nil {
		t.Fatal(err)
	}
	f.h.WaitIntelligenceChecks()
	run, err := f.db.GetIntelligenceCheckRun(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "queued" || run.LeaseOwner != "" || run.LeaseUntil != 0 {
		t.Fatalf("skipped account retained execution lease: %+v", run)
	}
	if got := f.requests.Load(); got != 0 {
		t.Fatalf("disabled account sent %d requests", got)
	}
	if !f.account.AdministrativelyDisabled() {
		t.Fatal("runner cleared administrative disablement")
	}
	atomic.StoreInt32(&f.account.DispatchPaused, 0)
	claimed, err := f.db.ClaimIntelligenceCheck(ctx, "resumed", time.Now().Add(6*time.Second), time.Minute)
	if err != nil || claimed == nil || claimed.ID != ids[0] {
		t.Fatalf("requeued check did not become claimable: %+v err=%v", claimed, err)
	}
}

// The scheduler may deliberately hand a known hold to the runner for a neutral
// result. A cooldown first observed after that handoff must instead be retried.
func TestIntelligenceRunnerDistinguishesKnownAndNewQuotaHold(t *testing.T) {
	for _, known := range []bool{false, true} {
		name := "after_handoff"
		if known {
			name = "before_handoff"
		}
		t.Run(name, func(t *testing.T) {
			f := newIntelligenceHoldFixture(t)
			ctx := context.Background()
			if n, err := f.db.EnqueueDueIntelligenceChecks(ctx, time.Now(), 1); err != nil || n != 1 {
				t.Fatalf("enqueue=%d err=%v", n, err)
			}
			run, err := f.db.ClaimIntelligenceCheck(ctx, "worker", time.Now(), time.Minute, true)
			if err != nil || run == nil {
				t.Fatalf("claim=%+v err=%v", run, err)
			}
			if known {
				f.store.MarkCooldown(f.account, time.Hour, "usage_limit")
			}
			_, heldAtHandoff := f.h.intelligenceQuotaHold(f.account, run.Model)
			if !known {
				f.store.MarkCooldown(f.account, time.Hour, "usage_limit")
			}
			f.h.runIntelligenceCheck(ctx, *run, f.account, heldAtHandoff)
			stored, err := f.db.GetIntelligenceCheckRun(ctx, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantStatus := "queued"
			if known {
				wantStatus = "interrupted"
			}
			if stored.Status != wantStatus || (stored.CompletedAt != nil) != known || stored.LeaseOwner != "" || stored.LeaseUntil != 0 {
				t.Fatalf("handoff held=%t: %+v", heldAtHandoff, stored)
			}
			if f.requests.Load() != 0 {
				t.Fatal("quota-held account sent a request")
			}
			if state := f.state(); state.DegradeStreak != 0 || state.RecoverStreak != 0 {
				t.Fatalf("quota hold was graded: %+v", state)
			}
		})
	}
}
