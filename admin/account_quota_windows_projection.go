package admin

import (
	"fmt"
	"math"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
)

const quotaObservationMaxAge = 10 * time.Minute

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

func (s quotaWindowSnapshot) activeStart(asOf time.Time) (time.Time, bool) {
	if s.ResetAt.IsZero() || s.Duration <= 0 || !s.ResetAt.After(asOf) {
		return time.Time{}, false
	}
	start := s.ResetAt.Add(-s.Duration)
	return start, !start.After(asOf)
}

func projectAccountQuotaWindow(s quotaWindowSnapshot, asOf time.Time, usage *database.AccountTimeRangeUsage, queryErr error) AccountQuotaWindow {
	resetUnix := int64(0)
	if !s.ResetAt.IsZero() {
		resetUnix = s.ResetAt.Unix()
	}
	out := AccountQuotaWindow{Label: s.Label, State: "unavailable", WindowID: fmt.Sprintf("%d:%d:%d", s.AccountID, int64(s.Duration/time.Second), resetUnix), AsOf: asOf.UTC()}
	if !s.ResetAt.IsZero() {
		reset := s.ResetAt.UTC()
		out.ResetAt = &reset
		start := reset.Add(-s.Duration)
		out.StartAt = &start
	}
	if !s.ObservedAt.IsZero() {
		at := s.ObservedAt.UTC()
		out.ObservedAt = &at
	}
	if s.PercentValid && finite(s.Percent) && s.Percent >= 0 {
		pct := s.Percent
		out.UsedPercent = &pct
	}
	if s.WindowChanged {
		out.State = "stale"
		out.Reason = "window_changed"
		return out
	}
	if s.ResetAt.IsZero() {
		out.Reason = "missing_window"
		return out
	}
	if !s.ResetAt.After(asOf) {
		out.State = "stale"
		out.Reason = "window_expired"
		return out
	}
	start, active := s.activeStart(asOf)
	if !active {
		out.Reason = "invalid_window"
		return out
	}
	if !s.ObservedAt.IsZero() && (s.ObservedAt.Before(start) || s.ObservedAt.After(asOf)) {
		out.State = "stale"
		out.Reason = "observation_outside_window"
		return out
	}
	if queryErr != nil {
		out.State = "error"
		out.Reason = "usage_query_failed"
		return out
	}
	if usage != nil {
		if !finite(usage.AccountBilled) || usage.AccountBilled < 0 || !finite(usage.UserBilled) || usage.UserBilled < 0 {
			out.State = "error"
			out.Reason = "invalid_usage"
			return out
		}
		used, user, requests, tokens := usage.AccountBilled, usage.UserBilled, usage.Requests, usage.Tokens
		out.UsedUSD = &used
		out.UserUSD = &user
		out.Requests = &requests
		out.Tokens = &tokens
	}
	if out.UsedPercent == nil {
		out.Reason = "missing_percent"
		return out
	}
	if s.ObservedAt.IsZero() {
		out.Reason = "missing_observation"
		return out
	}
	if asOf.Sub(s.ObservedAt) > quotaObservationMaxAge {
		out.State = "stale"
		out.Reason = "observation_stale"
		return out
	}
	if usage == nil {
		out.State = "error"
		out.Reason = "missing_usage_result"
		return out
	}
	if usage.AccountBilled == 0 {
		out.State = "no_usage"
		return out
	}
	out.State = "ready"
	if s.Percent == 0 {
		out.Reason = "zero_percent"
		return out
	}
	total := usage.AccountBilled * (100 / s.Percent)
	remaining := math.Max(0, total-usage.AccountBilled)
	if !finite(total) || !finite(remaining) {
		out.State = "error"
		out.Reason = "invalid_estimate"
		return out
	}
	out.EstimatedTotalUSD = &total
	out.EstimatedRemainingUSD = &remaining
	if s.Percent < 10 {
		out.State = "low_sample"
	}
	return out
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// Capture each window before querying. A reset change during aggregation is
// checked again before projecting, so the old aggregate cannot fill a new one.
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
				if seconds := account.GetWindow7dSeconds(); seconds > 0 && seconds <= int64(math.MaxInt64)/int64(time.Second) {
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
