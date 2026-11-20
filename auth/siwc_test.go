package auth

import (
	"context"
	"github.com/codex2api/database"
	"path/filepath"
	"testing"
)

func TestSIWCStandaloneDispatchNeedsNoNativeToken(t *testing.T) {
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "siwc.db"))
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(db, nil, &database.SystemSettings{MaxConcurrency: 1, FastSchedulerEnabled: true, LazyMode: true})
	t.Cleanup(func() { store.Stop(); db.Close() })
	id, err := db.InsertAccountWithCredentials(context.Background(), "siwc", map[string]any{"email": "test@example.com", "siwc_account": true, "siwc_enabled": true}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	account := store.Next()
	if account == nil {
		t.Fatal("selected SIWC account not dispatchable without native token")
	}
	defer store.Release(account)
	if account.ID() != id || account.GetAccessToken() != "" || !account.UsesSIWC() {
		t.Fatal("native credential was invented or wrong account selected")
	}
}
