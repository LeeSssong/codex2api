package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/cache"
	"github.com/codex2api/database"
	"github.com/codex2api/proxy"
	"github.com/gin-gonic/gin"
)

func TestQuotaWindowPredictionAndStates(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	base := quotaWindowSnapshot{AccountID: 42, Label: "5h", Duration: 5 * time.Hour, ResetAt: now.Add(time.Hour), ObservedAt: now.Add(-time.Minute), Percent: 30, PercentValid: true}
	tests := []struct {
		name             string
		mutate           func(*quotaWindowSnapshot)
		usage            *database.AccountTimeRangeUsage
		err              error
		state            string
		total, remaining *float64
	}{
		{name: "source formula", usage: &database.AccountTimeRangeUsage{Requests: 3, Tokens: 45, AccountBilled: 12, UserBilled: 18}, state: "ready", total: quotaTestFloat(40), remaining: quotaTestFloat(28)},
		{name: "low sample", mutate: func(s *quotaWindowSnapshot) { s.Percent = 9 }, usage: &database.AccountTimeRangeUsage{AccountBilled: 9}, state: "ready", total: quotaTestFloat(100), remaining: quotaTestFloat(91)},
		{name: "weekly uses same formula", mutate: func(s *quotaWindowSnapshot) { s.Label = "7d"; s.Duration = 7 * 24 * time.Hour }, usage: &database.AccountTimeRangeUsage{AccountBilled: 12}, state: "ready", total: quotaTestFloat(40), remaining: quotaTestFloat(28)},
		{name: "monthly uses same formula", mutate: func(s *quotaWindowSnapshot) { s.Label = "30d"; s.Duration = 30 * 24 * time.Hour }, usage: &database.AccountTimeRangeUsage{AccountBilled: 12}, state: "ready", total: quotaTestFloat(40), remaining: quotaTestFloat(28)},
		{name: "tiny positive cost", usage: &database.AccountTimeRangeUsage{AccountBilled: 0.003}, state: "ready", total: quotaTestFloat(0.01), remaining: quotaTestFloat(0.007)},
		{name: "zero percent with local charges", mutate: func(s *quotaWindowSnapshot) { s.Percent = 0 }, usage: &database.AccountTimeRangeUsage{AccountBilled: 12}, state: "ready"},
		{name: "negative percent", mutate: func(s *quotaWindowSnapshot) { s.Percent = -1 }, state: "unavailable"},
		{name: "invalid user cost", usage: &database.AccountTimeRangeUsage{AccountBilled: 12, UserBilled: math.Inf(1)}, state: "error"},
		{name: "zero percent", mutate: func(s *quotaWindowSnapshot) { s.Percent = 0 }, usage: &database.AccountTimeRangeUsage{}, state: "no_usage"},
		{name: "zero local usage", usage: &database.AccountTimeRangeUsage{}, state: "no_usage"},
		{name: "no snapshot", mutate: func(s *quotaWindowSnapshot) { s.PercentValid = false }, state: "unavailable"},
		{name: "no reset", mutate: func(s *quotaWindowSnapshot) { s.ResetAt = time.Time{} }, state: "unavailable"},
		{name: "expired", mutate: func(s *quotaWindowSnapshot) { s.ResetAt = now }, state: "unavailable"},
		{name: "previous window observation", mutate: func(s *quotaWindowSnapshot) { s.ObservedAt = now.Add(-5 * time.Hour) }, state: "unavailable"},
		{name: "missing observation", mutate: func(s *quotaWindowSnapshot) { s.ObservedAt = time.Time{} }, state: "unavailable"},
		{name: "query failed", err: errors.New("query failed"), state: "error"},
		{name: "invalid percentage", mutate: func(s *quotaWindowSnapshot) { s.Percent = math.NaN() }, state: "unavailable"},
		{name: "invalid cost", usage: &database.AccountTimeRangeUsage{AccountBilled: math.Inf(1)}, state: "error"},
		{name: "negative cost", usage: &database.AccountTimeRangeUsage{AccountBilled: -1}, state: "ready"},
		{name: "overflow", mutate: func(s *quotaWindowSnapshot) { s.Percent = 1e-300 }, usage: &database.AccountTimeRangeUsage{AccountBilled: 1e300}, state: "ready"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := base
			if tt.mutate != nil {
				tt.mutate(&s)
			}
			got := projectAccountQuotaWindow(s, now, tt.usage, tt.err)
			if got.State != tt.state {
				t.Fatalf("state=%s want %s: %+v", got.State, tt.state, got)
			}
			if tt.total == nil {
				if got.EstimatedTotalUSD != nil || got.EstimatedRemainingUSD != nil {
					t.Fatalf("unexpected prediction: %+v", got)
				}
			} else if got.EstimatedTotalUSD == nil || math.Abs(*got.EstimatedTotalUSD-*tt.total) > 1e-12 || got.EstimatedRemainingUSD == nil || math.Abs(*got.EstimatedRemainingUSD-*tt.remaining) > 1e-12 {
				t.Fatalf("prediction = %+v", got)
			}
			if _, err := json.Marshal(got); err != nil {
				t.Fatalf("non-finite JSON: %v", err)
			}
		})
	}
	got := projectAccountQuotaWindow(base, now, &database.AccountTimeRangeUsage{Requests: 3, Tokens: 45, AccountBilled: 12, UserBilled: 18}, nil)
	if got.WindowID != "42:18000:1793106000" || got.Requests == nil || *got.Requests != 3 || got.Tokens == nil || *got.Tokens != 45 || got.UserUSD == nil || *got.UserUSD != 18 {
		t.Fatalf("metadata and same-window metrics: %+v", got)
	}
}
func quotaTestFloat(v float64) *float64 { return &v }

func TestQuotaWindowCompletedSnapshotRemainsCoherentAfterLiveReset(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	s := quotaWindowSnapshot{AccountID: 1, Label: "5h", Duration: 5 * time.Hour, ResetAt: now.Add(time.Hour), Percent: 30, PercentValid: true, WindowChanged: true}
	got := projectAccountQuotaWindow(s, now, &database.AccountTimeRangeUsage{Requests: 2, AccountBilled: 12}, nil)
	if got.State != "ready" || got.UsedUSD == nil || *got.UsedUSD != 12 || got.EstimatedTotalUSD == nil || *got.EstimatedTotalUSD != 40 {
		t.Fatalf("coherent response incorrectly invalidated: %+v", got)
	}
}

func TestAccountQuotaPageStatsAlignsWindowsAndLegacyAliases(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbPath := filepath.Join(t.TempDir(), "quota.sqlite")
	db, err := database.New("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	id, err := db.InsertAccountWithCredentials(ctx, "quota-test", map[string]interface{}{"refresh_token": "rt-quota-test", "access_token": "at-quota-test"}, "")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, r := range []struct {
		model      string
		cost, user float64
		tokens     int64
	}{{"gpt-5.4", 12, 18, 60}, {"gpt-5.3-codex-spark", 30, 45, 100}} {
		if _, err := conn.ExecContext(ctx, `INSERT INTO usage_logs(account_id,status_code,model,account_billed,user_billed,total_tokens,created_at) VALUES($1,200,$2,$3,$4,$5,$6)`, id, r.model, r.cost, r.user, r.tokens, time.Now().Add(-time.Minute).UTC().Format("2006-01-02 15:04:05")); err != nil {
			t.Fatal(err)
		}
	}
	store := auth.NewStore(db, nil, nil)
	store.SetLazyMode(true)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	account := store.FindByID(id)
	now := time.Now()
	account.SetUsageSnapshot5hAt(30, now.Add(time.Hour), now)
	account.SetUsageSnapshot(30, now)
	account.SetReset7dAt(now.Add(24 * time.Hour))
	account.SetWindow7dSeconds(30 * 24 * 60 * 60)
	tokenCache := cache.NewMemory(1)
	t.Cleanup(func() { tokenCache.Close() })
	h := NewHandler(store, db, tokenCache, nil, "")
	h.queryWhamDailyUsage = func(context.Context, *auth.Account, string, string, string) (*proxy.WhamDailyUsageResponse, *http.Response, error) {
		t.Error("reading quota page must not probe upstream")
		return nil, nil, nil
	}
	stats := invokeAccountPageStats(t, h, []int64{id})
	got := stats[strconv.FormatInt(id, 10)]
	for _, q := range []*AccountQuotaWindow{got.Quota5h, got.Quota7d} {
		if q == nil || q.State != "ready" || q.UsedUSD == nil || *q.UsedUSD != 12 || q.EstimatedTotalUSD == nil || *q.EstimatedTotalUSD != 40 || q.Requests == nil || *q.Requests != 1 || q.Tokens == nil || *q.Tokens != 60 {
			t.Fatalf("quota=%+v", q)
		}
	}
	if got.Quota7d.Label != "7d" || got.Quota7d.StartAt == nil || !got.Quota7d.StartAt.Equal(now.Add(-6*24*time.Hour)) {
		t.Fatalf("monthly quota=%+v", got.Quota7d)
	}
	if got.Billed5h == nil || *got.Billed5h != 12 || got.Billed7d == nil || *got.Billed7d != 12 || got.Usage5hDetail == nil || got.Usage5hDetail.Requests != 1 || got.Usage7dDetail == nil || got.Usage7dDetail.Tokens != 60 {
		t.Fatalf("legacy aliases=%+v", got)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO usage_logs(account_id,status_code,model,account_billed,created_at) VALUES($1,200,'gpt-5.4',$2,$3)`, id, math.Inf(1), time.Now().Add(-time.Minute).UTC().Format("2006-01-02 15:04:05")); err != nil {
		t.Fatal(err)
	}
	invalid := invokeAccountPageStats(t, h, []int64{id})[strconv.FormatInt(id, 10)]
	if invalid.Quota5h == nil || invalid.Quota5h.State != "error" || invalid.Billed5h != nil || invalid.Usage5hDetail != nil {
		t.Fatalf("non-finite values must not reach JSON aliases: %+v", invalid)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/accounts/page-stats?ids="+strconv.FormatInt(id, 10), nil).WithContext(canceled)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	h.GetAccountPageStats(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("quota errors should remain per-window states, got %d: %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Stats map[string]accountPageStatsItem `json:"stats"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	failed := response.Stats[strconv.FormatInt(id, 10)]
	if failed.Quota5h == nil || failed.Quota5h.State != "error" || failed.Quota5h.UsedUSD != nil || failed.Billed5h != nil {
		t.Fatalf("query failure must not become zero: %+v", failed)
	}
}

func TestQuotaWindowSub2DoesNotExpirePredictions(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	for _, observed := range []time.Time{time.Time{}, now.Add(-time.Hour), now.Add(-10 * 24 * time.Hour)} {
		s := quotaWindowSnapshot{AccountID: 1, Label: "7d", Duration: 7 * 24 * time.Hour, ResetAt: now.Add(time.Hour), ObservedAt: observed, Percent: 1, PercentValid: true}
		got := projectAccountQuotaWindow(s, now, &database.AccountTimeRangeUsage{AccountBilled: 12}, nil)
		if got.State != "ready" || got.EstimatedTotalUSD == nil || *got.EstimatedTotalUSD != 1200 {
			t.Fatalf("source retains valid estimate regardless of timestamp: %+v", got)
		}
	}
}
func TestQuotaWindowSub2RollingFallbackKeepsCosts(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	for _, reset := range []time.Time{time.Time{}, now, now.Add(-time.Hour)} {
		s := quotaWindowSnapshot{AccountID: 1, Label: "5h", Duration: 5 * time.Hour, ResetAt: reset, Percent: 30, PercentValid: true}
		starts := quotaWindowStarts(map[int64]quotaWindowSnapshot{1: s}, now)
		if !starts[1].Equal(now.Add(-5 * time.Hour)) {
			t.Fatalf("fallback start=%v", starts[1])
		}
		got := projectAccountQuotaWindow(s, now, &database.AccountTimeRangeUsage{AccountBilled: 12}, nil)
		if got.UsedUSD == nil || *got.UsedUSD != 12 {
			t.Fatalf("fallback cost disappeared: %+v", got)
		}
		if reset.IsZero() {
			if got.EstimatedTotalUSD == nil || *got.EstimatedTotalUSD != 40 {
				t.Fatalf("missing reset with known percentage: %+v", got)
			}
		} else if got.UsedPercent == nil || *got.UsedPercent != 0 || got.EstimatedTotalUSD != nil {
			t.Fatalf("expired cycle should zero percentage: %+v", got)
		}
	}
}

func TestQuotaSourceFeePredictionAndHistoryProvenance(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	for _, legacy := range []int64{0, 1, 2} {
		usage := &database.AccountTimeRangeUsage{Requests: 2, Tokens: 2200, AccountBilled: .0071, StandardCost: .00355, UserBilled: .009, LegacyRequests: legacy}
		s := quotaWindowSnapshot{AccountID: 1, Label: "7d", Duration: 7 * 24 * time.Hour, ResetAt: now.Add(time.Hour), Percent: 20, PercentValid: true}
		q := projectAccountQuotaWindow(s, now, usage, nil)
		wantBasis := map[int64]string{0: "snapshot", 1: "mixed", 2: "legacy"}[legacy]
		if q.CostBasis != wantBasis || q.LegacyRequests != legacy || q.EstimatedTotalUSD == nil || math.Abs(*q.EstimatedTotalUSD-.0355) > 1e-12 || q.StandardUSD == nil || *q.StandardUSD != .00355 || q.UserUSD == nil || *q.UserUSD != .009 {
			t.Fatalf("fee projection/provenance=%+v", q)
		}
	}
}
