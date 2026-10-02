package credentialops

import (
	"context"
	"testing"
	"time"
)

func TestManagerRejectsUnencryptedSecrets(t *testing.T) {
	m := NewManager(ManagerConfig{Encrypt: func(string) (string, error) { return "", ErrEncryptionUnavailable }})
	_, err := m.SaveLoginConfig(context.Background(), LoginConfigInput{AccountID: 7, Email: "a@example.com", Password: "pw", TOTPSecret: "totp"})
	if err != ErrEncryptionUnavailable {
		t.Fatalf("error = %v, want encryption failure", err)
	}
}

func TestLeaseEngineCooldown(t *testing.T) {
	m := NewManager(ManagerConfig{})
	m.UpsertMonitor(Monitor{AccountID: 9, Enabled: true, NextProbeAt: time.Now().Add(-time.Second)})
	claimed := m.ClaimDue("worker", time.Minute, 1)
	if len(claimed) != 1 {
		t.Fatalf("claimed = %d", len(claimed))
	}
	cooldown := time.Now().Add(time.Hour)
	if err := m.CompleteProbe("worker", 9, ProbeResult{State: ProbeAuth, Detail: "expired", FailStreak: 2, CooldownUntil: &cooldown}); err != nil {
		t.Fatal(err)
	}
	if got := m.ClaimDue("worker-2", time.Minute, 1); len(got) != 0 {
		t.Fatalf("cooldown claim = %d", len(got))
	}
}
