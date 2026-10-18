package auth

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStateBusinessBudgetIsAtomicAndSharesTotalSlots(t *testing.T) {
	account := &Account{DBID: 1, AccessToken: "test", Status: StatusReady}
	account.SetStateBusinessLimit(1)
	if !reserveOccupiedAccountSlot(account, 4, true) || !reserveOccupiedAccountSlot(account, 4, true) {
		t.Fatal("capture could not reserve slots")
	}
	var acquired atomic.Int32
	var workers sync.WaitGroup
	for range 20 {
		workers.Go(func() {
			if reserveOccupiedAccountSlot(account, 4) {
				acquired.Add(1)
			}
		})
	}
	workers.Wait()
	if acquired.Load() != 1 || account.GetActiveRequests() != 3 {
		t.Fatal("concurrent business requests bypassed stage cap")
	}
	if !reserveOccupiedAccountSlot(account, 4, true) || reserveOccupiedAccountSlot(account, 4, true) {
		t.Fatal("capture bypassed total account cap")
	}
	for range 3 {
		releaseStateCaptureSlot(account)
	}
	if account.GetActiveRequests() != 1 || account.stateCaptureRequests != 0 {
		t.Fatal("capture release corrupted business slots")
	}
	account.SetStateBusinessLimit(0)
	if !reserveOccupiedAccountSlot(account, 4) {
		t.Fatal("normal budget not restored")
	}
}

func TestStateBudgetIncludesBufferedSessionReclaim(t *testing.T) {
	store := NewStore(nil, nil, nil)
	t.Cleanup(store.Stop)
	store.SetSessionSlotBufferEnabled(true)
	store.SetSessionSlotBuffer(time.Hour)
	account := &Account{DBID: 1, AccessToken: "test", Status: StatusReady}
	store.AddAccount(account)
	if !reserveOccupiedAccountSlot(account, 4) {
		t.Fatal("initial reservation failed")
	}
	store.ReleaseForSession(account, "buffered")
	account.SetStateBusinessLimit(1)
	if !reserveOccupiedAccountSlot(account, 4) {
		t.Fatal("business reservation failed")
	}
	if store.tryReclaimSessionSlot(account, "buffered", false) {
		t.Fatal("buffered session bypassed stage business limit")
	}
	releaseOccupiedAccountSlot(account)
	if !store.tryReclaimSessionSlot(account, "buffered", false) {
		t.Fatal("buffered session could not resume after a slot freed")
	}
}
