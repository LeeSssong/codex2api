package proxy

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/codex2api/ipv6state"
)

func TestRequiredStateFilterAndFinalDispatchGuard(t *testing.T) {
	SetIPv6StateProvider(&IPv6StateProvider{
		EligibleAccounts: func(string) (bool, map[int64]bool) { return true, map[int64]bool{2: true} },
		Resolve:          func(*auth.Account, string) (string, bool, error) { return "", true, ipv6state.ErrStateRequired },
	})
	t.Cleanup(func() { SetIPv6StateProvider(nil) })
	filter := withRequiredStateFilter("gpt-5.6-sol", func(a *auth.Account) bool { return a.ID() != 3 })
	if filter(&auth.Account{DBID: 1}) || !filter(&auth.Account{DBID: 2}) || filter(&auth.Account{DBID: 3}) {
		t.Fatal("scheduler admitted an account without state")
	}
	if !filter(&auth.Account{DBID: 4, UpstreamType: auth.UpstreamClaude, AccessToken: "fake"}) {
		t.Fatal("Codex policy affected another channel")
	}
	body := []byte(`{"model":"gpt-5.6-sol"}`)
	_, _, active, err := applyIPv6State(context.Background(), &auth.Account{DBID: 2}, body, nil)
	if !active || err == nil {
		t.Fatal("dispatch guard accepted a state that disappeared after scheduling")
	}
	apiErr, ok := err.(*Error)
	if !ok || apiErr.HTTPStatus != 503 || apiErr.Code != "valid_state_required" {
		t.Fatal("wrong strict-state error")
	}
	if _, _, active, err := applyIPv6State(WithoutStatePool(context.Background()), &auth.Account{DBID: 2}, body, nil); active || err != nil {
		t.Fatal("capture could not bypass business policy")
	}
}

func TestUnselectedCaptureModelsKeepNormalDispatch(t *testing.T) {
	ctx := context.Background()
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if errClose := db.Close(); errClose != nil {
			t.Error(errClose)
		}
	})
	if err := db.InitIPv6State(ctx); err != nil {
		t.Fatal(err)
	}
	store := auth.NewStore(db, nil, &database.SystemSettings{MaxConcurrency: 4})
	t.Cleanup(store.Stop)
	manager := ipv6state.New(db, store, nil, nil, nil)
	config := ipv6state.DefaultConfig()
	config.Enabled = true
	config.Models = []string{"gpt-5.6-sol", "gpt-6-astra"}
	if err := manager.Configure(ctx, config); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetRequireValidState(ctx, true); err != nil {
		t.Fatal(err)
	}
	SetIPv6StateProvider(&IPv6StateProvider{
		EligibleAccounts: manager.EligibleAccounts,
		Resolve:          manager.Resolve,
		Applies:          manager.Applies,
		Guard:            manager.Guard,
	})
	t.Cleanup(func() { SetIPv6StateProvider(nil) })
	account := &auth.Account{DBID: 1, AccessToken: "test-token"}
	for _, model := range []string{"gpt-5.6-terra", "gpt-5.6-luna", "codex-auto-review"} {
		filter := withRequiredStateFilter(model, func(a *auth.Account) bool { return a.ID() == account.ID() })
		if !filter(account) || filter(&auth.Account{DBID: 2}) {
			t.Fatalf("state policy changed normal account eligibility for %s", model)
		}
		body := []byte(`{"model":"` + model + `","client_metadata":{"x-codex-turn-state":"native-state"}}`)
		headers := http.Header{codexTurnStateHeader: {"native-state"}}
		updated, outgoing, err := applyVerifiedState(ctx, account, body, headers, "")
		if err != nil || string(updated) != string(body) || outgoing.Get(codexTurnStateHeader) != "native-state" {
			t.Fatalf("unselected model %s lost normal continuation: %v", model, err)
		}
		if ipv6StateDirect(ctx, account, body) {
			t.Fatalf("unselected model %s inherited the capture route", model)
		}
	}
	for _, model := range config.Models {
		if withRequiredStateFilter(model, nil)(account) {
			t.Fatalf("selected model %s admitted an account without state", model)
		}
	}
}
