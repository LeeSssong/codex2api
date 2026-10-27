package fixedegress

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// TrackRoute reserves a resource across replicas until the actual connection is
// closed. Heartbeats prevent remote retirement while a long stream still runs.
func (m *Manager) TrackRoute(ctx context.Context, r Route) (func(), error) {
	if !r.Enrolled {
		return func() {}, nil
	}
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return nil, e
	}
	id := hex.EncodeToString(b[:])
	if e := m.db.ClaimFixedEgressLease(ctx, id, r.Binding.ResourceID); e != nil {
		return nil, e
	}
	done := make(chan struct{})
	var once sync.Once
	release := func() {
		once.Do(func() {
			close(done)
			c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = m.db.ReleaseFixedEgressLease(c, id)
		})
	}
	go func() {
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = m.db.RenewFixedEgressLease(c, id)
				cancel()
			}
		}
	}()
	return release, nil
}
