package auth

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex2api/database"
)

func TestCodexPathEvidenceOrderingAndIsolation(t *testing.T) {
	a := &Account{DBID: 1}
	ctx := context.Background()
	now := time.Unix(100, 0)
	if a.CodexPathSnapshot("basispoints", "m", now).Capability != "unknown" {
		t.Fatal("legacy not unknown")
	}
	a.ObserveCodexPath(ctx, database.CodexCapability{Upstream: "basispoints", Model: "m", Capability: "supported", ObservedAt: now.UnixNano()})
	a.ObserveCodexPath(ctx, database.CodexCapability{Upstream: "basispoints", Model: "m", Capability: "unsupported", ObservedAt: now.Add(-time.Second).UnixNano()})
	a.SetCodexPathCooldown("basispoints", "m", "old", now.Add(-time.Second), now.Add(time.Hour))
	if s := a.CodexPathSnapshot("basispoints", "m", now); s.Capability != "supported" || s.Health != "ready" {
		t.Fatalf("stale failure: %+v", s)
	}
	for _, pair := range [][2]string{{"codex", "m"}, {"basispoints", "other"}} {
		if a.CodexPathSnapshot(pair[0], pair[1], now).Capability != "unknown" {
			t.Fatal("evidence leaked")
		}
	}
	a.codexRoutes.mu.Lock()
	a.codexRoutes.configs = map[string]bool{"basispoints": false}
	a.codexRoutes.mu.Unlock()
	a.ObserveCodexPath(ctx, database.CodexCapability{Upstream: "basispoints", Model: "m", Capability: "supported", ObservedAt: now.Add(time.Second).UnixNano()})
	if a.CodexPathSnapshot("basispoints", "m", now).Allowed {
		t.Fatal("success overrode admin disable")
	}
}
func TestCodexPathSingleRecoveryProbe(t *testing.T) {
	a := &Account{DBID: 1}
	now := time.Unix(100, 0)
	a.SetCodexPathCooldown("basispoints", "m", "transient", now, now.Add(time.Second))
	if _, ok := a.BeginCodexPath("basispoints", "m", now); ok {
		t.Fatal("cooldown ignored")
	}
	var won atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	releaseAll := make(chan struct{})
	contended := make(chan struct{}, 40)
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			release, ok := a.BeginCodexPath("basispoints", "m", now.Add(2*time.Second))
			contended <- struct{}{}
			if ok {
				won.Add(1)
				<-releaseAll
				release()
			}
		}()
	}
	close(start)
	// Every contender has evaluated the gate before the successful probe finishes.
	for range 40 {
		<-contended
	}
	if s := a.CodexPathSnapshot("codex", "m", now); s.Health != "ready" {
		t.Fatal("other path cooled")
	}
	close(releaseAll)
	wg.Wait()
	if won.Load() != 1 {
		t.Fatalf("probes=%d", won.Load())
	}
	if _, ok := a.BeginCodexPath("basispoints", "m", now.Add(3*time.Second)); ok {
		t.Fatal("inconclusive probe retried immediately")
	}
	a.ObserveCodexPath(context.Background(), database.CodexCapability{Upstream: "basispoints", Model: "m", Capability: "supported", ObservedAt: now.Add(4 * time.Second).UnixNano()})
	if s := a.CodexPathSnapshot("basispoints", "m", now.Add(5*time.Second)); s.Health != "ready" {
		t.Fatalf("success failed to recover: %+v", s)
	}
}

func TestCodexConcurrentEvidenceAndViews(t *testing.T) {
	a := &Account{DBID: 1}
	now := time.Unix(100, 0)
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			started := now.Add(time.Duration(i) * time.Second)
			a.SetCodexPathCooldown("basispoints", "M", "access", started, started.Add(time.Minute))
			a.ObserveCodexPath(context.Background(), database.CodexCapability{Upstream: "basispoints", Model: "M", Capability: "supported", ObservedAt: started.UnixNano()})
			a.CodexPathViews("", now)
		}()
	}
	wg.Wait()
	v := a.CodexPathSnapshot("basispoints", "m", now)
	if v.Capability != "supported" || v.ObservedAt != now.Add(39*time.Second).UnixNano() || v.Health != "ready" {
		t.Fatalf("stale concurrent result: %+v", v)
	}
	a.SetCodexPathCooldown("codex", "unobserved", "access", now, now.Add(time.Minute))
	found := false
	for _, view := range a.CodexPathViews("", now) {
		if view.Upstream == "codex" && view.Model == "unobserved" && view.Health == "cooldown" {
			found = true
		}
	}
	if !found {
		t.Fatal("health without capability evidence missing from admin views")
	}
}
