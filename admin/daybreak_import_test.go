package admin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/codex2api/proxy"
)

// Identity/import fixtures must keep native warmup scheduling and persistence
// real while replacing only the external model manifest request.
func noNetworkImportedModels(context.Context, *auth.Account, string) (*proxy.CodexModelsManifest, error) {
	return nil, errors.New("model manifest offline in import fixture")
}

func TestImportedDaybreakUsesBoundAccountAndKeepsSnapshotOnFailure(t *testing.T) {
	h, store, db := newImportEndToEndHandler(t)
	id := insertTestAccount(t, db)
	if err := store.LoadAccountByID(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	account := store.FindByID(id)
	snapshot := database.DaybreakSnapshot{Identity: account.DaybreakIdentity(), ObservedAt: time.Now().UnixNano(), CheckedAt: time.Now().Unix(), Models: map[string][]string{"gpt-5.4": {auth.DaybreakBlue}}}
	account.ApplyDaybreakSnapshot(snapshot)
	var observed *auth.Account
	var observedProxy string
	h.fetchImportedModels = func(ctx context.Context, got *auth.Account, proxyURL string) (*proxy.CodexModelsManifest, error) {
		observed = got
		observedProxy = proxyURL
		return nil, errors.New("fixture unavailable")
	}
	h.refreshImportedDaybreak(context.Background(), id)
	if observed != account || observedProxy != store.ResolveProxyForAccount(account) {
		t.Fatal("native imported account/proxy was not used for model observation")
	}
	if got := account.DaybreakSnapshot(); got.ObservedAt != snapshot.ObservedAt || !account.SupportsDaybreak("gpt-5.4", auth.DaybreakBlue) {
		t.Fatal("failed model observation replaced the prior capability")
	}
}
