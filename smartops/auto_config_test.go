package smartops

import (
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

func TestApplyModelMappingsPreservesExplicitTarget(t *testing.T) {
	out := ApplyModelMappings(map[string]string{"gpt-5.4": "custom"}, []ModelMapping{{From: "gpt-5.4", To: "gpt-5.5"}, {From: "gpt-5.3", To: "gpt-5.5"}})
	if out["gpt-5.4"] != "custom" || out["gpt-5.3"] != "gpt-5.5" {
		t.Fatalf("mapping changed unexpectedly: %#v", out)
	}
}
