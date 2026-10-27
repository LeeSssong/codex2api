package auth

import (
	"context"
	"github.com/codex2api/database"
	"path/filepath"
	"testing"
)

func TestAccountStatsPricingPersistsAndLoads(t *testing.T) {
	db, e := database.New("sqlite", filepath.Join(t.TempDir(), "stats.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	id, e := db.InsertAccountWithCredentials(context.Background(), "stats", map[string]any{"refresh_token": "stats-test-rt"}, "")
	if e != nil {
		t.Fatal(e)
	}
	s := NewStore(db, nil, &database.SystemSettings{MaxConcurrency: 1, TestConcurrency: 1})
	defer s.Stop()
	if e := s.LoadAccountByID(context.Background(), id); e != nil {
		t.Fatal(e)
	}
	zero := 0.0
	cfg := database.AccountStatsPricingConfig{RateMultiplier: &zero, ApplySalePricing: true}
	if e := s.SetAccountStatsPricing(context.Background(), id, cfg); e != nil {
		t.Fatal(e)
	}
	if got := s.FindByID(id).AccountStatsPricing(); got.RateMultiplier == nil || *got.RateMultiplier != 0 || !got.ApplySalePricing {
		t.Fatalf("runtime config %+v", got)
	}
	row, e := db.GetAccountByID(context.Background(), id)
	if e != nil {
		t.Fatal(e)
	}
	got := accountStatsPricingFromRow(row)
	if got.RateMultiplier == nil || *got.RateMultiplier != 0 || !got.ApplySalePricing {
		t.Fatalf("persisted config %+v", got)
	}
	cfg.RateMultiplier = new(float64)
	*cfg.RateMultiplier = -1
	if e := s.SetAccountStatsPricing(context.Background(), id, cfg); e == nil {
		t.Fatal("accepted negative multiplier")
	}
}
