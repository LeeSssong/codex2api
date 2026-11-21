package database

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPinnedIPv6ImportReserveCommitAndFailClosed(t *testing.T) {
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	db, ips := manualIPv6Fixture(t)
	ctx := context.Background()
	cfg := CredentialOpsLoginConfigRow{LoginEmail: "strict@example.test", CredentialMode: "password_totp", Engine: "local_worker", ProxySource: "direct", PasswordCiphertext: "sealed", PinnedIPv6: ips[0]}
	task, err := db.CreateCredentialOpsTaskWithConfig(ctx, 0, 0, cfg)
	if err != nil {
		t.Fatal(err)
	}
	competing := cfg
	competing.LoginEmail = "competing@example.test"
	if _, err = db.CreateCredentialOpsTaskWithConfig(ctx, 0, 0, competing); !errors.Is(err, ErrIPv6SelectionConflict) {
		t.Fatalf("overlapping reservations: %v", err)
	}
	if route, _, err := db.ClaimIPv6Egress(ctx, 1, ips, "", "", time.Now().Unix(), true); err != nil || route.IP != ips[1] {
		t.Fatalf("rotation allocated a reserved address: %+v %v", route, err)
	}
	claim, err := db.ClaimCredentialOpsTask(ctx, "pinned-worker", time.Minute)
	if err != nil || claim == nil || claim.ID != task.ID {
		t.Fatalf("claim = %+v, %v", claim, err)
	}
	if err := db.CheckPinnedIPv6Reservation(ctx, claim.ID, ips[0]); err != nil {
		t.Fatal(err)
	}
	credentials := map[string]any{"email": cfg.LoginEmail, "workspace_id": "strict-workspace", "access_token": "synthetic", "refresh_token": "synthetic-refresh"}
	id, err := db.CommitCredentialOpsLogin(ctx, task.ID, "pinned-worker", claim.Attempt, credentials)
	if err != nil {
		t.Fatal(err)
	}
	row, err := db.GetAccountByID(ctx, id)
	if err != nil || row.GetCredential(PinnedIPv6CredentialKey) != ips[0] {
		t.Fatalf("pinned credential not persisted: %v", err)
	}
	competing.LoginEmail, competing.PinnedIPv6 = cfg.LoginEmail, ""
	if _, err := db.CreateCredentialOpsTaskWithConfig(ctx, 0, 0, competing); err == nil {
		t.Fatal("a first import must not re-login a pinned account on a different route")
	}
	if got, err := db.CurrentIPv6EgressIP(ctx, id); err != nil || got != ips[0] {
		t.Fatalf("binding = %q, %v", got, err)
	}
	if err := db.ReleasePinnedIPv6Import(ctx, task.ID, claim.Attempt); err != nil {
		t.Fatal(err)
	}
	if err := db.CheckPinnedIPv6Reservation(ctx, task.ID, ips[0]); !errors.Is(err, ErrIPv6Unavailable) {
		t.Fatalf("temporary reservation not released: %v", err)
	}
	for _, test := range []struct {
		local    []string
		previous string
	}{{ips[1:], ""}, {ips, ips[0]}} {
		if _, _, err := db.ClaimIPv6Egress(ctx, id, test.local, test.previous, "connect_failed", time.Now().Unix(), true); !errors.Is(err, ErrIPv6Unavailable) {
			t.Fatalf("strict route should fail closed: %v", err)
		}
	}
	if _, err := db.AssignIPv6Egress(ctx, id, ips, ips[1], ips[0], time.Now().Unix()); !errors.Is(err, ErrIPv6SelectionConflict) {
		t.Fatalf("manual switch of a pin: %v", err)
	}
	if got, err := db.CurrentIPv6EgressIP(ctx, id); err != nil || got != ips[0] {
		t.Fatalf("binding changed after failures: %q, %v", got, err)
	}
}

func TestPinnedIPv6ImportCancelOnlyReleasesIdleReservation(t *testing.T) {
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	db, ips := manualIPv6Fixture(t)
	ctx := context.Background()
	cfg := CredentialOpsLoginConfigRow{LoginEmail: "strict@example.test", CredentialMode: "password_totp", Engine: "local_worker", ProxySource: "direct", PasswordCiphertext: "sealed", PinnedIPv6: ips[0]}
	queued, err := db.CreateCredentialOpsTaskWithConfig(ctx, 0, 0, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CancelCredentialOpsTask(ctx, queued.ID); err != nil {
		t.Fatal(err)
	}
	task, err := db.CreateCredentialOpsTaskWithConfig(ctx, 0, 0, cfg)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := db.ClaimCredentialOpsTask(ctx, "pinned-worker", time.Minute)
	if err != nil || claim == nil || claim.ID != task.ID {
		t.Fatalf("claim = %+v, %v", claim, err)
	}
	if err := db.CancelCredentialOpsTask(ctx, claim.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.CheckPinnedIPv6Reservation(ctx, claim.ID, ips[0]); err != nil {
		t.Fatalf("released before child exit: %v", err)
	}
	if err := db.ReleasePinnedIPv6Import(ctx, claim.ID, claim.Attempt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateCredentialOpsTaskWithConfig(ctx, 0, 0, cfg); err != nil {
		t.Fatalf("address unavailable after child exit: %v", err)
	}
}
