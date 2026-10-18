package auth

import (
	"context"
	"testing"
	"time"

	"github.com/codex2api/database"
)

func TestCodexProbeGuardsRetestAndAccountSerialization(t *testing.T) {
	ctx := context.Background()
	a := &Account{DBID: 1, AccessToken: "synthetic", AccountID: "workspace", CredentialGeneration: 1}
	a.ObserveCodexPath(ctx, database.CodexCapability{Upstream: "basispoints", Model: "model", Capability: "unsupported", ObservedAt: time.Now().UnixNano()})
	release, reason := a.BeginCodexCapabilityProbe(ctx, "model")
	if reason != "" {
		t.Fatalf("learned evidence prevented explicit retest: %s", reason)
	}
	if _, reason := a.BeginCodexCapabilityProbe(ctx, "different-model"); reason != "probe_in_progress" {
		t.Fatalf("account allowed parallel probe: %s", reason)
	}
	release()
	a.codexRoutes.configs = map[string]bool{"basispoints": false}
	if _, reason := a.BeginCodexCapabilityProbe(ctx, "model"); reason != "path_disabled" {
		t.Fatalf("admin route permission bypassed: %s", reason)
	}
	a.codexRoutes.configs["basispoints"] = true
	a.SetCodexPathCooldown("basispoints", "model", "transient", time.Now().Add(time.Second), time.Now().Add(time.Minute))
	if _, reason := a.BeginCodexCapabilityProbe(ctx, "model"); reason != "path_cooldown" {
		t.Fatalf("cooldown bypassed: %s", reason)
	}
}

func TestCodexProbeMemoryGenerationAndLatestAttempt(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	a := &Account{DBID: 1, CredentialGeneration: 1}
	r := database.CodexProbeResult{AccountID: 1, Model: "model", Upstream: "basispoints", Level: "basic", Outcome: "supported", StartedAt: now}
	f := database.CodexCapability{Upstream: r.Upstream, Model: r.Model, Capability: "supported", ObservedAt: now.UnixNano()}
	if applied, err := a.SaveCodexCapabilityProbeResult(ctx, 1, r, &f); !applied || err != nil {
		t.Fatal(applied, err)
	}
	r.StartedAt = now.Add(-time.Second)
	r.Outcome = "unsupported"
	f.Capability = "unsupported"
	f.ObservedAt = r.StartedAt.UnixNano()
	if applied, err := a.SaveCodexCapabilityProbeResult(ctx, 1, r, &f); applied || err != nil {
		t.Fatal("stale result", applied, err)
	}
	a.mu.Lock()
	a.CredentialGeneration = 2
	a.mu.Unlock()
	oldProduction := f
	oldProduction.CredentialGeneration = 1
	oldProduction.ObservedAt = now.Add(time.Hour).UnixNano()
	a.ObserveCodexPath(ctx, oldProduction)
	if s := a.CodexPathSnapshot("basispoints", "model", now); s.Capability != "unknown" {
		t.Fatalf("old capability visible %+v", s)
	}
	if results, err := a.GetCodexCapabilityProbeResults(ctx); err != nil || len(results) != 0 {
		t.Fatal("old probes visible", results, err)
	}
	r.StartedAt = now.Add(time.Second)
	if applied, err := a.SaveCodexCapabilityProbeResult(ctx, 1, r, &f); applied || err != nil {
		t.Fatal("old generation result", applied, err)
	}
}

func TestCodexProbeMemoryResetRejectsInFlightResult(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	a := &Account{DBID: 1, CredentialGeneration: 1}
	a.codexRoutes.facts = map[string]database.CodexCapability{
		codexFactKey("basispoints", ""): {Source: "admin_reset", ObservedAt: now.Add(time.Second).UnixNano()},
	}
	r := database.CodexProbeResult{AccountID: 1, Model: "model", Upstream: "basispoints", Level: "basic", Outcome: "supported", StartedAt: now}
	f := database.CodexCapability{Upstream: r.Upstream, Model: r.Model, Capability: "supported", ObservedAt: now.UnixNano()}
	if applied, err := a.SaveCodexCapabilityProbeResult(ctx, 1, r, &f); applied || err != nil {
		t.Fatal("pre-reset probe result", applied, err)
	}
	if results, err := a.GetCodexCapabilityProbeResults(ctx); err != nil || len(results) != 0 {
		t.Fatal("pre-reset probe visible", results, err)
	}
}

func TestCodexProbeGenerationHidesCachedGlobalEvidence(t *testing.T) {
	a := &Account{DBID: 1, CredentialGeneration: 2}
	a.codexRoutes.facts = map[string]database.CodexCapability{
		codexFactKey("basispoints", ""): {Capability: "unsupported", CredentialGeneration: 1},
	}
	if snapshot := a.CodexPathSnapshot("basispoints", "model", time.Now()); snapshot.Capability != "unknown" {
		t.Fatalf("old global evidence visible: %+v", snapshot)
	}
}
