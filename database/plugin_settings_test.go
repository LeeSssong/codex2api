package database

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/codex2api/plugins"
)

func TestPluginStoreSQLitePersistsVersionFlags(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "plugins.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer db.Close()
	store := NewPluginStore(db)
	want := plugins.Setting{ID: "quality-ops", Version: "2.0.0", Enabled: false, Flags: map[string]bool{"alerts": true}}
	if err := store.Put(context.Background(), want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := store.Get(context.Background(), want.ID)
	if err != nil || got.Version != want.Version || got.Enabled || !got.Flags["alerts"] {
		t.Fatalf("Get = %+v, err=%v", got, err)
	}
}
