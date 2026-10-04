package smartops

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// OAuthAutoConfig is deliberately provider-neutral; adapters apply it to the
// native account creation request and remain responsible for eligibility.
type OAuthAutoConfig struct {
	Enabled          bool                 `json:"enabled"`
	Platform         string               `json:"platform"`
	Priority         int                  `json:"priority"`
	LoadFactor       int                  `json:"load_factor"`
	Concurrency      int                  `json:"concurrency"`
	GroupIDs         []int64              `json:"group_ids"`
	UpgradeGroupIDs  []int64              `json:"upgrade_group_ids"`
	ModelMappings    []ModelMapping       `json:"model_mappings"`
	UpgradeEnabled   bool                 `json:"upgrade_enabled"`
	SuccessesPerStep int                  `json:"successes_per_step"`
	UpgradeStep      int                  `json:"upgrade_step"`
	MaxConcurrency   int                  `json:"max_concurrency"`
	CooldownSeconds  int                  `json:"cooldown_seconds"`
	Revision         string               `json:"revision"`
	UpdatedAt        time.Time            `json:"updated_at"`
	BPS              BPSDefaults          `json:"bps"`
	ModelBilling     ModelBillingConfig   `json:"model_billing"`
	Quality5xx       Quality5xxRampConfig `json:"quality_5xx,omitempty"`
}

// Quality5xxRampConfig controls the conservative recovery path for repeated
// upstream 5xx responses on OAuth accounts. It is provider-neutral; adapters
// decide how to run the probe and persist model cooldowns.
type Quality5xxRampConfig struct {
	Enabled         bool     `json:"enabled"`
	Floor           int      `json:"floor"`
	CooldownSeconds int      `json:"cooldown_seconds"`
	Models          []string `json:"models"`
}

type BPSDefaults struct {
	WSSSEAcceleration       bool     `json:"ws_sse_acceleration"`
	AutoEnableOnDegradation bool     `json:"auto_enable_on_degradation"`
	OmitUnsupportedTools    bool     `json:"omit_unsupported_tools"`
	AutoMoveOn403           bool     `json:"auto_move_on_403"`
	TargetGroupID           int64    `json:"target_group_id"`
	CacheCreationAsInput    bool     `json:"cache_creation_as_input"`
	AllModels               bool     `json:"all_models"`
	Models                  []string `json:"models"`
	IgnoreEncryptedContent  bool     `json:"ignore_encrypted_content"`
	AutoDisableOn403        bool     `json:"auto_disable_on_403"`
	AutoRecoverOn403        bool     `json:"auto_recover_on_403"`
	RecoveryIntervalMinutes int      `json:"recovery_interval_minutes"`
	SessionProxy            bool     `json:"session_proxy"`
	ProxySource             string   `json:"proxy_source"`
}

type ModelMapping struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func DefaultOAuthAutoConfig() OAuthAutoConfig {
	return OAuthAutoConfig{Platform: "openai", Priority: 50, LoadFactor: 1, Concurrency: 3,
		ModelBilling: ModelBillingConfig{Rules: []ModelBillingRule{{Model: "gpt-6-luna*", Multiplier: 10}}},
		GroupIDs:     []int64{}, ModelMappings: []ModelMapping{{From: "gpt-5.4", To: "gpt-5.5"}},
		SuccessesPerStep: 20, UpgradeStep: 1, MaxConcurrency: 100, CooldownSeconds: 60,
		BPS:        BPSDefaults{TargetGroupID: -1, CacheCreationAsInput: true, Models: []string{"gpt-6-astra", "gpt-5.6-sol"}, IgnoreEncryptedContent: true, AutoDisableOn403: true, RecoveryIntervalMinutes: 60, ProxySource: "ip_pool"},
		Quality5xx: Quality5xxRampConfig{Enabled: false, Floor: 5, CooldownSeconds: 300}}
}

func ValidateOAuthAutoConfig(c OAuthAutoConfig) error {
	if c.BPS.WSSSEAcceleration {
		return errors.New("Basispoints WS acceleration is unavailable; native SSE transport remains active")
	}
	if c.BPS.SessionProxy && c.BPS.ProxySource != "ip_pool" {
		return errors.New("Mihomo session proxy is unavailable; select native ip_pool")
	}
	if e := ValidateModelBilling(c.ModelBilling); e != nil {
		return e
	}
	if c.Platform == "" {
		return errors.New("platform is required")
	}
	if c.Platform != "openai" && c.Platform != "claude" && c.Platform != "grok" && c.Platform != "antigravity" {
		return errors.New("unsupported OAuth platform")
	}
	if c.Priority < 0 || c.Priority > 10000 || c.LoadFactor < 1 || c.LoadFactor > 10000 || c.Concurrency < 1 || c.Concurrency > 10000 {
		return errors.New("invalid priority, load factor, or concurrency")
	}
	if c.SuccessesPerStep < 1 || c.SuccessesPerStep > 100000 || c.UpgradeStep < 1 || c.UpgradeStep > 1000 || c.MaxConcurrency < 1 || c.MaxConcurrency > 10000 || c.CooldownSeconds < 1 || c.CooldownSeconds > 86400 {
		return errors.New("invalid upgrade settings")
	}
	if c.Quality5xx.Floor < 1 || c.Quality5xx.Floor > 10000 || c.Quality5xx.CooldownSeconds < 1 || c.Quality5xx.CooldownSeconds > 86400 {
		return errors.New("invalid quality 5xx ramp settings")
	}
	for _, model := range c.Quality5xx.Models {
		if strings.TrimSpace(model) == "" || len(model) > 200 {
			return errors.New("invalid quality 5xx model")
		}
	}
	if c.Enabled && len(c.GroupIDs) == 0 {
		return errors.New("enabled configuration requires a group")
	}
	seen := map[int64]bool{}
	for _, id := range c.GroupIDs {
		if id <= 0 || seen[id] {
			return errors.New("group IDs must be positive and unique")
		}
		seen[id] = true
	}
	seen = map[int64]bool{}
	for _, id := range c.UpgradeGroupIDs {
		if id <= 0 || seen[id] {
			return errors.New("upgrade groups must be positive and unique")
		}
		seen[id] = true
	}
	if c.UpgradeEnabled && len(c.UpgradeGroupIDs) == 0 {
		return errors.New("select concurrency upgrade groups")
	}
	if !c.BPS.AllModels && len(c.BPS.Models) == 0 {
		return errors.New("select BPS models")
	}
	if c.BPS.RecoveryIntervalMinutes < 1 || c.BPS.RecoveryIntervalMinutes > 10080 {
		return errors.New("invalid BPS recovery interval")
	}
	if c.BPS.ProxySource != "mihomo" && c.BPS.ProxySource != "ip_pool" {
		return errors.New("invalid BPS proxy source")
	}
	if c.BPS.AutoRecoverOn403 && !c.BPS.AutoDisableOn403 {
		return errors.New("BPS recovery requires automatic disabling")
	}
	if c.BPS.AutoMoveOn403 && c.BPS.TargetGroupID < 0 {
		return errors.New("select BPS target group")
	}
	for _, model := range c.BPS.Models {
		if strings.TrimSpace(model) == "" || len(model) > 200 {
			return errors.New("invalid BPS model")
		}
	}
	seenModels := map[string]bool{}
	for _, m := range c.ModelMappings {
		m.From, m.To = strings.TrimSpace(m.From), strings.TrimSpace(m.To)
		if m.From == "" || m.To == "" || len(m.From) > 256 || len(m.To) > 256 || strings.ContainsAny(m.From+m.To, " \t\r\n") {
			return errors.New("invalid model mapping")
		}
		if seenModels[m.From] {
			return fmt.Errorf("duplicate mapping source %q", m.From)
		}
		seenModels[m.From] = true
	}
	return nil
}

type Quality5xxRampState struct {
	Revision            string           `json:"revision"`
	Generation          int64            `json:"generation"`
	OriginalConcurrency int              `json:"original_concurrency"`
	CurrentConcurrency  int              `json:"current_concurrency"`
	Active              bool             `json:"active"`
	Attempt             uint64           `json:"attempt"`
	ProbePending        bool             `json:"probe_pending"`
	CooldownUntil       time.Time        `json:"cooldown_until"`
	OwnedModels         []string         `json:"owned_models,omitempty"`
	Concurrency         ConcurrencyState `json:"concurrency"`
}

func (s Quality5xxRampState) ResetOnFailure(c Quality5xxRampConfig, current int, generation int64, revision string, now time.Time) Quality5xxRampState {
	if !c.Enabled {
		return s
	}
	original := current
	if current > c.Floor {
		current = c.Floor
	}
	if !s.Active || s.Generation != generation || s.Revision != revision || s.OriginalConcurrency < current {
		s.OriginalConcurrency = original
	}
	s.Revision, s.Generation, s.CurrentConcurrency = revision, generation, current
	s.Active, s.ProbePending, s.Attempt = true, true, s.Attempt+1
	s.CooldownUntil = now.Add(time.Duration(c.CooldownSeconds) * time.Second)
	if s.Concurrency.Concurrency == 0 {
		s.Concurrency = ConcurrencyState{Revision: revision, Concurrency: current}
	} else {
		s.Concurrency.Revision = revision
		s.Concurrency.Concurrency = current
		s.Concurrency.Successes = 0
	}
	return s
}

func (s Quality5xxRampState) ProbeResult(c OAuthAutoConfig, current int, generation int64, revision string, passed bool, conclusive bool, now time.Time) (Quality5xxRampState, int) {
	if !s.Active || s.Generation != generation || s.Revision != revision || !s.ProbePending {
		return s, current
	}
	if !conclusive {
		s.ProbePending = true
		s.CooldownUntil = now.Add(time.Duration(c.Quality5xx.CooldownSeconds) * time.Second)
		return s, current
	}
	if !passed {
		s.ProbePending = true
		s.CooldownUntil = now.Add(time.Duration(c.Quality5xx.CooldownSeconds) * time.Second)
		return s, current
	}
	s.ProbePending = false
	// Release only our owned cooldown state; progression remains success based.
	s.Concurrency, current = AdvanceConcurrency(s.Concurrency, current, c, ConcurrencyResult{Success: true, At: now}, now)
	if current >= s.OriginalConcurrency {
		s.Active = false
		s.CurrentConcurrency = current
		return s, current
	}
	s.CurrentConcurrency = current
	return s, current
}

// ApplyModelMappings copies a credential map and preserves explicit mappings.
func ApplyModelMappings(credentials map[string]string, rules []ModelMapping) map[string]string {
	out := make(map[string]string, len(credentials)+len(rules))
	for k, v := range credentials {
		out[k] = v
	}
	for _, rule := range rules {
		if current, ok := out[rule.From]; !ok || current == rule.From {
			out[rule.From] = rule.To
		}
	}
	return out
}

type ConcurrencyState struct {
	Revision               string `json:"revision"`
	Concurrency, Successes int
	PausedUntil            time.Time
}
type ConcurrencyResult struct {
	Success bool
	At      time.Time
}

// AdvanceConcurrency is pure and only increases after a complete success cycle.
func AdvanceConcurrency(s ConcurrencyState, current int, c OAuthAutoConfig, result ConcurrencyResult, now time.Time) (ConcurrencyState, int) {
	if s.Revision != c.Revision || s.Concurrency != current {
		s = ConcurrencyState{Revision: c.Revision, Concurrency: current}
	}
	if !result.Success || !c.UpgradeEnabled || now.Before(s.PausedUntil) {
		if !result.Success {
			s.Successes = 0
			s.PausedUntil = now.Add(time.Duration(c.CooldownSeconds) * time.Second)
		}
		return s, current
	}
	s.Successes++
	if s.Successes >= c.SuccessesPerStep && current < c.MaxConcurrency {
		current += c.UpgradeStep
		if current > c.MaxConcurrency {
			current = c.MaxConcurrency
		}
		s.Successes = 0
		s.Concurrency = current
	}
	return s, current
}
