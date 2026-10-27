package fixedegress

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/codex2api/database"
)

func TestEnrollmentPersistsBindingAndGenerationFence(t *testing.T) {
	t.Setenv("CODEX_FIXED_EGRESS_KEY", "test-fixed-egress-key")
	p := filepath.Join(t.TempDir(), "fixed-egress.db")
	db, err := database.New("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(db, nil)
	ctx := context.Background()
	accountID, err := db.InsertAccount(ctx, "fixed-test", "rt-fixed-test", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveFixedEgressConfig(ctx, database.FixedEgressConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := m.EnablePool(ctx, PoolConfig{ID: "pool1", Enabled: true, Strategy: "strict"}); err != nil {
		t.Fatal(err)
	}
	if err := m.UpsertResource(ctx, Resource{ID: "r1", PoolID: "pool1", ProxyURL: "http://127.0.0.1:8001", ObservedExitIP: "203.0.113.1", State: "available", Owned: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Enroll(ctx, accountID, "pool1"); err != nil {
		t.Fatal(err)
	}
	b, err := m.Resolve(ctx, accountID)
	if err != nil || b.ResourceID != "r1" {
		t.Fatalf("resolve: %#v %v", b, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = database.New("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	m = NewManager(db, nil)
	b, err = m.Resolve(ctx, accountID)
	if err != nil || b.ResourceID != "r1" {
		t.Fatalf("restart resolve: %#v %v", b, err)
	}
	if err := m.Rotate(ctx, accountID, b.Generation+1, "r1"); err == nil {
		t.Fatal("stale generation rotation unexpectedly succeeded")
	}
}
