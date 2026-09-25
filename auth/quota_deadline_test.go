package auth

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/codex2api/database"
)

func TestAuthoritativeQuotaDeadlineCannotBeShortened(t *testing.T) {
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "quota.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	id, err := db.InsertAccountWithCredentials(context.Background(), "quota", map[string]interface{}{"access_token": "token", "plan_type": "plus"}, "")
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(db, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1})
	t.Cleanup(store.Stop)
	a := &Account{DBID: id, AccessToken: "token", PlanType: "plus", Status: StatusReady}
	store.AddAccount(a)
	store.MarkResponsesRateLimited(a, 24*time.Hour)
	_, deadline := a.GetCooldownSnapshot()
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			switch i % 3 {
			case 0:
				store.MarkResponsesRateLimited(a, time.Minute)
			case 1:
				store.MarkResponsesPremium5hRateLimited(a, time.Now().Add(time.Hour))
			case 2:
				store.MarkTransientRateLimited(a, time.Second)
			}
		}(i)
	}
	wg.Wait()
	reason, got := a.GetCooldownSnapshot()
	if reason != ResponsesRateLimitedCooldownReason || !got.Equal(deadline) || a.IsAvailable() {
		t.Fatalf("authoritative deadline lost: %s %v, want %v", reason, got, deadline)
	}
	row, err := db.GetAccountByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if row.CooldownReason != ResponsesRateLimitedCooldownReason || !row.CooldownUntil.Valid || row.CooldownUntil.Time.Unix() != deadline.Unix() {
		t.Fatalf("persisted deadline lost: %s %v", row.CooldownReason, row.CooldownUntil)
	}
	// A delayed WHAM snapshot must not replace the authoritative reason and
	// thereby enable the active-turn continuation exception.
	store.MarkPremium5hRateLimited(a, time.Now().Add(time.Minute))
	if a.UsageLimitContinuationEligible() {
		t.Fatal("snapshot reopened an exhausted turn")
	}
	if _, got := a.GetCooldownSnapshot(); !got.Equal(deadline) {
		t.Fatal("snapshot shortened deadline")
	}
	a.SetUsagePercent7d(100)
	a.SetReset7dAt(time.Now().Add(time.Minute))
	if !store.MarkUsage7dRateLimited(a) {
		t.Fatal("missing 7d observation")
	}
	if reason, until := a.GetCooldownSnapshot(); reason != ResponsesRateLimitedCooldownReason || !until.Equal(deadline) {
		t.Fatal("7d metadata replaced authoritative cooldown")
	}
	if store.ClearUsageWindowCooldownSince(a, time.Now().Add(time.Second)) {
		t.Fatal("metadata-only recovery cleared authoritative quota")
	}
	if !store.ClearUsageLimitCooldownSince(a, time.Now().Add(time.Second)) {
		t.Fatal("a fresh successful Responses recovery did not clear quota")
	}
}
