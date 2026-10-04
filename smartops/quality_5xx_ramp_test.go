package smartops

import (
	"testing"
	"time"
)

func TestQuality5xxRampFailureAndOwnedRecovery(t *testing.T) {
	c := DefaultOAuthAutoConfig()
	c.Revision = "r"
	c.Quality5xx = Quality5xxRampConfig{Enabled: true, Floor: 5, CooldownSeconds: 10}
	c.UpgradeEnabled = true
	c.SuccessesPerStep = 1
	c.UpgradeStep = 1
	c.MaxConcurrency = 20
	now := time.Unix(100, 0)
	s := Quality5xxRampState{}.ResetOnFailure(c.Quality5xx, 9, 3, c.Revision, now)
	if s.CurrentConcurrency != 5 || s.OriginalConcurrency != 9 || !s.ProbePending {
		t.Fatalf("state=%+v", s)
	}
	s, n := s.ProbeResult(c, 5, 3, c.Revision, false, true, now)
	if n != 5 || !s.Active {
		t.Fatalf("failed probe recovered: %+v n=%d", s, n)
	}
	s, n = s.ProbeResult(c, 5, 3, c.Revision, true, false, now)
	if n != 5 || !s.Active {
		t.Fatalf("inconclusive probe recovered: %+v n=%d", s, n)
	}
	s, n = s.ProbeResult(c, 5, 3, c.Revision, true, true, now)
	if n != 6 || s.Active == false {
		t.Fatalf("first success progression state=%+v n=%d", s, n)
	}
}

func TestQuality5xxRampStaleGenerationIgnored(t *testing.T) {
	c := DefaultOAuthAutoConfig()
	c.Revision = "r"
	c.Quality5xx = Quality5xxRampConfig{Enabled: true, Floor: 5, CooldownSeconds: 1}
	s := Quality5xxRampState{}.ResetOnFailure(c.Quality5xx, 8, 1, "r", time.Now())
	got, n := s.ProbeResult(c, 5, 2, "r", true, true, time.Now())
	if n != 5 || got.Attempt != s.Attempt || got.CurrentConcurrency != s.CurrentConcurrency {
		t.Fatalf("stale result changed state: %+v n=%d", got, n)
	}
}
