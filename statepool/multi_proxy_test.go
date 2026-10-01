package statepool

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codex2api/auth"
)

func addProxy(t *testing.T, m *Manager, endpoint, ip string) int64 {
	t.Helper()
	id, err := m.db.InsertProxy(context.Background(), endpoint, "Test proxy")
	if err != nil {
		t.Fatal(err)
	}
	if ip != "" {
		if err := m.db.UpdateProxyTestResult(context.Background(), id, endpoint, "success", ip, "", 1); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func TestStatePoolMultiProxyWinnerReplaysOnBusinessEgress(t *testing.T) {
	m, account := fixture(t, "member", false)
	account.ProxyURL = "http://business.local:8080"
	a := addProxy(t, m, "http://capture-a.local:8080", "192.0.2.1")
	b := addProxy(t, m, "http://capture-b.local:8080", "192.0.2.2")
	options := CaptureOptions{ProxyIDs: []int64{a, b}, Candidates: 3, Strategy: "race", DistinctIPs: true}
	ids, err := m.Capture(context.Background(), []int64{account.ID()}, []string{"sol"}, true, true, options)
	if err != nil || len(ids) != 3 {
		t.Fatalf("capture: %v %v", ids, err)
	}
	if duplicates, err := m.Capture(context.Background(), []int64{account.ID()}, []string{"sol"}, true, true, options); err != nil || len(duplicates) != 0 {
		t.Fatal("active group duplicated")
	}
	var routes []string
	m.execute = func(_ context.Context, a *auth.Account, _ []byte, route string, headers http.Header) (*http.Response, error) {
		routes = append(routes, route)
		if headers.Get(Header) != "" {
			if route != account.ProxyURL || headers.Get(Header) != "winner" {
				t.Fatal("replay used capture route or different value")
			}
			return response(Models[0], correctVerification(), "ignored"), nil
		}
		if route == "http://capture-a.local:8080" {
			return response(Models[0], `{"total":29}`, "bad"), nil
		}
		if route != "http://capture-b.local:8080" {
			t.Fatal("wrong capture route")
		}
		return response(Models[0], correctCandy, "winner"), nil
	}
	runOne(t, m)
	runOne(t, m)
	if len(routes) != 3 {
		t.Fatalf("unexpected generations: %d", len(routes))
	}
	entries := m.Entries()
	if len(entries) != 1 || entries[0].Status != "ready" || entries[0].Checks[0].ProxyID != b {
		t.Fatalf("invalid winner: %+v", entries)
	}
	if value, err := m.Resolve(account, Models[0], "high", account.ProxyURL, ""); err != nil || value != "winner" {
		t.Fatal("winner did not bind to business egress")
	}
	jobs, _ := m.Jobs(context.Background())
	var superseded int
	for _, job := range jobs {
		if job.Status == "superseded" {
			superseded++
		}
	}
	if superseded != 1 {
		t.Fatal("queued candidate was not stopped")
	}
}

func TestStatePoolProxyIPDeduplicationAndChangedRoute(t *testing.T) {
	m, account := fixture(t, "member", false)
	a := addProxy(t, m, "http://a.local:8080", "192.0.2.1")
	b := addProxy(t, m, "http://b.local:8080", "192.0.2.1")
	routes, err := m.captureRoutes(context.Background(), CaptureOptions{ProxyIDs: []int64{a, b}, DistinctIPs: true})
	if err != nil || len(routes) != 1 {
		t.Fatal("known duplicate IP not removed")
	}
	if _, err := m.Capture(context.Background(), []int64{account.ID()}, []string{"sol"}, true, true, CaptureOptions{ProxyIDs: []int64{a}, Candidates: 1}); err != nil {
		t.Fatal(err)
	}
	changed := "http://changed.local:8080"
	if err := m.db.UpdateProxy(context.Background(), a, &changed, nil, nil); err != nil {
		t.Fatal(err)
	}
	m.execute = func(context.Context, *auth.Account, []byte, string, http.Header) (*http.Response, error) {
		t.Fatal("sent credentials after proxy changed")
		return nil, nil
	}
	runOne(t, m)
	jobs, _ := m.Jobs(context.Background())
	if jobs[0].Error != "capture_proxy_changed_or_unavailable" {
		t.Fatalf("unexpected failure: %s", jobs[0].Error)
	}
}

func TestStatePoolRateLimitStopsOtherProxyCandidates(t *testing.T) {
	m, account := fixture(t, "member", false)
	a := addProxy(t, m, "http://a.local:8080", "")
	b := addProxy(t, m, "http://b.local:8080", "")
	if _, err := m.Capture(context.Background(), []int64{account.ID()}, []string{"sol", "terra"}, true, true, CaptureOptions{ProxyIDs: []int64{a, b}, Candidates: 2}); err != nil {
		t.Fatal(err)
	}
	m.execute = func(context.Context, *auth.Account, []byte, string, http.Header) (*http.Response, error) {
		return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{"900"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"rate_limit_exceeded"}}`))}, nil
	}
	runOne(t, m)
	jobs, _ := m.Jobs(context.Background())
	blocked := 0
	for _, job := range jobs {
		if job.Status == "blocked" {
			blocked++
		}
	}
	if blocked != 3 {
		t.Fatalf("%d candidates blocked, want 3", blocked)
	}
	if row, err := m.db.ClaimStatePoolJob(context.Background(), m.owner, time.Now()); err != nil || row != nil {
		t.Fatal("rate limit caused proxy switching")
	}
}

func TestStatePoolWinnerCancelsInFlightSibling(t *testing.T) {
	m, account := fixture(t, "member", false)
	if _, err := m.Capture(context.Background(), []int64{account.ID()}, []string{"sol"}, true, true, CaptureOptions{Candidates: 2}); err != nil {
		t.Fatal(err)
	}
	first, _ := m.db.ClaimStatePoolJob(context.Background(), m.owner, time.Now())
	second, _ := m.db.ClaimStatePoolJob(context.Background(), m.owner, time.Now())
	if first == nil || second == nil {
		t.Fatal("parallel candidates not claimed")
	}
	started := make(chan struct{}, 2)
	gate := make(chan struct{})
	var mu sync.Mutex
	captures := 0
	m.execute = func(ctx context.Context, _ *auth.Account, _ []byte, _ string, headers http.Header) (*http.Response, error) {
		if headers.Get(Header) != "" {
			return response(Models[0], correctVerification(), "ignored"), nil
		}
		mu.Lock()
		captures++
		ordinal := captures
		mu.Unlock()
		started <- struct{}{}
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if ordinal == 2 {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return response(Models[0], correctCandy, "winner"), nil
	}
	done := make(chan struct{}, 2)
	go func() { m.run(*first); done <- struct{}{} }()
	go func() { m.run(*second); done <- struct{}{} }()
	for range 2 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("candidate failed to start")
		}
	}
	close(gate)
	for range 2 {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("sibling was not cancelled")
		}
	}
	if len(m.Entries()) != 1 {
		t.Fatal("winner not published")
	}
	jobs, _ := m.Jobs(context.Background())
	for _, job := range jobs {
		if job.Status != "completed" && job.Status != "cancelled" {
			t.Fatalf("unexpected job state: %s", job.Status)
		}
	}
}

func TestStatePoolImportPreviewSkipsExpiredAndReusesVerified(t *testing.T) {
	m, account := fixture(t, "member", false)
	calls := 0
	m.execute = func(_ context.Context, _ *auth.Account, _ []byte, _ string, headers http.Header) (*http.Response, error) {
		calls++
		answer := correctCandy
		if headers.Get(Header) != "" {
			answer = correctVerification()
		}
		return response(Models[0], answer, "state"), nil
	}
	if _, err := m.Capture(context.Background(), []int64{account.ID()}, []string{"sol"}, true, true); err != nil {
		t.Fatal(err)
	}
	runOne(t, m)
	pack, err := m.Export([]string{m.Entries()[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	expired := pack.States[0]
	expired.ExpiresAt = time.Now().Unix() - 1
	pack.States = append(pack.States, expired)
	preview, err := m.PreviewImport(pack)
	if err != nil || preview[0].Status != "already_verified" || preview[1].Status != "invalid" {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	ids, err := m.Import(context.Background(), pack, true, true, true)
	if err != nil || len(ids) != 0 || calls != 2 {
		t.Fatal("valid local import made unnecessary upstream calls")
	}
}

func TestStatePoolForwardProxyOnlyAppliesToCapture(t *testing.T) {
	m, account := fixture(t, "member", false)
	account.ProxyURL = "http://business.local:8080"
	forward := addProxy(t, m, "http://forward.local:9000", "")
	capture := addProxy(t, m, "http://capture.local:8082", "")
	options := CaptureOptions{ProxyIDs: []int64{capture}, ForwardProxyID: forward, Candidates: 1}
	if _, err := m.Capture(context.Background(), []int64{account.ID()}, []string{"sol"}, true, true, options); err != nil {
		t.Fatal(err)
	}
	m.execute = func(ctx context.Context, _ *auth.Account, _ []byte, route string, headers http.Header) (*http.Response, error) {
		if headers.Get(Header) == "" {
			if route != "http://capture.local:8082" || CaptureForwardProxy(ctx) != "http://forward.local:9000" {
				t.Fatal("capture chain missing")
			}
			return response(Models[0], correctCandy, "state"), nil
		}
		if route != account.ProxyURL || CaptureForwardProxy(ctx) != "" {
			t.Fatal("capture chain contaminated business replay")
		}
		return response(Models[0], correctVerification(), "ignored"), nil
	}
	runOne(t, m)
	entry := m.Entries()[0]
	if entry.Checks[0].ForwardProxyName == "" || entry.Checks[1].ForwardProxyName != "" {
		t.Fatal("chain evidence missing")
	}
	pack, err := m.Export([]string{entry.ID})
	if err != nil || len(pack.States[0].Checks) != 0 {
		t.Fatal("portable export included machine-local network evidence")
	}
	options.ForwardProxyID = capture
	if _, err := m.Capture(context.Background(), []int64{account.ID()}, []string{"sol"}, true, true, options); err == nil {
		t.Fatal("proxy loop allowed")
	}
}
