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
	Enabled          bool           `json:"enabled"`
	Platform         string         `json:"platform"`
	Priority         int            `json:"priority"`
	LoadFactor       int            `json:"load_factor"`
	Concurrency      int            `json:"concurrency"`
	GroupIDs         []int64        `json:"group_ids"`
	ModelMappings    []ModelMapping `json:"model_mappings"`
	UpgradeEnabled   bool           `json:"upgrade_enabled"`
	SuccessesPerStep int            `json:"successes_per_step"`
	UpgradeStep      int            `json:"upgrade_step"`
	MaxConcurrency   int            `json:"max_concurrency"`
	CooldownSeconds  int            `json:"cooldown_seconds"`
	Revision         string         `json:"revision"`
	UpdatedAt        time.Time      `json:"updated_at"`
	BPS              BPSDefaults    `json:"bps"`
}

type BPSDefaults struct {
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
		GroupIDs: []int64{}, ModelMappings: []ModelMapping{{From: "gpt-5.4", To: "gpt-5.5"}},
		SuccessesPerStep: 20, UpgradeStep: 1, MaxConcurrency: 100, CooldownSeconds: 60,
		BPS: BPSDefaults{Models: []string{"gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-terra"}, IgnoreEncryptedContent: true, AutoDisableOn403: true, RecoveryIntervalMinutes: 60, ProxySource: "mihomo"}}
}

func ValidateOAuthAutoConfig(c OAuthAutoConfig) error {
	if c.Platform == "" {
		return errors.New("platform is required")
	}
	if c.Priority < 0 || c.Priority > 10000 || c.LoadFactor < 1 || c.LoadFactor > 10000 || c.Concurrency < 1 || c.Concurrency > 10000 {
		return errors.New("invalid priority, load factor, or concurrency")
	}
	if c.SuccessesPerStep < 1 || c.SuccessesPerStep > 100000 || c.UpgradeStep < 1 || c.UpgradeStep > 1000 || c.MaxConcurrency < 1 || c.MaxConcurrency > 10000 || c.CooldownSeconds < 1 || c.CooldownSeconds > 86400 {
		return errors.New("invalid upgrade settings")
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
