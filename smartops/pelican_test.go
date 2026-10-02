package smartops

import (
	"context"
	"sync/atomic"
	"testing"
)

type testHistory struct{ n atomic.Int32 }

func (h *testHistory) SavePelicanResult(context.Context, PelicanResult) error { h.n.Add(1); return nil }
func TestRunGroupRetriesAndBoundsConcurrency(t *testing.T) {
	var active, max atomic.Int32
	var calls atomic.Int32
	h := &testHistory{}
	probe := func(ctx context.Context, _ int64, _ string) (string, error) {
		_ = ctx
		n := active.Add(1)
		for {
			old := max.Load()
			if n <= old || max.CompareAndSwap(old, n) {
				break
			}
		}
		defer active.Add(-1)
		if calls.Add(1) <= 2 {
			return "", context.DeadlineExceeded
		}
		return "<html></html>", nil
	}
	got, err := RunGroup(context.Background(), PelicanJob{ID: 1, AccountID: 2, Samples: 4, Parallel: 2, Retries: 1}, probe, h)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || h.n.Load() != 4 || max.Load() > 2 {
		t.Fatalf("results=%d saved=%d max=%d", len(got), h.n.Load(), max.Load())
	}
}
