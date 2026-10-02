package smartops

import (
	"errors"
	"math"
	"sort"
)

type PriorityConfig struct {
	Enabled                                              bool   `json:"enabled"`
	Mode                                                 string `json:"mode"`
	WindowMinutes                                        int    `json:"window_minutes"`
	MinSamples                                           int    `json:"min_samples"`
	TargetTTFTMs                                         int    `json:"target_ttft_ms"`
	MaxLoadPercent                                       int    `json:"max_load_percent"`
	MinQualityPercent                                    int    `json:"min_quality_percent"`
	QualityWeight, LatencyWeight, LoadWeight, CostWeight float64
}

func DefaultPriorityConfig() PriorityConfig {
	return PriorityConfig{Mode: "balanced", WindowMinutes: 60, MinSamples: 5, TargetTTFTMs: 3000, MaxLoadPercent: 80, MinQualityPercent: 90, QualityWeight: 30, LatencyWeight: 25, LoadWeight: 25, CostWeight: 20}
}
func ValidatePriorityConfig(c PriorityConfig) error {
	if c.Mode != "experience" && c.Mode != "balanced" && c.Mode != "profit" && c.Mode != "custom" {
		return errors.New("invalid scheduling mode")
	}
	if c.WindowMinutes < 5 || c.WindowMinutes > 1440 || c.MinSamples < 1 || c.TargetTTFTMs < 100 || c.MaxLoadPercent < 10 || c.MaxLoadPercent > 100 || c.MinQualityPercent < 0 || c.MinQualityPercent > 100 {
		return errors.New("invalid scheduling thresholds")
	}
	s := c.QualityWeight + c.LatencyWeight + c.LoadWeight + c.CostWeight
	if math.IsNaN(s) || math.IsInf(s, 0) || s <= 0 {
		return errors.New("weights must have a positive sum")
	}
	return nil
}

type Signal struct {
	QualityPercent float64
	P90TTFTMs      float64
	LoadPercent    float64
	Cost           float64
	Samples        int
	LastUsedUnix   int64
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
		q := x.Signal.QualityPercent / 100
		latency := 1 - x.Signal.P90TTFTMs/float64(c.TargetTTFTMs)
		load := 1 - x.Signal.LoadPercent/100
		cost := 1 - x.Signal.Cost/maxCost
		x.Score = qw*q + lw*latency + lw2*load + cw*cost
		x.Exploration = x.Signal.Samples < c.MinSamples
		if x.Exploration {
			x.Score += 5
		}
		out = append(out, x)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Exploration != out[j].Exploration {
			return out[i].Exploration
		}
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ID < out[j].ID
	})
	return out
}
