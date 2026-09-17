package database

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestStatePoolAtomicClaimsAndCancellation(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "states.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Unix(10000, 0)
	jobs := []StatePoolRow{}
	for i := 0; i < 12; i++ {
		jobs = append(jobs, StatePoolRow{ID: fmt.Sprint(i), AccountID: int64(i/4 + 1), Model: fmt.Sprint(i % 4), Effort: "high", Data: "{}", CreatedAt: now.Unix()})
	}
	ids, err := db.CreateStatePoolJobs(ctx, jobs)
	if err != nil || len(ids) != 12 {
		t.Fatalf("admission: %v %v", ids, err)
	}
	ids, err = db.CreateStatePoolJobs(ctx, jobs)
	if err != nil || len(ids) != 0 {
		t.Fatalf("duplicate admission: %v %v", ids, err)
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	claimed := []StatePoolRow{}
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			row, err := db.ClaimStatePoolJob(ctx, "owner", now)
			if err != nil {
				t.Error(err)
			}
			if row != nil {
				mu.Lock()
				claimed = append(claimed, *row)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(claimed) != 4 {
		t.Fatalf("got %d claims, want 4", len(claimed))
	}
	counts := map[int64]int{}
	for _, row := range claimed {
		counts[row.AccountID]++
	}
	for _, count := range counts {
		if count > 2 {
			t.Fatal("per-account capacity exceeded")
		}
	}
	row := claimed[0]
	if err := db.CancelStatePoolJob(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	if next, err := db.ClaimStatePoolJob(ctx, "other", now); err != nil || next != nil {
		t.Fatal("cancellation freed in-flight capacity early")
	}
	row.Status = "completed"
	if ok, err := db.UpdateStatePoolJob(ctx, row, now); err != nil || ok {
		t.Fatal("completion overrode cancellation")
	}
	if err := db.FinishCancelledStatePoolJob(ctx, row.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if next, err := db.ClaimStatePoolJob(ctx, "other", now); err != nil || next == nil {
		t.Fatal("cancelled capacity not released")
	}
	if next, err := db.ClaimStatePoolJob(ctx, "restart", now.Add(91*time.Second)); err != nil || next == nil {
		t.Fatal("stale lease not recovered")
	}
}

func TestStatePoolSequentialCandidatesAndProxyReservations(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "states.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Unix(10000, 0)
	if err := db.SetStatePoolLimits(ctx, 4, 2, 1); err != nil {
		t.Fatal(err)
	}
	_, err = db.CreateStatePoolJobs(ctx, []StatePoolRow{
		{ID: "a1", GroupID: "a", AccountID: 1, Model: "sol", Effort: "high", Serial: true, Ordinal: 1, Data: "{}", CreatedAt: now.Unix()},
		{ID: "a2", GroupID: "a", AccountID: 1, Model: "sol", Effort: "high", Serial: true, Ordinal: 2, Data: "{}", CreatedAt: now.Unix()},
		{ID: "b1", GroupID: "b", AccountID: 2, Model: "sol", Effort: "high", Ordinal: 1, Data: "{}", CreatedAt: now.Unix()},
	})
	if err != nil {
		t.Fatal(err)
	}
	a, err := db.ClaimStatePoolJob(ctx, "owner", now)
	if err != nil || a == nil || a.ID != "a1" {
		t.Fatal("first candidate not claimed")
	}
	b, err := db.ClaimStatePoolJob(ctx, "owner", now)
	if err != nil || b == nil || b.ID != "b1" {
		t.Fatal("sequential candidate ran concurrently")
	}
	if extra, err := db.ClaimStatePoolJob(ctx, "owner", now); err != nil || extra != nil {
		t.Fatal("sequential limit exceeded")
	}
	if ok, err := db.AcquireStatePoolRoute(ctx, a.ID, "owner", "proxy-a"); err != nil || !ok {
		t.Fatal("route not reserved")
	}
	if ok, err := db.AcquireStatePoolRoute(ctx, b.ID, "owner", "proxy-a"); err != nil || ok {
		t.Fatal("proxy capacity exceeded")
	}
	if ok, err := db.AcquireStatePoolRoute(ctx, b.ID, "owner", "proxy-b", "proxy-a"); err != nil || ok {
		t.Fatal("forward proxy bypassed shared capacity")
	}
	if ok, err := db.AcquireStatePoolRoute(ctx, b.ID, "owner", "proxy-b"); err != nil || !ok {
		t.Fatal("independent proxy was blocked")
	}
	if err := db.ReleaseStatePoolRoute(ctx, a.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	a.Status = "failed"
	if _, err := db.UpdateStatePoolJob(ctx, *a, now); err != nil {
		t.Fatal(err)
	}
	next, err := db.ClaimStatePoolJob(ctx, "owner", now)
	if err != nil || next == nil || next.ID != "a2" {
		t.Fatal("next sequential candidate not claimed")
	}
	if ok, err := db.AcquireStatePoolRoute(ctx, next.ID, "owner", "proxy-a"); err != nil || !ok {
		t.Fatal("finished request retained proxy capacity")
	}
}

func TestStatePoolLegacyJobMigrationPreservesData(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "states.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	for _, statement := range []string{
		`DROP TABLE codex_state_jobs`,
		`CREATE TABLE codex_state_jobs (id TEXT PRIMARY KEY,account_id BIGINT NOT NULL,model TEXT NOT NULL,effort TEXT NOT NULL,status TEXT NOT NULL,data TEXT NOT NULL,secret TEXT NOT NULL DEFAULT '',owner TEXT NOT NULL DEFAULT '',lease_until BIGINT NOT NULL DEFAULT 0,created_at BIGINT NOT NULL,updated_at BIGINT NOT NULL)`,
		`INSERT INTO codex_state_jobs(id,account_id,model,effort,status,data,created_at,updated_at) VALUES('legacy',1,'sol','high','completed','{"preserved":true}',1,1)`,
	} {
		if _, err := db.conn.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := db.ensureStatePoolSchema(ctx); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.ListStatePoolJobs(ctx)
	if err != nil || len(rows) != 1 || rows[0].GroupID != "legacy" || rows[0].Data != `{"preserved":true}` {
		t.Fatal("legacy data lost during migration")
	}
}

func TestStatePoolLargeMultiAccountBatch(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "states.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	rows := []StatePoolRow{}
	for account := int64(1); account <= 20; account++ {
		for model := 0; model < 4; model++ {
			group := fmt.Sprintf("%d/%d", account, model)
			for ordinal := 1; ordinal <= 3; ordinal++ {
				rows = append(rows, StatePoolRow{ID: fmt.Sprintf("%s/%d", group, ordinal), GroupID: group, Ordinal: ordinal, AccountID: account, Model: fmt.Sprint(model), Effort: "high", Data: "{}", CreatedAt: 1})
			}
		}
	}
	ids, err := db.CreateStatePoolJobs(ctx, rows)
	if err != nil || len(ids) != 240 {
		t.Fatalf("large batch rejected: %d %v", len(ids), err)
	}
	if again, err := db.CreateStatePoolJobs(ctx, rows); err != nil || len(again) != 0 {
		t.Fatal("large batch duplicated active groups")
	}
}
