package proxy

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/cache"
	"github.com/codex2api/database"
	"github.com/codex2api/internal/turnstate"
	"github.com/tidwall/gjson"
)

func proxyTurnStateTicket(issued time.Time) string {
	raw := make([]byte, 217)
	raw[0] = 0x80
	binary.BigEndian.PutUint64(raw[1:9], uint64(issued.Unix()))
	return base64.URLEncoding.EncodeToString(raw)
}

func newTurnStateReuseRuntimeFixture(t *testing.T) (*turnStateReuseRuntime, *auth.Account, turnstate.Ticket) {
	t.Helper()
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "turn-state-runtime.db"))
	if err != nil {
		t.Fatalf("database.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	groupID, err := db.CreateAccountGroup(ctx, "astra", "", "", 0, 0, sql.NullInt64{})
	if err != nil {
		t.Fatalf("CreateAccountGroup: %v", err)
	}
	enabled := true
	if err := db.UpdateAccountGroup(ctx, groupID, nil, nil, nil, &database.UpdateAccountGroupOpts{TurnStateInjectEnabled: &enabled}); err != nil {
		t.Fatalf("UpdateAccountGroup: %v", err)
	}
	if err := db.UpdateTurnStateReuseSettings(ctx, database.TurnStateReuseSettings{
		Enabled:             true,
		HarvestUseProxyPool: true,
		MissAction:          database.TurnStateMissNone,
		RecoveredAction:     database.TurnStateRecoveredNone,
	}); err != nil {
		t.Fatalf("UpdateTurnStateReuseSettings: %v", err)
	}

	tokenCache := cache.NewMemory(1)
	t.Cleanup(func() { _ = tokenCache.Close() })
	runtime := newTurnStateReuseRuntime(db, tokenCache)
	account := &auth.Account{DBID: 41, AccessToken: "access-token", AccountID: "workspace-1", GroupIDs: []int64{groupID}}
	now := time.Now().Truncate(time.Second)
	ticket, err := turnstate.Parse(proxyTurnStateTicket(now.Add(-time.Minute)), now)
	if err != nil {
		t.Fatalf("turnstate.Parse: %v", err)
	}
	key := turnstate.Key{AccountID: account.ID(), Model: turnstate.HarvestModel, CredentialHash: turnstate.CredentialHash(account.AccessToken, account.AccountID)}
	if replaced, err := runtime.store.Put(ctx, key, ticket); err != nil || !replaced {
		t.Fatalf("store.Put replaced=%t err=%v", replaced, err)
	}
	return runtime, account, ticket
}

func TestTurnStateReuseRuntimeAppliesOnlyInScopeAstraResponses(t *testing.T) {
	runtime, account, ticket := newTurnStateReuseRuntimeFixture(t)
	ctx := context.Background()

	for _, tc := range []struct {
		name       string
		account    *auth.Account
		body       []byte
		wantTicket bool
	}{
		{name: "astra", account: account, body: []byte(`{"model":"gpt-6-astra","input":"hello"}`), wantTicket: true},
		{name: "sol", account: account, body: []byte(`{"model":"gpt-5.6-sol","input":"hello"}`)},
		{name: "compact", account: account, body: []byte(`{"model":"gpt-6-astra","input":[{"type":"compaction_trigger"}]}`)},
		{name: "out of scope", account: &auth.Account{DBID: account.DBID, AccessToken: account.AccessToken, AccountID: account.AccountID}, body: []byte(`{"model":"gpt-6-astra","input":"hello"}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := http.Header{turnstate.HeaderName: []string{"client-state"}}
			applied := runtime.applyHTTP(ctx, headers, tc.account, tc.body, "/responses")
			if applied != tc.wantTicket {
				t.Fatalf("applied = %t, want %t", applied, tc.wantTicket)
			}
			want := "client-state"
			if tc.wantTicket {
				want = ticket.Raw
			}
			if got := headers.Get(turnstate.HeaderName); got != want {
				t.Fatalf("turn-state header = %q, want %q", got, want)
			}
		})
	}
}

func TestTurnStateReuseRuntimeAppliesWebsocketMetadata(t *testing.T) {
	runtime, account, ticket := newTurnStateReuseRuntimeFixture(t)
	body := []byte(`{"type":"response.create","model":"gpt-6-astra","client_metadata":{"x-codex-turn-state":"client-state"},"input":"hello"}`)

	got, applied := runtime.applyWebsocket(context.Background(), account, body)
	if !applied {
		t.Fatal("applyWebsocket applied = false, want true")
	}
	if state := gjson.GetBytes(got, "client_metadata.x-codex-turn-state").String(); state != ticket.Raw {
		t.Fatalf("websocket turn-state = %q, want %q", state, ticket.Raw)
	}
}

func TestTurnStateReuseRuntimeSchedulerFilterHonorsMissAction(t *testing.T) {
	runtime, account, _ := newTurnStateReuseRuntimeFixture(t)
	ctx := context.Background()
	body := []byte(`{"model":"gpt-6-astra","input":"hello"}`)

	key := turnstate.Key{AccountID: account.ID(), Model: turnstate.HarvestModel, CredentialHash: turnstate.CredentialHash(account.AccessToken, account.AccountID)}
	current, found, err := runtime.store.Get(ctx, key, time.Now())
	if err != nil || !found {
		t.Fatalf("store.Get found=%t err=%v", found, err)
	}
	if deleted, err := runtime.store.DeleteIfMatch(ctx, key, current.Raw); err != nil || !deleted {
		t.Fatalf("store.DeleteIfMatch deleted=%t err=%v", deleted, err)
	}

	filter := runtime.schedulerFilter(ctx, body, "/responses", nil)
	if !filter(account) {
		t.Fatal("miss_action=none rejected an in-scope account without a ticket")
	}

	settings, err := runtime.db.GetTurnStateReuseSettings(ctx)
	if err != nil {
		t.Fatalf("GetTurnStateReuseSettings: %v", err)
	}
	settings.MissAction = database.TurnStateMissUnschedulable
	if err := runtime.db.UpdateTurnStateReuseSettings(ctx, settings); err != nil {
		t.Fatalf("UpdateTurnStateReuseSettings: %v", err)
	}
	runtime.invalidate()
	filter = runtime.schedulerFilter(ctx, body, "/responses", nil)
	if filter(account) {
		t.Fatal("non-none miss action accepted an in-scope account without a ticket")
	}

	outOfScope := &auth.Account{DBID: 42, AccessToken: "other-token", AccountID: "workspace-2"}
	if !filter(outOfScope) {
		t.Fatal("out-of-scope account was rejected by turn-state miss policy")
	}
}

func TestTurnStateReuseStatusDoesNotExposeTicket(t *testing.T) {
	runtime, account, ticket := newTurnStateReuseRuntimeFixture(t)
	status := runtime.accountStatus(context.Background(), account, time.Now())
	if status.Status != "fresh" || status.EncodedLength != len(ticket.Raw) || status.DecodedLength != ticket.DecodedLength {
		t.Fatalf("status = %+v", status)
	}
	if status.AccountID != account.ID() || status.IssuedAt == "" || status.ExpiresAt == "" || status.RemainingSeconds <= 0 {
		t.Fatalf("incomplete status = %+v", status)
	}
}
