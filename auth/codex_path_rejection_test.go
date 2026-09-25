package auth

import (
	"context"
	"testing"
	"time"

	"github.com/codex2api/database"
)

func TestCodexUsageRejectionBackoffDoesNotExhaustAccount(t *testing.T) {
	a := &Account{DBID: 1}
	now := time.Unix(1000, 0)
	for _, delay := range []time.Duration{30, 60, 120, 240, 300, 300} {
		a.NoteCodexPathUsageRejection("basispoints", "m", now, now)
		snapshot := a.CodexPathSnapshot("basispoints", "m", now)
		if snapshot.Health != "cooldown" || snapshot.Capability != "unknown" || snapshot.CooldownUntil.Sub(now) != delay*time.Second {
			t.Fatalf("rejection changed capability or wrong backoff: %+v", snapshot)
		}
		// Other in-flight failures cannot slide the recovery deadline.
		a.NoteCodexPathUsageRejection("basispoints", "m", now.Add(time.Second), now.Add(2*time.Second))
		if got := a.CodexPathSnapshot("basispoints", "m", now); !got.CooldownUntil.Equal(snapshot.CooldownUntil) {
			t.Fatalf("concurrent failure extended cooldown: %+v", got)
		}
		a.NoteCodexPathUsageRejection("basispoints", "m", now.Add(time.Second), snapshot.CooldownUntil.Add(time.Second))
		if got := a.CodexPathSnapshot("basispoints", "m", now); !got.CooldownUntil.Equal(snapshot.CooldownUntil) {
			t.Fatal("late completion of an old request became a failed recovery probe")
		}
		for _, pair := range [][2]string{{"codex", "m"}, {"basispoints", "other"}} {
			if got := a.CodexPathSnapshot(pair[0], pair[1], now); got.Health != "ready" {
				t.Fatalf("unrelated path was cooled: %+v", got)
			}
		}
		now = snapshot.CooldownUntil.Add(time.Second)
	}
	a.ObserveCodexPath(context.Background(), database.CodexCapability{Upstream: "codex", Model: "m", Capability: "supported", ObservedAt: now.UnixNano()})
	if a.CodexPathSnapshot("basispoints", "m", now).Health == "ready" {
		t.Fatal("native success cleared BPS health")
	}
	a.ObserveCodexPath(context.Background(), database.CodexCapability{Upstream: "basispoints", Model: "m", Capability: "supported", ObservedAt: now.UnixNano()})
	if a.CodexPathSnapshot("basispoints", "m", now).Health != "ready" {
		t.Fatal("BPS success did not restore its path")
	}
	now = now.Add(time.Second)
	a.NoteCodexPathUsageRejection("basispoints", "m", now, now)
	if a.CodexPathSnapshot("basispoints", "m", now).CooldownUntil.Sub(now) != 30*time.Second {
		t.Fatal("success did not reset backoff")
	}
	if a.FreshDispatchUsageLimited() {
		t.Fatal("ambiguous rejection became an account quota failure")
	}
}

func TestCodexUsageRejectionPreservesStrongerAndNewerEvidence(t *testing.T) {
	a := &Account{DBID: 1}
	now := time.Unix(1000, 0)
	a.SetCodexPathCooldown("basispoints", "m", "upstream_access", now, now.Add(time.Hour))
	a.NoteCodexPathUsageRejection("basispoints", "m", now.Add(time.Second), now.Add(time.Second))
	if got := a.CodexPathSnapshot("basispoints", "m", now); got.HealthReason != "upstream_access" || !got.CooldownUntil.Equal(now.Add(time.Hour)) {
		t.Fatalf("weaker evidence replaced existing cooldown: %+v", got)
	}
	a.ObserveCodexPath(context.Background(), database.CodexCapability{Upstream: "basispoints", Model: "m", Capability: "supported", ObservedAt: now.Add(2 * time.Second).UnixNano()})
	a.NoteCodexPathUsageRejection("basispoints", "m", now.Add(time.Second), now.Add(3*time.Second))
	if got := a.CodexPathSnapshot("basispoints", "m", now); got.Health != "ready" {
		t.Fatalf("stale failure overrode newer success: %+v", got)
	}
}
