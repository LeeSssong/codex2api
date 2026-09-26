package auth

import (
	"context"
	"github.com/codex2api/cache"
	"github.com/codex2api/database"
	"path/filepath"
	"testing"
	"time"
)

func TestBasispointsPolicyOutboxRefresh(t *testing.T) {
	ctx := context.Background()
	db, e := database.New("sqlite", filepath.Join(t.TempDir(), "policy.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	id, e := db.InsertAccount(ctx, "bps", "synthetic-refresh", "")
	if e != nil {
		t.Fatal(e)
	}
	store := NewStore(db, cache.NewMemory(8), &database.SystemSettings{MaxConcurrency: 2})
	defer store.Stop()
	if e = store.LoadAccountByID(ctx, id); e != nil {
		t.Fatal(e)
	}
	a := store.FindByID(id)
	if e = a.ReloadCodexRoutes(ctx); e != nil {
		t.Fatal(e)
	}
	if e = db.SetCodexPathAllowed(ctx, id, "basispoints", false); e != nil {
		t.Fatal(e)
	}
	if e = store.applySchedulerOutboxBatch(ctx, []database.SchedulerOutboxEvent{{ID: 1, EntityType: "account", EntityID: id, EventType: "codex_routes_updated"}, {ID: 2, EntityType: "account", EntityID: id, EventType: "updated"}}); e != nil {
		t.Fatal(e)
	}
	if a.CodexPathSnapshot("basispoints", "gpt-test", time.Now()).Allowed {
		t.Fatal("outbox left stale allowed cache")
	}
}
