package smartops

import "testing"

func TestPriorityDisabledPreservesOrder(t *testing.T) {
	c := DefaultPriorityConfig()
	got := RankCandidates(c, []Candidate{{ID: 2}, {ID: 1}})
	if len(got) != 2 || got[0].ID != 2 {
		t.Fatalf("disabled config changed candidates: %#v", got)
	}
}
func TestPriorityRanksEligibleAndExplores(t *testing.T) {
	c := DefaultPriorityConfig()
	c.Enabled = true
	got := RankCandidates(c, []Candidate{{ID: 1, Eligible: true, Signal: Signal{QualityPercent: 99, P90TTFTMs: 100, LoadPercent: 10, Cost: .1, Samples: 10}}, {ID: 2, Eligible: true, Signal: Signal{QualityPercent: 70, P90TTFTMs: 5000, LoadPercent: 90, Cost: 1, Samples: 0}}, {ID: 3, Eligible: false}})
	if len(got) != 2 || !got[0].Exploration && got[0].ID != 2 {
		t.Fatalf("expected exploration first: %#v", got)
	}
}
