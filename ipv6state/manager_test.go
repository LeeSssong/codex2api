package ipv6state

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/codex2api/statepool"
)

func tokenAt(issued int64) string {
	raw := make([]byte, 217)
	raw[0] = 0x80
	binary.BigEndian.PutUint64(raw[1:9], uint64(issued))
	return base64.URLEncoding.EncodeToString(raw)
}

func TestTokenTimestampDefinesExpiry(t *testing.T) {
	now := time.Unix(20000, 0)
	issued, expires, err := TokenTimes(tokenAt(18000), now)
	if err != nil || issued != 18000 || expires != 21600 {
		t.Fatalf("unexpected times: %d %d %v", issued, expires, err)
	}
	for _, value := range []string{tokenAt(16400), tokenAt(20001), strings.Repeat("a", 292), tokenAt(18000) + "\n", strings.Repeat("a", 312)} {
		if _, _, err := TokenTimes(value, now); err == nil {
			t.Fatal("accepted expired, future, or malformed token")
		}
	}
}

type unreadBody struct {
	t      *testing.T
	ctx    context.Context
	closed bool
}

func (b *unreadBody) Read([]byte) (int, error) {
	b.t.Fatal("probe read the response body")
	return 0, nil
}
func (b *unreadBody) Close() error {
	if b.ctx.Err() == nil {
		b.t.Error("body closed before cancellation")
	}
	b.closed = true
	return nil
}

func fixture(t *testing.T, member string, offset bool) (*Manager, *auth.Account, *time.Time) {
	t.Helper()
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.InitIPv6State(context.Background()); err != nil {
		t.Fatal(err)
	}
	if offset {
		_, err = db.InsertAccount(context.Background(), "offset", "offset", "")
		if err != nil {
			t.Fatal(err)
		}
	}
	claims := `{"sub":"` + member + `","exp":9999999999}`
	credential := "header." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".signature"
	id, err := db.InsertAccountWithCredentials(context.Background(), member, map[string]interface{}{"access_token": credential, "account_id": "workspace", "email": member}, "")
	if err != nil {
		t.Fatal(err)
	}
	store := auth.NewStore(db, nil, &database.SystemSettings{MaxConcurrency: 4})
	t.Cleanup(store.Stop)
	account := &auth.Account{DBID: id, AccessToken: credential, AccountID: "workspace", Email: member, CredentialGeneration: 1,
		PlanType: "plus", Status: auth.StatusReady, ExpiresAt: time.Now().Add(time.Hour)}
	store.AddAccount(account)
	now := time.Unix(20000, 0)
	m := New(db, store, nil, nil, nil)
	m.ctx, m.stop = context.WithCancel(context.Background())
	t.Cleanup(m.Stop)
	m.now = func() time.Time { return now }
	m.config.Enabled, m.config.CaptureMode = true, "local_ipv6"
	m.config.Concurrency = 1
	m.config.Models = []string{statepool.Models[0]}
	m.localIPs = func() ([]string, error) { return []string{"2001:db8::1", "2001:db8::2"}, nil }
	return m, account, &now
}

func stepAndWait(m *Manager) {
	m.step()
	m.workers.Wait()
}

func TestSequentialHeaderCaptureStopsAtFirst292AndRestartsAtExpiry(t *testing.T) {
	m, account, now := fixture(t, "member", false)
	var routes []string
	var bodies []*unreadBody
	m.execute = func(ctx context.Context, _ *auth.Account, model string, route Route) (*http.Response, error) {
		if model != statepool.Models[0] {
			t.Fatal("wrong model")
		}
		routes = append(routes, route.SourceIP)
		value := strings.Repeat("a", 312)
		if len(routes) > 1 {
			value = tokenAt(now.Unix() - 10)
		}
		body := &unreadBody{t: t, ctx: ctx}
		bodies = append(bodies, body)
		return &http.Response{StatusCode: 200, Header: http.Header{Header: {value}}, Body: body}, nil
	}
	stepAndWait(m)
	*now = now.Add(4 * time.Second)
	stepAndWait(m)
	if len(routes) != 2 || routes[0] == routes[1] || !bodies[0].closed || !bodies[1].closed {
		t.Fatalf("wrong route rotation or close behavior: %v", routes)
	}
	state, active, err := m.Resolve(account, statepool.Models[0])
	if err != nil || !active || len(state) != 292 {
		t.Fatal("captured state was not published")
	}
	*now = now.Add(4 * time.Second)
	stepAndWait(m)
	if len(routes) != 2 {
		t.Fatal("kept collecting after success")
	}
	*now = now.Add(time.Hour)
	state, _, _ = m.Resolve(account, statepool.Models[0])
	if state != "" {
		t.Fatal("expired state reused")
	}
	stepAndWait(m)
	if len(routes) != 3 {
		t.Fatal("expired state did not trigger recapture")
	}
}

func TestIdentityModelIPAndMigrationIsolation(t *testing.T) {
	m, account, now := fixture(t, "same-member", false)
	identity, _ := statepool.Snapshot(account, "")
	pack := Portable{Format: "codex2api-ipv6-292-v1", MemberHash: identity.MemberHash, WorkspaceHash: identity.WorkspaceHash, Model: statepool.Models[0], Value: tokenAt(now.Unix() - 30)}
	if err := m.Import(context.Background(), pack); err != nil {
		t.Fatal(err)
	}
	account.ProxyURL = "http://different-egress.invalid:8080"
	if state, active, err := m.Resolve(account, pack.Model); err != nil || !active || state != pack.Value {
		t.Fatal("IP change prevented reuse")
	}
	if state, _, _ := m.Resolve(account, statepool.Models[1]); state != "" {
		t.Fatal("state leaked to another model")
	}
	other, otherAccount, _ := fixture(t, "different-member", false)
	if err := other.Import(context.Background(), pack); err == nil {
		t.Fatal("accepted another member's package")
	}
	if state, _, _ := m.Resolve(otherAccount, pack.Model); state != "" {
		t.Fatal("state leaked through a colliding local ID")
	}
	if err := m.Guard(otherAccount, pack.Model, pack.Value); err == nil {
		t.Fatal("guard accepted another member's token")
	}
	if err := m.Guard(account, statepool.Models[1], pack.Value); err == nil {
		t.Fatal("guard accepted a token outside its model scope")
	}
	if err := m.Guard(account, pack.Model, pack.Value); err != nil {
		t.Fatal("guard rejected the matching identity and model")
	}
	destination, destinationAccount, _ := fixture(t, "same-member", true)
	if err := destination.Import(context.Background(), pack); err != nil {
		t.Fatal(err)
	}
	if destinationAccount.ID() == account.ID() {
		t.Fatal("fixture requires different local account IDs")
	}
	if state, _, _ := destination.Resolve(destinationAccount, pack.Model); state != pack.Value {
		t.Fatal("cross-host identity mapping failed")
	}
	entry := destination.entries[key(destinationAccount.ID(), pack.Model)]
	if entry.ExpiresAt != now.Unix()-30+3600 {
		t.Fatal("migration renewed expiry")
	}
	encoded, _ := json.Marshal(destination.Status())
	if strings.Contains(string(encoded), pack.Value) {
		t.Fatal("status exposed token")
	}
	account.CredentialGeneration++
	if state, _, _ := m.Resolve(account, pack.Model); state != "" {
		t.Fatal("credential replacement retained state")
	}
}

func Test429StopsAccountWithoutReadingBodyAndRespectsRetryAfter(t *testing.T) {
	m, account, now := fixture(t, "member", false)
	m.config.Models = []string{statepool.Models[0], statepool.Models[1]}
	calls := 0
	m.execute = func(ctx context.Context, _ *auth.Account, _ string, _ Route) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": {"7200"}}, Body: &unreadBody{t: t, ctx: ctx}}, nil
	}
	m.failure = func(a *auth.Account, _ string, response *http.Response) {
		if a != account || response.StatusCode != 429 {
			t.Fatal("wrong failure identity")
		}
		m.store.MarkCooldownWithError(a, 2*time.Hour, "rate_limited", "test cooldown")
	}
	stepAndWait(m)
	entry := m.entries[key(account.ID(), statepool.Models[0])]
	// The scheduler sorts models lexically, so use the recorded entry.
	for _, recorded := range m.entries {
		entry = recorded
	}
	if entry.RetryAt != now.Unix()+7200 {
		t.Fatal("Retry-After was truncated")
	}
	*now = now.Add(10 * time.Second)
	stepAndWait(m)
	if calls != 1 {
		t.Fatal("another model bypassed account cooldown")
	}
}

func TestProxyModeDoesNotRequireLocalIPv6(t *testing.T) {
	m, _, now := fixture(t, "member", false)
	m.config.CaptureMode, m.config.ProxyIDs = "proxy", []int64{7, 8}
	m.localIPs = func() ([]string, error) { t.Fatal("proxy mode enumerated local IPv6"); return nil, nil }
	m.route = func(_ context.Context, config Config, attempt int64) (Route, error) {
		return Route{ProxyID: config.ProxyIDs[attempt%2], ProxyURL: "http://gateway.invalid:8080"}, nil
	}
	var ids []int64
	m.execute = func(ctx context.Context, _ *auth.Account, _ string, route Route) (*http.Response, error) {
		ids = append(ids, route.ProxyID)
		return &http.Response{StatusCode: 200, Header: http.Header{Header: {"other-length"}}, Body: &unreadBody{t: t, ctx: ctx}}, nil
	}
	stepAndWait(m)
	*now = now.Add(4 * time.Second)
	stepAndWait(m)
	if len(ids) != 2 || ids[0] != 7 || ids[1] != 8 {
		t.Fatalf("proxy pool did not rotate: %v", ids)
	}
}

func TestDisableCancelsInflightAndDiscardsLateHeaders(t *testing.T) {
	m, account, now := fixture(t, "member", false)
	started := make(chan struct{})
	finished := make(chan struct{})
	m.execute = func(ctx context.Context, _ *auth.Account, _ string, _ Route) (*http.Response, error) {
		close(started)
		<-ctx.Done()
		return &http.Response{StatusCode: 200, Header: http.Header{Header: {tokenAt(now.Unix() - 30)}}, Body: &unreadBody{t: t, ctx: ctx}}, nil
	}
	go func() { stepAndWait(m); close(finished) }()
	<-started
	config := m.config
	config.Enabled = false
	if err := m.Configure(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	<-finished
	if entry := m.entries[key(account.ID(), statepool.Models[0])]; entry.Value != "" {
		t.Fatal("late response was published after disable")
	}
	if _, active, _ := m.Resolve(account, statepool.Models[0]); active {
		t.Fatal("disabled plugin still applies")
	}
	rows, err := m.db.ListIPv6States(context.Background())
	if err != nil || len(rows) != 0 {
		t.Fatal("late response was persisted after disable")
	}
}

func TestSavedStateSurvivesRestartWithoutRenewingExpiry(t *testing.T) {
	m, account, now := fixture(t, "member", false)
	identity, _ := statepool.Snapshot(account, "")
	pack := Portable{Format: "codex2api-ipv6-292-v1", MemberHash: identity.MemberHash, WorkspaceHash: identity.WorkspaceHash, Model: statepool.Models[0], Value: tokenAt(now.Unix() - 1800)}
	if err := m.Import(context.Background(), pack); err != nil {
		t.Fatal(err)
	}
	config := m.config
	config.Enabled = false
	if err := m.Configure(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	restarted := New(m.db, m.store, nil, nil, nil)
	restarted.now = m.now
	if err := restarted.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer restarted.Stop()
	exported, err := restarted.Export(account.ID(), pack.Model)
	if err != nil || exported != pack {
		t.Fatal("restart did not restore the saved token")
	}
	entry := restarted.entries[key(account.ID(), pack.Model)]
	if entry.ExpiresAt != now.Unix()+1800 || restarted.config.Enabled {
		t.Fatal("restart changed expiry or the saved switch")
	}
}
