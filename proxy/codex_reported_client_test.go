package proxy

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex2api/database"
)

type fakeReportedUsage struct {
	rows []*database.AccountDailyUsage
	err  error
}

func (f fakeReportedUsage) ListAccountDailyUsage(context.Context, int64, int) ([]*database.AccountDailyUsage, error) {
	return f.rows, f.err
}

type flakyReportedUsage struct {
	rows  []*database.AccountDailyUsage
	fails int
}

func (f *flakyReportedUsage) ListAccountDailyUsage(context.Context, int64, int) ([]*database.AccountDailyUsage, error) {
	if f.fails > 0 {
		f.fails--
		return nil, errors.New("db down")
	}
	return f.rows, nil
}

func TestReportedClientFollowsNewestOfficialClient(t *testing.T) {
	SetCodexReportedClientSource(fakeReportedUsage{rows: []*database.AccountDailyUsage{
		{Day: "2026-10-26", ClientsJSON: `[{"client_id":"CODEX_DESKTOP_APP","users":1,"threads":8,"turns":377}]`},
		{Day: "2026-10-27", ClientsJSON: `[{"client_id":"CODEX_CLI","users":0,"threads":0,"turns":0,"credits":995}]`},
	}})
	t.Cleanup(func() { SetCodexReportedClientSource(nil) })

	kind, ok := codexReportedClientKind(78)
	if !ok || kind != CodexClientKindDesktop {
		t.Fatalf("kind = %q ok=%v, want desktop from the day that had a real user", kind, ok)
	}
}

func TestReportedClientUsesTokenWhenUsersAreZero(t *testing.T) {
	tokens := int64(59_272_301)
	SetCodexReportedClientSource(fakeReportedUsage{rows: []*database.AccountDailyUsage{
		{Day: "2026-10-27", ClientsJSON: `[{"client_id":"CODEX_CLI","users":0,"threads":0,"turns":0,"text_total_tokens":59272301}]`},
	}})
	t.Cleanup(func() { SetCodexReportedClientSource(nil) })

	kind, ok := codexReportedClientKind(79)
	if !ok || kind != CodexClientKindTUI {
		t.Fatalf("kind = %q ok=%v, want cli from %d tokens", kind, ok, tokens)
	}
}

func TestReportedClientIgnoresUnknownGatewayTraffic(t *testing.T) {
	SetCodexReportedClientSource(fakeReportedUsage{rows: []*database.AccountDailyUsage{
		{Day: "2026-10-27", ClientsJSON: `[{"client_id":"CODEX_UNKNOWN_DEFAULT","users":4,"threads":20,"turns":80}]`},
	}})
	t.Cleanup(func() { SetCodexReportedClientSource(nil) })

	if kind, ok := codexReportedClientKind(81); ok || kind != "" {
		t.Fatalf("unknown gateway traffic selected %q", kind)
	}
}

func TestPoolPersonaUsesReportedClientInsteadOfMix(t *testing.T) {
	SetCodexReportedClientSource(fakeReportedUsage{rows: []*database.AccountDailyUsage{
		{Day: "2026-10-26", ClientsJSON: `[{"client_id":"CODEX_DESKTOP_APP","users":1,"threads":8,"turns":30}]`},
	}})
	t.Cleanup(func() { SetCodexReportedClientSource(nil) })

	normalized, err := NormalizeCodexUserAgentConfigJSON(`{"mode":"pool","pool_mix":{"codex-tui":100}}`)
	if err != nil {
		t.Fatal(err)
	}
	ua, _, ok, err := codexUserAgentFromConfigChecked(normalized, 90, "")
	if err != nil || !ok {
		t.Fatalf("persona missing: %v", err)
	}
	if !strings.HasPrefix(ua, "Codex Desktop/") {
		t.Fatalf("reported desktop was replaced by the pool mix: %s", ua)
	}
	again, _, _, err := codexUserAgentFromConfigChecked(normalized, 90, "")
	if err != nil || again != ua {
		t.Fatalf("reported persona drifted: %s vs %s", ua, again)
	}
}

func TestPoolPersonaUsesEveryReportedClientKind(t *testing.T) {
	t.Cleanup(func() { SetCodexReportedClientSource(nil) })
	cases := []struct {
		id     int64
		client string
		prefix string
	}{
		{93, "CODEX_CLI", "codex-tui/"},
		{94, "CODEX_IDE_VSCODE", "codex_vscode/"},
		{95, "CODEX_SERVICE_EXEC", "codex_exec/"},
		{96, "CODEX_WORK_DESKTOP", "Codex Desktop/"},
	}
	for _, tc := range cases {
		SetCodexReportedClientSource(fakeReportedUsage{rows: []*database.AccountDailyUsage{
			{Day: "2026-10-27", ClientsJSON: `[{"client_id":"` + tc.client + `","users":1,"threads":1,"turns":1}]`},
		}})
		normalized, err := NormalizeCodexUserAgentConfigJSON(`{"mode":"pool","pool_mix":{"codex-tui":100}}`)
		if err != nil {
			t.Fatal(err)
		}
		ua, _, ok, err := codexUserAgentFromConfigChecked(normalized, tc.id, "")
		if err != nil || !ok || !strings.HasPrefix(ua, tc.prefix) {
			t.Fatalf("%s ua = %q ok=%v, want prefix %s", tc.client, ua, ok, tc.prefix)
		}
	}
}

func TestPoolPersonaKeepsMixWhenUsageIsMissing(t *testing.T) {
	SetCodexReportedClientSource(fakeReportedUsage{})
	t.Cleanup(func() { SetCodexReportedClientSource(nil) })

	normalized, err := NormalizeCodexUserAgentConfigJSON(`{"mode":"pool","pool_mix":{"codex-tui":100}}`)
	if err != nil {
		t.Fatal(err)
	}
	ua, _, ok, err := codexUserAgentFromConfigChecked(normalized, 91, "")
	if err != nil || !ok {
		t.Fatalf("persona missing: %v", err)
	}
	if !strings.HasPrefix(ua, "codex-tui/") && !strings.HasPrefix(ua, "codex_cli_rs/") {
		t.Fatalf("missing usage changed the pool persona: %s", ua)
	}
}

func TestReportedClientRetriesAfterLookupFailure(t *testing.T) {
	source := &flakyReportedUsage{
		fails: 1,
		rows: []*database.AccountDailyUsage{
			{Day: "2026-10-27", ClientsJSON: `[{"client_id":"CODEX_IDE_VSCODE","users":1,"threads":1,"turns":2}]`},
		},
	}
	SetCodexReportedClientSource(source)
	t.Cleanup(func() { SetCodexReportedClientSource(nil) })

	if kind, ok := codexReportedClientKind(92); ok || kind != "" {
		t.Fatalf("lookup failure selected %q", kind)
	}
	kind, ok := codexReportedClientKind(92)
	if !ok || kind != CodexClientKindVSCode {
		t.Fatalf("kind = %q ok=%v, want vscode after the failed lookup was not cached", kind, ok)
	}
}

func TestSameDayPicksTheClientThatWasActuallyUsed(t *testing.T) {
	raw := `[{"client_id":"CODEX_CLI","users":0,"threads":0,"turns":0,"text_total_tokens":900},{"client_id":"CODEX_DESKTOP_APP","users":1,"threads":2,"turns":4,"text_total_tokens":10}]`
	kind, score, used := dominantReportedCodexClient(raw)
	if !used || kind != CodexClientKindDesktop || score <= 0 {
		t.Fatalf("kind=%s score=%d used=%v, want the desktop row that had a user", kind, score, used)
	}
}

func TestReportedClientHistorySelection(t *testing.T) {
	cases := []struct {
		name string
		rows []*database.AccountDailyUsage
		want CodexClientKind
	}{
		{"unsorted newest engaged", []*database.AccountDailyUsage{
			{Day: "2026-10-28", ClientsJSON: `[{"client_id":"CODEX_IDE_VSCODE","turns":1}]`},
			{Day: "2026-10-26", ClientsJSON: `[{"client_id":"CODEX_DESKTOP_APP","users":10}]`},
		}, CodexClientKindVSCode},
		{"token shell cannot replace prior engagement", []*database.AccountDailyUsage{
			{Day: "2026-10-29", ClientsJSON: `[{"client_id":"CODEX_CLI","text_total_tokens":9999999999}]`},
			{Day: "2026-10-28", ClientsJSON: `[{"client_id":"CODEX_DESKTOP_APP","threads":1}]`},
		}, CodexClientKindDesktop},
		{"newest token-only fallback", []*database.AccountDailyUsage{
			{Day: "2026-10-28", ClientsJSON: `[{"client_id":"CODEX_CLI","text_total_tokens":999999}]`},
			{Day: "2026-10-29", ClientsJSON: `[{"client_id":" codex_service_exec ","text_total_tokens":1}]`},
		}, CodexClientKindExec},
		{"empty malformed and unknown rows", []*database.AccountDailyUsage{
			nil, {Day: "2026-10-29", ClientsJSON: `not-json`},
			{Day: "2026-10-29", ClientsJSON: `[{"client_id":"CODEX_UNKNOWN_DEFAULT","users":100}]`},
			{Day: "2026-10-29", ClientsJSON: `[{"client_id":"CODEX_CLI"}]`},
		}, ""},
		{"same-day engaged beats huge token shell", []*database.AccountDailyUsage{
			{Day: "2026-10-29", ClientsJSON: `[{"client_id":"CODEX_CLI","text_total_tokens":9999999999},{"client_id":"CODEX_WORK_DESKTOP","turns":1}]`},
		}, CodexClientKindDesktop},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := newestReportedCodexClient(tc.rows); got != tc.want {
				t.Fatalf("kind = %q, want %q", got, tc.want)
			}
		})
	}
}

type countedReportedUsage struct {
	calls atomic.Int32
	rows  []*database.AccountDailyUsage
}

func (s *countedReportedUsage) ListAccountDailyUsage(_ context.Context, accountID int64, days int) ([]*database.AccountDailyUsage, error) {
	if accountID != 105 || days != 90 {
		return nil, errors.New("unexpected account or lookback")
	}
	s.calls.Add(1)
	return s.rows, nil
}

func TestReportedClientCacheExpiryAndSourceReset(t *testing.T) {
	for _, rows := range [][]*database.AccountDailyUsage{
		nil,
		{{Day: "2026-10-29", ClientsJSON: `[{"client_id":"CODEX_CLI","users":1}]`}},
	} {
		source := &countedReportedUsage{rows: rows}
		SetCodexReportedClientSource(source)
		t.Cleanup(func() { SetCodexReportedClientSource(nil) })
		want, wantOK := CodexClientKind(""), false
		if len(rows) > 0 {
			want, wantOK = CodexClientKindTUI, true
		}
		for range 2 {
			if got, ok := codexReportedClientKind(105); got != want || ok != wantOK {
				t.Fatalf("kind = %q/%v, want %q/%v", got, ok, want, wantOK)
			}
		}
		if source.calls.Load() != 1 {
			t.Fatalf("cache miss: %d calls", source.calls.Load())
		}
		codexReportedClientMu.Lock()
		entry := codexReportedClientCached[105]
		entry.at = time.Now().Add(-9 * time.Minute)
		codexReportedClientCached[105] = entry
		codexReportedClientMu.Unlock()
		codexReportedClientKind(105)
		if source.calls.Load() != 1 {
			t.Fatal("cache expired before ten minutes")
		}
		codexReportedClientMu.Lock()
		entry.at = time.Now().Add(-11 * time.Minute)
		codexReportedClientCached[105] = entry
		codexReportedClientMu.Unlock()
		codexReportedClientKind(105)
		if source.calls.Load() != 2 {
			t.Fatal("cache did not expire after ten minutes")
		}
		SetCodexReportedClientSource(source)
		codexReportedClientKind(105)
		if source.calls.Load() != 3 {
			t.Fatal("source replacement kept stale cache")
		}
	}
}

func TestReportedClientConcurrentLookups(t *testing.T) {
	SetCodexReportedClientSource(fakeReportedUsage{rows: []*database.AccountDailyUsage{
		{Day: "2026-10-29", ClientsJSON: `[{"client_id":"CODEX_IDE_VSCODE","users":1}]`},
	}})
	t.Cleanup(func() { SetCodexReportedClientSource(nil) })
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			if kind, ok := codexReportedClientKind(106); !ok || kind != CodexClientKindVSCode {
				t.Errorf("kind = %q/%v", kind, ok)
			}
		})
	}
	wg.Wait()
}

func TestReportedPoolPreservesSyncedPairsAndFloorErrors(t *testing.T) {
	codexTestIdentityCache(t)
	SetCodexReportedClientSource(fakeReportedUsage{rows: []*database.AccountDailyUsage{
		{Day: "2026-10-29", ClientsJSON: `[{"client_id":"CODEX_DESKTOP_APP","users":1}]`},
	}})
	t.Cleanup(func() { SetCodexReportedClientSource(nil) })
	cfg := CodexUserAgentConfig{Mode: CodexUserAgentModePool, PoolMix: map[string]int{"codex-tui": 100}}
	// Account 1 selects Windows x86_64; reported kind must still use the synced pair.
	ua, version, ok, err := codexPoolPersona(cfg, 1, "0.159.0")
	if err != nil || !ok || version != "0.159.2" || !strings.HasPrefix(ua, "Codex Desktop/0.159.2 ") || !strings.HasSuffix(ua, "Codex Desktop (Codex Desktop; 26.927.1)") {
		t.Fatalf("synced pair lost: %q, %q, %v, %v", ua, version, ok, err)
	}
	ua, version, ok, err = codexPoolPersona(cfg, 1, "0.160.0")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != ErrorCodeCodexClientVersionUnavailable || ua != "" || version != "" || ok {
		t.Fatalf("unavailable reported pair must fail without kind fallback: %q, %q, %v, %v", ua, version, ok, err)
	}
}

func TestReportedPoolUnknownAndFailureKeepOriginalMix(t *testing.T) {
	cfg := CodexUserAgentConfig{Mode: CodexUserAgentModePool, PoolMix: map[string]int{"codex-exec": 100}}
	SetCodexReportedClientSource(nil)
	t.Cleanup(func() { SetCodexReportedClientSource(nil) })
	want, wantVersion, _, err := codexPoolPersona(cfg, 110, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []fakeReportedUsage{
		{rows: []*database.AccountDailyUsage{{Day: "2026-10-29", ClientsJSON: `[{"client_id":"CODEX_UNKNOWN_DEFAULT","users":1}]`}}},
		{err: errors.New("db down")},
	} {
		SetCodexReportedClientSource(source)
		ua, version, ok, err := codexPoolPersona(cfg, 110, "")
		if err != nil || !ok || ua != want || version != wantVersion {
			t.Fatalf("mix changed without known usage: %q, %q, %v, %v", ua, version, ok, err)
		}
	}
}

func TestReportedClientDoesNotOverrideSingleOrRawPersona(t *testing.T) {
	SetCodexReportedClientSource(fakeReportedUsage{rows: []*database.AccountDailyUsage{
		{Day: "2026-10-29", ClientsJSON: `[{"client_id":"CODEX_DESKTOP_APP","users":1}]`},
	}})
	t.Cleanup(func() { SetCodexReportedClientSource(nil) })
	for _, raw := range []string{
		`{"client_kind":"codex-exec"}`,
		`{"raw_user_agent":"custom-client/1.2.3"}`,
	} {
		ua, _, ok, err := codexUserAgentFromConfigChecked(raw, 110, "")
		if err != nil || !ok || (!strings.HasPrefix(ua, "codex_exec/") && ua != "custom-client/1.2.3") {
			t.Fatalf("non-pool identity changed: %q/%v/%v", ua, ok, err)
		}
	}
}
