package tokenguard

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"github.com/codex2api/auth"
	"github.com/codex2api/cache"
	"github.com/codex2api/database"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type blockedClient struct {
	started     chan struct{}
	release     chan struct{}
	calls       atomic.Int32
	credentials map[string]any
}

func (c *blockedClient) Probe(context.Context, Config, string) ProbeResult {
	return ProbeResult{"auth", "探活报告令牌失效", 1}
}
func (c *blockedClient) Relogin(context.Context, Config, ReloginAccount) (map[string]any, error) {
	c.calls.Add(1)
	close(c.started)
	<-c.release
	return c.credentials, nil
}
func (c *blockedClient) Notify(context.Context, Config, string, string, bool) {}
func serviceFixture(t *testing.T) (*database.DB, *auth.Store, int64, Config) {
	t.Helper()
	ctx := context.Background()
	db, e := database.New("sqlite", filepath.Join(t.TempDir(), "service.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	if e = db.WithAccountControlTx(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, "INSERT INTO account_ops_settings(key,value) VALUES('module_enabled','true')")
		return e
	}); e != nil {
		t.Fatal(e)
	}
	token := "e30." + base64.RawURLEncoding.EncodeToString([]byte("{\"email\":\"guard@example.com\",\"https://api.openai.com/auth\":{\"chatgpt_account_id\":\"workspace\"}}")) + ".sig"
	id, e := db.InsertAccountWithCredentials(ctx, "admin-display-name", map[string]any{"refresh_token": "old-rt", "access_token": token, "id_token": token, "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)}, "")
	if e != nil {
		t.Fatal(e)
	}
	store := auth.NewStore(db, cache.NewMemory(16), &database.SystemSettings{MaxConcurrency: 2})
	t.Cleanup(store.Stop)
	if e = store.LoadAccountByID(ctx, id); e != nil {
		t.Fatal(e)
	}
	cfg := DefaultConfig()
	cfg.ProbeEndpoint = "https://probe.invalid/test"
	cfg.ReloginEndpoint = "https://login.invalid/test"
	cfg.ReloginAccounts = []ReloginAccount{{AccountID: id, Email: "guard@example.com", Password: "SECRET-PASSWORD", MFASecret: "SECRET-MFA"}}
	return db, store, id, cfg
}
func waitGuardState(t *testing.T, db *database.DB, id, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		j, e := db.TokenGuardJob(context.Background(), id)
		if e == nil && j.State == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	j, _ := db.TokenGuardJob(context.Background(), id)
	t.Fatalf("want %s, got %+v", want, j)
}
func TestLongReloginDoesNotHoldNativeRefreshLeaseAndCancellationFencesPublish(t *testing.T) {
	db, store, id, cfg := serviceFixture(t)
	ctx := context.Background()
	before, _ := db.TokenGuardAccount(ctx, id)
	client := &blockedClient{started: make(chan struct{}), release: make(chan struct{}), credentials: map[string]any{"refresh_token": "new-rt", "access_token": before.Credential("access_token"), "id_token": before.Credential("id_token")}}
	s := NewService(db, store, client)
	if _, e := s.SaveConfig(ctx, cfg); e != nil {
		t.Fatal(e)
	}
	s.Start(ctx)
	defer s.Stop()
	j, e := s.Run(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	select {
	case <-client.started:
	case <-time.After(5 * time.Second):
		t.Fatal("external login not started")
	}
	lockCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if e = store.WithOAuthRefreshLease(lockCtx, "old-rt", func(context.Context) error { return nil }); e != nil {
		close(client.release)
		t.Fatal("long external task held native RT lease", e)
	}
	if ok, e := s.Cancel(ctx, j.ID); e != nil || !ok {
		close(client.release)
		t.Fatal("cancel failed", e)
	}
	close(client.release)
	s.Stop()
	waitGuardState(t, db, j.ID, "cancelled")
	after, _ := db.TokenGuardAccount(ctx, id)
	if after.Generation != before.Generation || after.Credential("refresh_token") != "old-rt" {
		t.Fatal("late external response published after cancel")
	}
	if client.calls.Load() != 1 {
		t.Fatal("external login replayed")
	}
}
func TestExplicitMappingRejectsWrongIdentityAndRelay(t *testing.T) {
	db, store, id, cfg := serviceFixture(t)
	s := NewService(db, store, &blockedClient{})
	cfg.ReloginAccounts[0].Email = "other@example.com"
	if _, e := s.SaveConfig(context.Background(), cfg); e == nil {
		t.Fatal("wrong identity accepted")
	}
	if e := db.UpdateCredentials(context.Background(), id, map[string]any{"auth_mode": "agentIdentity"}); e != nil {
		t.Fatal(e)
	}
	cfg.ReloginAccounts[0].Email = "guard@example.com"
	if _, e := s.SaveConfig(context.Background(), cfg); e == nil {
		t.Fatal("agent identity accepted as OAuth")
	}
}
func TestGuardPublicationLeavesManualDisabledAndAuditSecretsOut(t *testing.T) {
	db, store, id, cfg := serviceFixture(t)
	ctx := context.Background()
	runtimeAccount := store.FindByID(id)
	atomic.StoreInt64(&runtimeAccount.ActiveRequests, 3)
	if e := db.SetAccountEnabled(ctx, id, false); e != nil {
		t.Fatal(e)
	}
	before, _ := db.TokenGuardAccount(ctx, id)
	client := &blockedClient{started: make(chan struct{}), release: make(chan struct{}), credentials: map[string]any{"refresh_token": "NEW-SECRET-RT", "access_token": before.Credential("access_token"), "id_token": before.Credential("id_token")}}
	close(client.release)
	s := NewService(db, store, client)
	if _, e := s.SaveConfig(ctx, cfg); e != nil {
		t.Fatal(e)
	}
	s.Start(ctx)
	defer s.Stop()
	j, e := s.Run(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	waitGuardState(t, db, j.ID, "completed")
	after, _ := db.TokenGuardAccount(ctx, id)
	if store.FindByID(id) != runtimeAccount || runtimeAccount.GetActiveRequests() != 3 {
		t.Fatal("publication replaced live account or reset active requests")
	}
	if after.Enabled || after.Generation != before.Generation+1 {
		t.Fatal("credential publication re-enabled manually disabled account")
	}
	status, e := s.Status(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if status.Accounts[0].Schedulable {
		t.Fatal("UI mislabeled repaired credentials as restored scheduling")
	}
	events, _, e := s.Events(ctx, 0, 100)
	if e != nil {
		t.Fatal(e)
	}
	raw := fmt.Sprint(events)
	for _, secret := range []string{"SECRET-PASSWORD", "SECRET-MFA", "NEW-SECRET-RT", "guard@example.com"} {
		if strings.Contains(raw, secret) {
			t.Fatal("event exposed credential")
		}
	}
}

func TestProbeAuditIsDurableBeforeLongReloginCompletes(t *testing.T) {
	db, store, id, cfg := serviceFixture(t)
	ctx := context.Background()
	before, _ := db.TokenGuardAccount(ctx, id)
	client := &blockedClient{started: make(chan struct{}), release: make(chan struct{}), credentials: map[string]any{"refresh_token": "new-rt", "access_token": before.Credential("access_token"), "id_token": before.Credential("id_token")}}
	cfg.Enabled = true
	cfg.AutoRelogin = true
	s := NewService(db, store, client)
	if _, e := s.SaveConfig(ctx, cfg); e != nil {
		t.Fatal(e)
	}
	_, v, _ := s.Config(ctx)
	if _, e := db.CreateTokenGuardJob(ctx, "cycle", 0, v); e != nil {
		t.Fatal(e)
	}
	j, e := db.ClaimTokenGuardJob(ctx, "audit-test", time.Now())
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan struct{})
	go func() { s.process(ctx, *j, cfg, *before, storedState{}); close(done) }()
	defer func() { close(client.release); <-done }()
	select {
	case <-client.started:
	case <-time.After(5 * time.Second):
		t.Fatal("external login not started")
	}
	events, _, e := s.Events(ctx, 0, 10)
	if e != nil {
		t.Fatal(e)
	}
	if len(events) != 1 || events[0].Kind != "probe_auth" {
		t.Fatal("probe evidence was not committed before long external authentication")
	}
}

func TestManualReloginFailureHasFailedJobState(t *testing.T) {
	db, store, id, cfg := serviceFixture(t)
	ctx := context.Background()
	client := &blockedClient{started: make(chan struct{}), release: make(chan struct{}), credentials: map[string]any{}}
	close(client.release)
	s := NewService(db, store, client)
	if _, e := s.SaveConfig(ctx, cfg); e != nil {
		t.Fatal(e)
	}
	_, v, _ := s.Config(ctx)
	pending, e := db.CreateTokenGuardJob(ctx, "relogin", id, v)
	if e != nil {
		t.Fatal(e)
	}
	j, e := db.ClaimTokenGuardJob(ctx, "failure-test", time.Now())
	if e != nil {
		t.Fatal(e)
	}
	s.execute(ctx, *j)
	waitGuardState(t, db, pending.ID, "failed")
}
