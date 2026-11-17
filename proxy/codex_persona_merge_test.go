package proxy

import (
	"path/filepath"
	"testing"
)

func TestPersonaRepairReservesAssignment(t *testing.T) {
	t.Setenv("CODEX_PERSONA_STORE", filepath.Join(t.TempDir(), "personas.json"))
	resetCodexPersonaAssignments()
	t.Cleanup(resetCodexPersonaAssignments)
	cfg := CodexUserAgentConfig{Mode: CodexUserAgentModePool, PoolMix: map[string]int{"codex-tui": 100}}
	ua, _, ok, err := codexPoolPersona(cfg, 76, "")
	if err != nil || !ok {
		t.Fatalf("repair unavailable: %v", err)
	}
	codexPersonaSlots.mu.Lock()
	remembered := false
	for _, slots := range codexPersonaSlots.byMix {
		if slots.ByUA[76] == ua {
			remembered = true
		}
	}
	codexPersonaSlots.mu.Unlock()
	if !remembered {
		t.Fatal("repair bypassed durable allocation")
	}
	for id := int64(1); id < 76; id++ {
		other, _, ok, _ := codexPoolPersona(cfg, id, "")
		if ok && other == ua {
			t.Fatalf("account %d reused reserved repair", id)
		}
	}
	resetCodexPersonaAssignments()
	after, _, ok, err := codexPoolPersona(cfg, 76, "")
	if err != nil || !ok || after != ua {
		t.Fatalf("repair lost after reload: %q %v", after, err)
	}
}

func TestPersonaStoreDefaultsToDurableDataDirectory(t *testing.T) {
	t.Setenv("CODEX_PERSONA_STORE", "")
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	if codexPersonaStorePath() != filepath.Join(dir, "codex-persona-slots.json") {
		t.Fatal("persona store bypassed data directory")
	}
}

func TestPersonaCatalogReloadKeepsExactAssignments(t *testing.T) {
	t.Setenv("CODEX_PERSONA_STORE", filepath.Join(t.TempDir(), "slots.json"))
	resetCodexPersonaAssignments()
	t.Cleanup(resetCodexPersonaAssignments)
	cfg := CodexUserAgentConfig{Mode: CodexUserAgentModePool, PoolMix: map[string]int{"codex-tui": 100, "codex-desktop": 100}}
	before := map[int64]string{}
	for id := int64(1); id <= 20; id++ {
		ua, _, ok, err := codexPoolPersona(cfg, id, "")
		if err != nil || !ok {
			t.Fatalf("before %d: %v", id, err)
		}
		before[id] = ua
	}
	resetCodexPersonaAssignments()
	seen := map[string]int64{}
	for id := int64(20); id >= 1; id-- {
		ua, _, ok, err := codexPoolPersona(cfg, id, "")
		if err != nil || !ok || ua != before[id] {
			t.Fatalf("after %d changed %q to %q: %v", id, before[id], ua, err)
		}
		if owner := seen[ua]; owner != 0 {
			t.Fatalf("accounts %d/%d collide", id, owner)
		}
		seen[ua] = id
	}
}

func TestPersonaRepairRespectsExplicitVersionOverrides(t *testing.T) {
	t.Setenv("CODEX_PERSONA_STORE", "memory")
	resetCodexPersonaAssignments()
	t.Cleanup(resetCodexPersonaAssignments)
	cfg := CodexUserAgentConfig{Mode: CodexUserAgentModePool, PoolMix: map[string]int{"codex-desktop": 100}, ClientVersion: "0.180.0", AppVersion: "99.1.1"}
	_, version, ok, err := codexPoolPersona(cfg, 78, "0.179.0")
	if err != nil || !ok || version != "0.180.0" {
		t.Fatalf("repair overrode user version: %s %v", version, err)
	}
}
