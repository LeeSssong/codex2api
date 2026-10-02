package database

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestCredentialOpsTaskLeaseIsDurableAndExclusive(t *testing.T) {
	db, err := New("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.EnsureCredentialOpsSchema(ctx); err != nil {
		t.Fatal(err)
	}
	task, err := db.CreateCredentialOpsTask(ctx, 42, 7)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := db.ClaimCredentialOpsTask(ctx, "worker-a", time.Minute)
	if err != nil || claimed == nil || claimed.ID != task.ID {
		t.Fatalf("claim = %#v, %v", claimed, err)
	}
	if next, err := db.ClaimCredentialOpsTask(ctx, "worker-b", time.Minute); err != nil || next != nil {
		t.Fatalf("second claim = %#v, %v", next, err)
	}
	if err := db.CompleteCredentialOpsTask(ctx, task.ID, "worker-b", "failed", "failed", "wrong owner"); err == nil {
		t.Fatal("wrong worker completed task")
	}
	if err := db.CompleteCredentialOpsTask(ctx, task.ID, "worker-a", "succeeded", "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetCredentialOpsTask(ctx, task.ID)
	if err != nil || stored.Status != "succeeded" || stored.FinishedAt == nil {
		t.Fatalf("stored = %#v, %v", stored, err)
	}
}

func TestCredentialOpsPostgresTaskLease(t *testing.T) {
	dsn := os.Getenv("CODEX2API_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("requires isolated CODEX2API_TEST_POSTGRES_DSN")
	}
	db, err := New("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.EnsureCredentialOpsSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.ExecContext(ctx, `DELETE FROM credential_ops_tasks`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateCredentialOpsTask(ctx, 99, 1); err != nil {
		t.Fatal(err)
	}
	got, err := db.ClaimCredentialOpsTask(ctx, "pg-worker", time.Minute)
	if err != nil || got == nil || got.LeaseOwner != "pg-worker" {
		t.Fatalf("claim = %#v, %v", got, err)
	}
}
