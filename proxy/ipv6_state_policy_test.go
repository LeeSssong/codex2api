package proxy

import (
	"context"
	"github.com/codex2api/auth"
	"github.com/codex2api/ipv6state"
	"testing"
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
