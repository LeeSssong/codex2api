package security

import "testing"

func TestRequestMemoryImageRelayUsesNativeBudget(t *testing.T) {
	before := GetRequestMemorySnapshot()
	ConfigureRequestMemoryBudget(64)
	defer ConfigureRequestMemoryBudget(before.LimitBytes)
	body, ok := TryAcquireRequestMemory(48)
	if !ok {
		t.Fatal("body not admitted")
	}
	defer body.Release()
	if r, ok := TryAcquireImageRelayMemory(17); ok {
		r.Release()
		t.Fatal("image bypassed native memory budget")
	}
	r, ok := TryAcquireImageRelayMemory(16)
	if !ok {
		t.Fatal("remaining image memory refused")
	}
	r.Release()
	r.Release()
	if got := GetRequestMemorySnapshot().UsedBytes; got != before.UsedBytes+48 {
		t.Fatalf("image memory leaked: %d", got)
	}
}
func TestRequestMemoryImageRelaySlotsBoundBothKinds(t *testing.T) {
	var held []*ImageRelayMemoryReservation
	for i := 0; i < 32; i++ {
		r, ok := TryAcquireImageRelayMemory(1)
		if !ok {
			t.Fatalf("slot %d rejected", i)
		}
		held = append(held, r)
	}
	defer func() {
		for _, r := range held {
			r.Release()
		}
	}()
	if r, ok := TryAcquireImageRelayMemory(1); ok {
		r.Release()
		t.Fatal("33rd image read/conversion admitted")
	}
}
