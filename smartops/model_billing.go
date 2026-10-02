package smartops

import (
	"errors"
	"math"
	"regexp"
	"strings"
)

type ModelBillingRule struct {
	Model      string  `json:"model"`
	Multiplier float64 `json:"multiplier"`
}
type ModelBillingConfig struct {
	Enabled bool               `json:"enabled"`
	Rules   []ModelBillingRule `json:"rules"`
}

var billingPattern = regexp.MustCompile(`^[a-zA-Z0-9_./:-]+\*?$`)

func ValidateModelBilling(c ModelBillingConfig) error {
	if len(c.Rules) > 100 || c.Enabled && len(c.Rules) == 0 {
		return errors.New("select 1-100 model billing rules")
	}
	seen := map[string]bool{}
	for _, r := range c.Rules {
		m := strings.ToLower(strings.TrimSpace(r.Model))
		if !billingPattern.MatchString(m) || len(m) > 200 || seen[m] || math.IsNaN(r.Multiplier) || math.IsInf(r.Multiplier, 0) || r.Multiplier < 1 || r.Multiplier > 1000 {
			return errors.New("invalid model billing rule")
		}
		seen[m] = true
	}
	return nil
}
func (c ModelBillingConfig) Multiplier(model string) float64 {
	if !c.Enabled {
		return 1
	}
	model = strings.ToLower(strings.TrimSpace(model))
	value, longest := 1.0, 0
	for _, r := range c.Rules {
		pattern := strings.ToLower(strings.TrimSpace(r.Model))
		if model == pattern {
			return r.Multiplier
		}
		if prefix, ok := strings.CutSuffix(pattern, "*"); ok && len(prefix) > longest && strings.HasPrefix(model, prefix) {
			value, longest = r.Multiplier, len(prefix)
		}
	}
	return value
}
