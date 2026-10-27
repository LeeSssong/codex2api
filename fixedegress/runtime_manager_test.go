package fixedegress

import (
	"context"
	"github.com/codex2api/database"
	"path/filepath"
	"testing"
)

func TestManagerAcquireUnenrolledAndDisabledEnrollment(t *testing.T) {
	t.Setenv("CODEX_FIXED_EGRESS_KEY", "test-fixed-key")
	db, e := database.New("sqlite", filepath.Join(t.TempDir(), "test.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	accountID, e := db.InsertAccount(context.Background(), "fixed-test", "rt-fixed", "")
	if e != nil {
		t.Fatal(e)
	}
	m := NewManager(db, nil)
	r, e := m.Acquire(context.Background(), accountID)
	if e != nil || r.Enrolled {
		t.Fatalf("unenrolled %#v %v", r, e)
	}
	if e = db.SaveFixedEgressPool(context.Background(), database.FixedEgressPool{ID: "pool", Enabled: true, Strategy: "strict"}); e != nil {
		t.Fatal(e)
	}
	if e = db.EnrollFixedEgress(context.Background(), accountID, "pool"); e != nil {
		t.Fatal(e)
	}
	r, e = m.Acquire(context.Background(), accountID)
	if e == nil || !r.Enrolled {
		t.Fatal("disabled enrolled account allowed fallback")
	}
}
func TestManagerFailedEnrollmentLeavesNoEnrollment(t *testing.T) {
	t.Setenv("CODEX_FIXED_EGRESS_KEY", "test-fixed-key")
	db, e := database.New("sqlite", filepath.Join(t.TempDir(), "test.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	id, e := db.InsertAccount(context.Background(), "fixed", "rt", "")
	if e != nil {
		t.Fatal(e)
	}
	db.SaveFixedEgressPool(context.Background(), database.FixedEgressPool{ID: "pool", Enabled: true, Strategy: "stable"})
	m := NewManager(db, nil)
	if _, e = m.Enroll(context.Background(), id, "pool"); e == nil {
		t.Fatal("disabled enrollment accepted")
	}
	has, e := m.HasEnrollment(context.Background(), id)
	if e != nil || has {
		t.Fatalf("failed enrollment persisted %v %v", has, e)
	}
}
