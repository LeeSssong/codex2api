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

func TestManagerDeduplicatesIdentityAndRejectsStaleCallback(t *testing.T) {
	m := NewManager(ManagerConfig{Encrypt: func(v string) (string, error) { return "enc:" + v, nil }, Decrypt: func(v string) (string, error) { return v[4:], nil }})
	first, err := m.ImportCredential(context.Background(), ImportInput{Email: "A@EXAMPLE.COM", AccountID: "acct-1", RefreshToken: "rt-1", Credential: map[string]any{"access_token": "at-1"}})
	if err != nil || !first.Created {
		t.Fatalf("first import = %#v, %v", first, err)
	}
	second, err := m.ImportCredential(context.Background(), ImportInput{Email: "a@example.com", AccountID: "acct-1", RefreshToken: "rt-2", Credential: map[string]any{"access_token": "at-2"}})
	if err != nil || second.AccountID != first.AccountID {
		t.Fatalf("dedup import = %#v, %v", second, err)
	}
	if err := m.ApplyCallback(context.Background(), Callback{AccountID: first.AccountID, Generation: first.Generation - 1, Credential: map[string]any{"access_token": "stale"}}); err != ErrStaleCallback {
		t.Fatalf("stale callback = %v", err)
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
