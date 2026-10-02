package smartops

import (
	"errors"
	"math"
	"sort"
	"strings"
	"time"
)

type PriorityConfig struct {
	GroupIDs           []int64  `json:"group_ids"`
	Models             []string `json:"models"`
	QualityMaxAgeHours int      `json:"quality_max_age_hours"`
	BalanceProtocols   bool     `json:"balance_protocols"`
	Enabled            bool     `json:"enabled"`
	Mode               string   `json:"mode"`
	WindowMinutes      int      `json:"window_minutes"`
	MinSamples         int      `json:"min_samples"`
	TargetTTFTMs       int      `json:"target_ttft_ms"`
	MaxLoadPercent     int      `json:"max_load_percent"`
	MinQualityPercent  int      `json:"min_quality_percent"`
	QualityWeight      float64  `json:"quality_weight"`
	LatencyWeight      float64  `json:"latency_weight"`
	LoadWeight         float64  `json:"load_weight"`
	CostWeight         float64  `json:"cost_weight"`
}

func DefaultPriorityConfig() PriorityConfig {
	return PriorityConfig{BalanceProtocols: true, QualityMaxAgeHours: 24, GroupIDs: []int64{}, Models: []string{}, Mode: "balanced", WindowMinutes: 60, MinSamples: 5, TargetTTFTMs: 3000, MaxLoadPercent: 80, MinQualityPercent: 90, QualityWeight: 30, LatencyWeight: 25, LoadWeight: 25, CostWeight: 20}
}
func ValidatePriorityConfig(c PriorityConfig) error {
	if len(c.GroupIDs) > 100 || len(c.Models) > 100 || c.QualityMaxAgeHours < 1 || c.QualityMaxAgeHours > 168 {
		return errors.New("invalid priority scope or quality age")
	}
	seen := map[int64]bool{}
	for _, id := range c.GroupIDs {
		if id <= 0 || seen[id] {
			return errors.New("invalid priority groups")
		}
		seen[id] = true
	}
	for _, m := range c.Models {
		if strings.TrimSpace(m) == "" || len(m) > 200 {
			return errors.New("invalid priority model")
		}
	}
	if c.Mode != "experience" && c.Mode != "balanced" && c.Mode != "profit" && c.Mode != "custom" {
		return errors.New("invalid scheduling mode")
	}
	if c.WindowMinutes < 5 || c.WindowMinutes > 1440 || c.MinSamples < 1 || c.TargetTTFTMs < 100 || c.MaxLoadPercent < 10 || c.MaxLoadPercent > 100 || c.MinQualityPercent < 0 || c.MinQualityPercent > 100 {
		return errors.New("invalid scheduling thresholds")
	}
	s := c.QualityWeight + c.LatencyWeight + c.LoadWeight + c.CostWeight
	for _, w := range []float64{c.QualityWeight, c.LatencyWeight, c.LoadWeight, c.CostWeight} {
		if w < 0 || math.IsNaN(w) || math.IsInf(w, 0) {
			return errors.New("weights must be finite and nonnegative")
		}
	}
	if math.IsNaN(s) || math.IsInf(s, 0) || s <= 0 {
		return errors.New("weights must have a positive sum")
	}
	return nil
}

type Signal struct {
	QualityPercent      float64
	P90TTFTMs           float64
	LoadPercent         float64
	Cost                float64
	Samples             int
	LastUsedUnix        int64
	QualityObservedUnix int64
	QualityKnown        bool
	Protocol            string
}
type Candidate struct {
	ID          int64
	Signal      Signal
	Eligible    bool
	Score       float64
	Exploration bool
}

func weights(c PriorityConfig) (float64, float64, float64, float64) {
	switch c.Mode {
	case "experience":
		return 40, 35, 20, 5
	case "profit":
		return 20, 15, 20, 45
	case "balanced":
		return 30, 25, 25, 20
	}
	return c.QualityWeight, c.LatencyWeight, c.LoadWeight, c.CostWeight
}

// RankCandidates only scores candidates already accepted by native eligibility.
// A deterministic 10% exploration cohort prevents starvation of new accounts.
func RankCandidates(c PriorityConfig, in []Candidate) []Candidate {
	if !c.Enabled {
		out := append([]Candidate(nil), in...)
		return out
	}
	qw, lw, lw2, cw := weights(c)
	protocolLast := map[string]int64{}
	var newest int64
	for _, candidate := range in {
		if candidate.Signal.LastUsedUnix > protocolLast[candidate.Signal.Protocol] {
			protocolLast[candidate.Signal.Protocol] = candidate.Signal.LastUsedUnix
		}
		if candidate.Signal.LastUsedUnix > newest {
			newest = candidate.Signal.LastUsedUnix
		}
	}
	maxCost := 0.0
	for _, x := range in {
		if x.Signal.Cost > maxCost {
			maxCost = x.Signal.Cost
		}
	}
	if maxCost == 0 {
		maxCost = 1
	}
	out := make([]Candidate, 0, len(in))
	for _, x := range in {
		if !x.Eligible {
			continue
		}
		qualityFresh := c.QualityMaxAgeHours <= 0 || x.Signal.QualityObservedUnix == 0 || time.Now().Unix()-x.Signal.QualityObservedUnix <= int64(c.QualityMaxAgeHours)*3600
		q := x.Signal.QualityPercent / 100
		if !qualityFresh || x.Signal.Samples < c.MinSamples && !x.Signal.QualityKnown {
			q = 1
		}
		latency := 1 - x.Signal.P90TTFTMs/float64(c.TargetTTFTMs)
		latency = math.Max(0, math.Min(1, latency))
		load := 1 - x.Signal.LoadPercent/100
		load = math.Max(0, math.Min(1, load))
		cost := 1 - x.Signal.Cost/maxCost
		x.Score = qw*q + lw*latency + lw2*load + cw*cost
		if c.BalanceProtocols && x.Signal.Protocol != "" {
			x.Score += math.Min(5, float64(newest-protocolLast[x.Signal.Protocol])/60)
		}
		if qualityFresh && (x.Signal.QualityKnown || x.Signal.Samples >= c.MinSamples) && x.Signal.QualityPercent < float64(c.MinQualityPercent) {
			x.Score -= qw
		}
		if x.Signal.LoadPercent > float64(c.MaxLoadPercent) {
			x.Score -= lw2
		}
		x.Exploration = x.Signal.Samples < c.MinSamples
		if x.Exploration {
			x.Score += 5
		}
		out = append(out, x)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].Signal.LastUsedUnix != out[j].Signal.LastUsedUnix {
			return out[i].Signal.LastUsedUnix < out[j].Signal.LastUsedUnix
		}
		return out[i].ID < out[j].ID
	})
	return out
}
