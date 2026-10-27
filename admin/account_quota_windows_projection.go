package admin

import (
	"fmt"
	"math"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
)

// AccountQuotaWindow is the shared list/detail quota projection. Optional
// metrics remain absent when their source is unavailable or a query fails.
type AccountQuotaWindow struct {
	Label                 string     `json:"label"`
	State                 string     `json:"state"`
	Reason                string     `json:"reason,omitempty"`
	WindowID              string     `json:"window_id"`
	StartAt               *time.Time `json:"start_at,omitempty"`
	ResetAt               *time.Time `json:"reset_at,omitempty"`
	AsOf                  time.Time  `json:"as_of"`
	ObservedAt            *time.Time `json:"observed_at,omitempty"`
	UsedPercent           *float64   `json:"used_percent,omitempty"`
	UsedUSD               *float64   `json:"used_usd,omitempty"`
	EstimatedTotalUSD     *float64   `json:"estimated_total_usd,omitempty"`
	EstimatedRemainingUSD *float64   `json:"estimated_remaining_usd,omitempty"`
	Requests              *int64     `json:"requests,omitempty"`
	Tokens                *int64     `json:"tokens,omitempty"`
	UserUSD               *float64   `json:"user_usd,omitempty"`
	StandardUSD           *float64   `json:"standard_usd,omitempty"`
	CostBasis             string     `json:"cost_basis,omitempty"`
	LegacyRequests        int64      `json:"legacy_requests,omitempty"`
}

type quotaWindowSnapshot struct {
	AccountID     int64
	Label         string
	Duration      time.Duration
	ResetAt       time.Time
	ObservedAt    time.Time
	Percent       float64
	PercentValid  bool
	WindowChanged bool
}

// Source codexWindowStatsStart uses the official reset when valid, otherwise
// a rolling window. Cached percentages remain usable until a confirmed reset.
func (s quotaWindowSnapshot) activeStart(asOf time.Time) (time.Time, bool) {
	if s.Duration <= 0 {
		return time.Time{}, false
	}
	if !s.ResetAt.IsZero() && s.ResetAt.After(asOf) {
		return s.ResetAt.Add(-s.Duration), true
	}
	return asOf.Add(-s.Duration), true
}

func projectAccountQuotaWindow(s quotaWindowSnapshot, asOf time.Time, usage *database.AccountTimeRangeUsage, queryErr error) AccountQuotaWindow {
	resetUnix := int64(0)
	if !s.ResetAt.IsZero() {
		resetUnix = s.ResetAt.Unix()
	}
	out := AccountQuotaWindow{Label: s.Label, State: "unavailable", WindowID: fmt.Sprintf("%d:%d:%d", s.AccountID, int64(s.Duration/time.Second), resetUnix), AsOf: asOf.UTC()}
	if start, ok := s.activeStart(asOf); ok {
		start = start.UTC()
		out.StartAt = &start
	}
	if !s.ResetAt.IsZero() {
		reset := s.ResetAt.UTC()
		out.ResetAt = &reset
	}
	if !s.ObservedAt.IsZero() {
		observed := s.ObservedAt.UTC()
		out.ObservedAt = &observed
	}
	if queryErr != nil {
		out.State = "error"
		out.Reason = "usage_query_failed"
		return out
	}
	if usage == nil {
		out.Reason = "missing_usage_result"
		return out
	}
	if !finite(usage.AccountBilled) || !finite(usage.UserBilled) {
		out.State = "error"
		out.Reason = "invalid_usage"
		return out
	}
	used, user, requests, tokens := usage.AccountBilled, usage.UserBilled, usage.Requests, usage.Tokens
	out.UsedUSD = &used
	out.UserUSD = &user
	out.Requests = &requests
	out.Tokens = &tokens
	standard := usage.StandardCost
	if finite(standard) {
		out.StandardUSD = &standard
	}
	out.CostBasis = "snapshot"
	out.LegacyRequests = usage.LegacyRequests
	if usage.LegacyRequests > 0 {
		out.CostBasis = "mixed"
		if usage.LegacyRequests == usage.Requests {
			out.CostBasis = "legacy"
		}
	}
	// Sub2 creates a zero-utilization progress object when only local stats exist.
	pct := 0.0
	if s.PercentValid && finite(s.Percent) {
		pct = s.Percent
	}
	if !s.ResetAt.IsZero() && !s.ResetAt.After(asOf) {
		pct = 0
	}
	out.UsedPercent = &pct
	out.State = "ready"
	if used == 0 {
		out.State = "no_usage"
	}
	// Exact source eligibility: positive finite account cost and utilization.
	// Neither timestamp freshness nor sample percentage suppresses a prediction.
	if used <= 0 || pct <= 0 {
		return out
	}
	total := used * 100 / pct
	if !finite(total) || total <= 0 {
		return out
	}
	remaining := math.Max(0, total-used)
	out.EstimatedTotalUSD = &total
	out.EstimatedRemainingUSD = &remaining
	return out
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// Capture one coherent snapshot before aggregation. A later live observation
// triggers a new response instead of invalidating the completed snapshot.
func (h *Handler) accountQuotaSnapshots(ids []int64) (map[int64]quotaWindowSnapshot, map[int64]quotaWindowSnapshot) {
	short, long := make(map[int64]quotaWindowSnapshot, len(ids)), make(map[int64]quotaWindowSnapshot, len(ids))
	for _, id := range ids {
		s := quotaWindowSnapshot{AccountID: id, Label: "5h", Duration: 5 * time.Hour}
		l := quotaWindowSnapshot{AccountID: id, Label: "7d", Duration: 7 * 24 * time.Hour}
		if h.store != nil {
			if account := h.store.FindByID(id); account != nil {
				live := account.GetAccountLiveUsageSnapshot()
				s.ResetAt, s.ObservedAt, s.Percent, s.PercentValid = live.Reset5hAt, live.UsageUpdatedAt5h, live.UsagePercent5h, live.UsagePercent5hValid
				l.ResetAt, l.ObservedAt, l.Percent, l.PercentValid = live.Reset7dAt, live.UsageUpdatedAt, live.UsagePercent7d, live.UsagePercent7dValid
				if seconds := account.GetWindow7dSeconds(); (account.IsClaudeOAuth() || account.IsGrokAPI() || account.IsAntigravityAPI()) && seconds > 0 && seconds <= int64(math.MaxInt64)/int64(time.Second) {
					l.Duration = time.Duration(seconds) * time.Second
					if auth.IsMonthlyWindowSeconds(seconds) {
						l.Label = "30d"
					}
				}
			}
		}
		short[id], long[id] = s, l
	}
	return short, long
}

func quotaWindowStarts(snapshots map[int64]quotaWindowSnapshot, asOf time.Time) map[int64]time.Time {
	starts := make(map[int64]time.Time, len(snapshots))
	for id, s := range snapshots {
		if start, ok := s.activeStart(asOf); ok {
			starts[id] = start
		}
	}
	return starts
}
