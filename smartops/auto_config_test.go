package smartops

import (
	"encoding/json"
	"testing"
	"time"
)

func TestOAuthDefaultsAndProgression(t *testing.T) {
	c := DefaultOAuthAutoConfig()
	c.Enabled = true
	c.GroupIDs = []int64{7}
	c.UpgradeGroupIDs = []int64{7}
	c.UpgradeEnabled = true
	c.SuccessesPerStep = 2
	c.UpgradeStep = 2
	c.MaxConcurrency = 6
	c.Revision = "r1"
	if err := ValidateOAuthAutoConfig(c); err != nil {
		t.Fatal(err)
	}
	s := ConcurrencyState{Revision: "r1", Concurrency: 3}
	now := time.Unix(10, 0)
	s, current := AdvanceConcurrency(s, 3, c, ConcurrencyResult{Success: true}, now)
	if current != 3 || s.Successes != 1 {
		t.Fatalf("first success: %#v %d", s, current)
	}
	s, current = AdvanceConcurrency(s, current, c, ConcurrencyResult{Success: true}, now)
	if current != 5 || s.Successes != 0 {
		t.Fatalf("upgrade: %#v %d", s, current)
	}
	_, current = AdvanceConcurrency(s, current, c, ConcurrencyResult{Success: false}, now)
	if current != 5 {
		t.Fatal("failure must not reduce native concurrency")
	}
}

func TestAdvanceConcurrencySetsPromotionCooldownAndUsesRequestStartFence(t *testing.T) {
	c := DefaultOAuthAutoConfig()
	c.UpgradeEnabled = true
	c.SuccessesPerStep = 1
	c.UpgradeStep = 2
	c.MaxConcurrency = 4
	c.CooldownSeconds = 30
	c.Revision = "r1"
	now := time.Unix(100, 0)

	s, current := AdvanceConcurrency(ConcurrencyState{Revision: c.Revision, Concurrency: 2}, 2, c, ConcurrencyResult{Success: true, StartedAt: now}, now)
	if current != 4 || !s.PausedUntil.Equal(now.Add(30*time.Second)) {
		t.Fatalf("promotion = %#v, %d", s, current)
	}
	// A request that began during the promotion cooldown cannot count after it ends.
	s, current = AdvanceConcurrency(s, current, c, ConcurrencyResult{Success: true, StartedAt: now.Add(time.Second)}, now.Add(time.Minute))
	if current != 4 || s.Successes != 0 {
		t.Fatalf("stale request advanced: %#v, %d", s, current)
	}
}

func TestConcurrencyStateDecodesLegacyCapitalizedFields(t *testing.T) {
	var state ConcurrencyState
	if err := json.Unmarshal([]byte(`{"Revision":"old","Concurrency":3,"Successes":2,"PausedUntil":"1970-01-01T00:01:40Z"}`), &state); err != nil {
		t.Fatal(err)
	}
	if state.Revision != "old" || state.Concurrency != 3 || state.Successes != 2 || state.PausedUntil.Unix() != 100 {
		t.Fatalf("legacy state = %#v", state)
	}
}

func TestAdvanceConcurrencyDoesNotAccumulateAtMaximum(t *testing.T) {
	c := DefaultOAuthAutoConfig()
	c.UpgradeEnabled, c.SuccessesPerStep, c.MaxConcurrency, c.Revision = true, 2, 3, "r1"
	s, current := AdvanceConcurrency(ConcurrencyState{Revision: c.Revision, Concurrency: 3, Successes: 1}, 3, c, ConcurrencyResult{Success: true}, time.Now())
	if current != 3 || s.Successes != 1 {
		t.Fatalf("cap state = %#v, %d", s, current)
	}
}

func TestApplyModelMappingsPreservesExplicitTarget(t *testing.T) {
	out := ApplyModelMappings(map[string]string{"gpt-5.4": "custom"}, []ModelMapping{{From: "gpt-5.4", To: "gpt-5.5"}, {From: "gpt-5.3", To: "gpt-5.5"}})
	if out["gpt-5.4"] != "custom" || out["gpt-5.3"] != "gpt-5.5" {
		t.Fatalf("mapping changed unexpectedly: %#v", out)
	}
}
