package auth

import (
	"context"
	"database/sql"
	"encoding/base64"
	"github.com/codex2api/cache"
	"github.com/codex2api/database"
	"path/filepath"
	"testing"
	"time"
)

func TestTokenGuardPublicationCommitFailureIsNotReportedSuccessful(t *testing.T) {
	ctx := context.Background()
	db, err := database.New("sqlite", "file:"+filepath.Join(t.TempDir(), "commit-failure.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	jwt := "e30." + base64.RawURLEncoding.EncodeToString([]byte("{\"email\":\"guard@example.com\",\"https://api.openai.com/auth\":{\"chatgpt_account_id\":\"workspace\"}}")) + ".sig"
	id, err := db.InsertAccountWithCredentials(ctx, "guard", map[string]any{"refresh_token": "old-rt", "access_token": jwt, "id_token": jwt}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.WithAccountControlTx(ctx, func(tx *sql.Tx) error {
		for _, q := range []string{
			"INSERT INTO account_ops_settings(key,value) VALUES('module_enabled','true')",
			"CREATE TABLE token_guard_commit_parent(id INTEGER PRIMARY KEY)",
			"CREATE TABLE token_guard_commit_child(parent_id INTEGER REFERENCES token_guard_commit_parent(id) DEFERRABLE INITIALLY DEFERRED)",
			"CREATE TRIGGER token_guard_commit_fault AFTER UPDATE OF credentials ON accounts BEGIN INSERT INTO token_guard_commit_child(parent_id) VALUES(99999); END",
		} {
			if _, e := tx.ExecContext(ctx, q); e != nil {
				return e
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, version, err := db.TokenGuardConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.CreateTokenGuardJob(ctx, "relogin", id, version); err != nil {
		t.Fatal(err)
	}
	job, err := db.ClaimTokenGuardJob(ctx, "commit-test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := db.TokenGuardAccount(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(db, cache.NewMemory(8), &database.SystemSettings{MaxConcurrency: 2})
	defer store.Stop()
	if err = store.LoadAccountByID(ctx, id); err != nil {
		t.Fatal(err)
	}
	result, err := store.PublishTokenGuardCredentials(ctx, *job, *snapshot, map[string]any{"refresh_token": "new-rt", "access_token": jwt, "id_token": jwt})
	if err == nil || result != nil {
		t.Fatalf("deferred constraint rejected COMMIT but publication reported success: result=%v err=%v", result != nil, err)
	}
	if store.FindByID(id).RefreshToken != "old-rt" {
		t.Fatal("uncommitted credentials reached runtime")
	}
}

func TestTokenGuardOutboxProjectionDropsLegacyCredentialEvidence(t *testing.T) {
	ctx := context.Background()
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "projection.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	jwt := "e30." + base64.RawURLEncoding.EncodeToString([]byte("{\"email\":\"guard@example.com\",\"https://api.openai.com/auth\":{\"chatgpt_account_id\":\"workspace\"}}")) + ".sig"
	id, err := db.InsertAccountWithCredentials(ctx, "guard", map[string]any{"refresh_token": "old-rt", "access_token": jwt, "id_token": jwt}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.WithAccountControlTx(ctx, func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, "INSERT INTO account_ops_settings(key,value) VALUES('module_enabled','true')"); e != nil {
			return e
		}
		_, e := tx.ExecContext(ctx, "INSERT INTO account_codex_capabilities(account_id,upstream,model,capability,credential_generation) VALUES($1,'basispoints','m','supported',0)", id)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	first := NewStore(db, cache.NewMemory(8), &database.SystemSettings{MaxConcurrency: 2})
	defer first.Stop()
	second := NewStore(db, cache.NewMemory(8), &database.SystemSettings{MaxConcurrency: 2})
	defer second.Stop()
	for _, store := range []*Store{first, second} {
		if err = store.LoadAccountByID(ctx, id); err != nil {
			t.Fatal(err)
		}
		if err = store.FindByID(id).ReloadCodexRoutes(ctx); err != nil {
			t.Fatal(err)
		}
	}
	cached := second.FindByID(id)
	now := time.Now()
	cached.SetCodexPathCooldown("basispoints", "m", "transient", now, now.Add(time.Minute))
	if cached.CodexPathSnapshot("basispoints", "m", now).Capability != database.CapabilitySupported {
		t.Fatal("legacy evidence fixture missing")
	}
	_, v, err := db.TokenGuardConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.CreateTokenGuardJob(ctx, "relogin", id, v); err != nil {
		t.Fatal(err)
	}
	j, err := db.ClaimTokenGuardJob(ctx, "first", now)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := db.TokenGuardAccount(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = first.PublishTokenGuardCredentials(ctx, *j, *snapshot, map[string]any{"refresh_token": "new-rt", "access_token": jwt, "id_token": jwt}); err != nil {
		t.Fatal(err)
	}
	if err = second.reloadDispatchAccountByID(ctx, id); err != nil {
		t.Fatal(err)
	}
	view := cached.CodexPathSnapshot("basispoints", "m", now)
	if view.Capability != database.CapabilityUnknown {
		t.Fatal("other instance inherited legacy capability after new credential generation")
	}
	if view.Health != "cooldown" {
		t.Fatal("credential projection erased path cooldown")
	}
	if second.FindByID(id) != cached {
		t.Fatal("credential projection replaced live account")
	}
}
