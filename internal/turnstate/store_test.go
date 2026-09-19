package turnstate

import (
	"context"
	"testing"
	"time"

	"github.com/codex2api/cache"
)

func TestStoreKeepsNewestTicketAndSeparatesCredentials(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	store := NewStore(cache.NewMemory(4))
	key := Key{AccountID: 7, Model: HarvestModel, CredentialHash: "hash-a"}
	newer, _ := Parse(encodedTicket(t, 217, now.Add(-time.Minute)), now)
	older, _ := Parse(encodedTicket(t, 217, now.Add(-2*time.Minute)), now)
	if ok, err := store.Put(ctx, key, newer); err != nil || !ok {
		t.Fatalf("Put newer = %v, %v", ok, err)
	}
	if ok, err := store.Put(ctx, key, older); err != nil || ok {
		t.Fatalf("Put older = %v, %v", ok, err)
	}
	got, ok, err := store.Get(ctx, key, now)
	if err != nil || !ok || got.Raw != newer.Raw {
		t.Fatalf("Get = %#v, %v, %v", got, ok, err)
	}
	if _, ok, _ := store.Get(ctx, Key{AccountID: 7, Model: HarvestModel, CredentialHash: "hash-b"}, now); ok {
		t.Fatal("ticket leaked across credential hash")
	}
}

func TestStoreDeletesOnlyMatchingCurrentTicket(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	store := NewStore(cache.NewMemory(4))
	key := Key{AccountID: 8, Model: HarvestModel, CredentialHash: "hash"}
	old, _ := Parse(encodedTicket(t, 217, now.Add(-2*time.Minute)), now)
	current, _ := Parse(encodedTicket(t, 217, now.Add(-time.Minute)), now)
	_, _ = store.Put(ctx, key, current)
	deleted, err := store.DeleteIfMatch(ctx, key, old.Raw)
	if err != nil || deleted {
		t.Fatalf("stale delete = %v, %v", deleted, err)
	}
	deleted, err = store.DeleteIfMatch(ctx, key, current.Raw)
	if err != nil || !deleted {
		t.Fatalf("current delete = %v, %v", deleted, err)
	}
}
