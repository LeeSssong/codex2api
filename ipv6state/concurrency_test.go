package ipv6state

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/statepool"
)

func sizedToken(issued int64, size int) string {
	raw := make([]byte, size)
	raw[0] = 0x80
	binary.BigEndian.PutUint64(raw[1:9], uint64(issued))
	return base64.URLEncoding.EncodeToString(raw)
}

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not reach synchronization point")
		var zero T
		return zero
	}
}

func TestTwentyConcurrentCandidatesFirstMatchWins(t *testing.T) {
	m, account, now := fixture(t, "member", false)
	m.store.SetMaxConcurrency(20)
	m.config.Concurrency, m.config.AcceptedLengths = 20, []int{292, 332}
	m.config.CaptureMode, m.config.ProxyIDs = "proxy", []int64{1}
	m.route = func(_ context.Context, _ Config, attempt int64) (Route, error) {
		return Route{SessionID: strconv.FormatInt(attempt, 10)}, nil
	}
	started, release := make(chan string, 20), make(chan struct{})
	var calls, cancelled atomic.Int32
	winning := sizedToken(now.Unix()-30, 249)
	m.execute = func(ctx context.Context, _ *auth.Account, _ string, route Route) (*http.Response, error) {
		calls.Add(1)
		started <- route.SessionID
		if route.SessionID == "0" {
			select {
			case <-release:
			case <-ctx.Done():
			}
		} else {
			<-ctx.Done()
			cancelled.Add(1)
		}
		value := tokenAt(now.Unix() - 1)
		if route.SessionID == "0" {
			value = winning
		}
		return &http.Response{StatusCode: 200, Header: http.Header{Header: {value}}, Body: &unreadBody{t: t, ctx: ctx}}, nil
	}
	m.step()
	seen := map[string]bool{}
	for range 20 {
		seen[receive(t, started)] = true
	}
	if len(seen) != 20 || m.Status().ActiveRequests != 20 {
		t.Fatal("20 unique candidates were not concurrent")
	}
	m.step()
	if calls.Load() != 20 {
		t.Fatal("global limit exceeded")
	}
	close(release)
	m.workers.Wait()
	value, _, _ := m.Resolve(account, m.config.Models[0])
	if value != winning || cancelled.Load() != 19 || m.Status().ActiveRequests != 0 {
		t.Fatal("winner or peer cancellation failed")
	}
	entry := m.entries[key(account.ID(), m.config.Models[0])]
	if entry.Attempts != 20 || entry.LastLength != 332 || entry.ExpiresAt != now.Unix()-30+3600 {
		t.Fatal("wrong winner metadata")
	}
	stepAndWait(m)
	if calls.Load() != 20 {
		t.Fatal("ready pair was captured again")
	}
}

func TestConcurrentAccountLimitAndFairModelScheduling(t *testing.T) {
	m, _, _ := fixture(t, "member", false)
	m.config.Concurrency, m.config.Models = 20, statepool.Models
	started := make(chan string, 20)
	m.execute = func(ctx context.Context, _ *auth.Account, model string, _ Route) (*http.Response, error) {
		started <- model
		<-ctx.Done()
		return nil, ctx.Err()
	}
	m.step()
	models := map[string]bool{}
	for range 4 {
		models[receive(t, started)] = true
	}
	if len(models) != 4 || m.Status().ActiveRequests != 4 {
		t.Fatal("account limit or model fairness failed")
	}
	config := m.Status().Config
	config.Enabled = false
	if err := m.Configure(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	m.workers.Wait()
	if m.Status().ActiveRequests != 0 {
		t.Fatal("disable left active workers")
	}
	rows, err := m.db.ListIPv6States(context.Background())
	if err != nil || len(rows) != 0 {
		t.Fatal("cancelled capture was saved")
	}
}

func TestConcurrent429CancelsAllModelsAndHonorsRetry(t *testing.T) {
	m, account, now := fixture(t, "member", false)
	m.config.Concurrency, m.config.Models = 4, statepool.Models
	started, release := make(chan struct{}, 4), make(chan struct{})
	var calls, failures atomic.Int32
	m.execute = func(ctx context.Context, _ *auth.Account, model string, _ Route) (*http.Response, error) {
		calls.Add(1)
		started <- struct{}{}
		if model == statepool.Models[0] {
			select {
			case <-release:
			case <-ctx.Done():
			}
			return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": {"7200"}}, Body: &unreadBody{t: t, ctx: ctx}}, nil
		}
		<-ctx.Done()
		return &http.Response{StatusCode: 200, Header: http.Header{Header: {tokenAt(now.Unix() - 30)}}, Body: &unreadBody{t: t, ctx: ctx}}, nil
	}
	m.failure = func(_ *auth.Account, _ string, _ *http.Response) { failures.Add(1) }
	m.step()
	for range 4 {
		receive(t, started)
	}
	close(release)
	m.workers.Wait()
	for _, entry := range m.Status().Entries {
		if entry.Status != "account_unavailable" || entry.RetryAt != now.Unix()+7200 {
			t.Fatal("account-wide retry deadline missing")
		}
		if value, _, _ := m.Resolve(account, entry.Model); value != "" {
			t.Fatal("late success bypassed 429")
		}
	}
	*now = now.Add(time.Hour)
	stepAndWait(m)
	if calls.Load() != 4 || failures.Load() != 1 {
		t.Fatal("cooldown was bypassed or failure duplicated")
	}
}

func TestImportCancelsOnlyMatchingPairAndPreservesImportedState(t *testing.T) {
	m, account, now := fixture(t, "member", false)
	m.config.Concurrency = 4
	m.config.AcceptedLengths = []int{292, 332}
	m.config.Models = statepool.Models[:2]
	started, release := make(chan string, 4), make(chan struct{})
	m.execute = func(ctx context.Context, _ *auth.Account, model string, _ Route) (*http.Response, error) {
		started <- model
		if model == statepool.Models[0] {
			<-ctx.Done()
		} else {
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		return &http.Response{StatusCode: 200, Header: http.Header{Header: {tokenAt(now.Unix() - 10)}}, Body: &unreadBody{t: t, ctx: ctx}}, nil
	}
	m.step()
	for range 4 {
		receive(t, started)
	}
	identity, _ := statepool.Snapshot(account, "")
	pack := Portable{Format: "codex2api-ipv6-292-v1", MemberHash: identity.MemberHash, WorkspaceHash: identity.WorkspaceHash, Model: statepool.Models[0], Value: sizedToken(now.Unix()-300, 249)}
	if err := m.Import(context.Background(), pack); err != nil {
		t.Fatal(err)
	}
	close(release)
	m.workers.Wait()
	if value, _, _ := m.Resolve(account, pack.Model); value != pack.Value {
		t.Fatal("late result replaced imported state")
	}
	if value, _, _ := m.Resolve(account, statepool.Models[1]); value == "" {
		t.Fatal("import cancelled unrelated model")
	}
	if exported, err := m.Export(account.ID(), pack.Model); err != nil || exported != pack {
		t.Fatal("332 export failed")
	}
	config := m.Status().Config
	config.AcceptedLengths = []int{292}
	if err := m.Configure(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if value, _, _ := m.Resolve(account, pack.Model); value != "" {
		t.Fatal("removed length still reused")
	}
	if _, err := m.Export(account.ID(), pack.Model); err == nil {
		t.Fatal("removed length exported")
	}
	if err := m.Import(context.Background(), pack); err == nil {
		t.Fatal("removed length imported")
	}
}

func TestLengthPolicyValidatesCustomValuesAndSurvivesRestart(t *testing.T) {
	m, account, now := fixture(t, "member", false)
	for _, lengths := range [][]int{nil, {}, {0}, {292, 292}, {8193}} {
		config := DefaultConfig()
		config.AcceptedLengths = lengths
		if config.Validate() == nil {
			t.Fatal("invalid lengths accepted")
		}
	}
	for _, concurrency := range []int{0, 21} {
		config := DefaultConfig()
		config.Concurrency = concurrency
		if config.Validate() == nil {
			t.Fatal("invalid concurrency accepted")
		}
	}
	m.config.AcceptedLengths = []int{292, 332}
	for _, size := range []int{217, 249, 265} {
		if _, _, err := TokenTimes(sizedToken(now.Unix()-100, size), *now); err != nil {
			t.Fatal(err)
		}
	}
	value := sizedToken(now.Unix()-100, 249)
	m.execute = func(ctx context.Context, _ *auth.Account, _ string, _ Route) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{Header: {value}}, Body: &unreadBody{t: t, ctx: ctx}}, nil
	}
	stepAndWait(m)
	config := m.Status().Config
	config.Enabled, config.Concurrency = false, 20
	if err := m.Configure(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	restarted := New(m.db, m.store, nil, nil, nil)
	restarted.now = m.now
	if err := restarted.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer restarted.Stop()
	pack, err := restarted.Export(account.ID(), config.Models[0])
	if err != nil || pack.Value != value || restarted.Status().Config.Concurrency != 20 {
		t.Fatal("custom capture settings/state not restored")
	}
}
