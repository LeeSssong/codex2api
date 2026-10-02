package accountops

import (
	"fmt"
	"math"
	"strings"
)

// These controls map to the native per-account BPS route. Transport settings
// without a native implementation are not offered as policy controls.
type QualityBPSPolicy struct {
	FailureThreshold        int      `json:"failure_threshold"`
	UsagePercent            float64  `json:"usage_percent"`
	RequireAll              bool     `json:"require_all"`
	PassThreshold           int      `json:"pass_threshold"`
	HoldOnUsage             bool     `json:"hold_on_usage"`
	AllModels               bool     `json:"all_models"`
	Models                  []string `json:"models"`
	OmitUnsupportedTools    bool     `json:"omit_unsupported_tools"`
	IgnoreEncryptedContent  bool     `json:"ignore_encrypted_content"`
	AutoDisableOn403        bool     `json:"auto_disable_on_403"`
	AutoRecoverOn403        bool     `json:"auto_recover_on_403"`
	RecoveryIntervalMinutes *int     `json:"recovery_interval_minutes,omitempty"`
	AutoMoveOn403           bool     `json:"auto_move_on_403"`
	TargetGroupID           int64    `json:"target_group_id"`
	SessionProxy            bool     `json:"session_proxy"`
	ProxySource             string   `json:"proxy_source"`
	CacheCreationAsInput    bool     `json:"cache_creation_as_input"`
	WsSSEAcceleration       bool     `json:"ws_sse_acceleration"`
	AutoEnableOnDegradation bool     `json:"auto_enable_on_degradation"`
}

func ValidateQualityBPSPolicy(b *QualityBPSPolicy) error {
	if b == nil {
		return fmt.Errorf("BPS settings are required")
	}
	if b.WsSSEAcceleration {
		return fmt.Errorf("BPS WebSocket acceleration is not supported by the native transport")
	}
	if b.RecoveryIntervalMinutes != nil && (*b.RecoveryIntervalMinutes < 1 || *b.RecoveryIntervalMinutes > 10080) {
		return fmt.Errorf("BPS recovery interval must be 1-10080 minutes")
	}
	if b.AutoMoveOn403 && b.TargetGroupID < 0 {
		return fmt.Errorf("BPS 403 target group must be 0 or an existing group")
	}
	if !b.AutoMoveOn403 {
		b.TargetGroupID = 0
	}
	if b.SessionProxy && b.ProxySource != "ip_pool" {
		return fmt.Errorf("BPS session proxy requires the native ip_pool source")
	}
	if !b.SessionProxy {
		b.ProxySource = ""
	}
	if b.FailureThreshold < 0 || b.FailureThreshold > 100 || math.IsNaN(b.UsagePercent) || b.UsagePercent < 0 || b.UsagePercent > 100 {
		return fmt.Errorf("BPS trigger count must be 0-100 and usage must be 0-100 percent")
	}
	if b.FailureThreshold == 0 && b.UsagePercent == 0 {
		return fmt.Errorf("set a degraded count or usage threshold")
	}
	if b.PassThreshold == 0 {
		b.PassThreshold = 1
	}
	if b.PassThreshold < 1 || b.PassThreshold > 100 {
		return fmt.Errorf("BPS recovery count must be 1-100")
	}
	if b.AllModels {
		b.Models = nil
		return nil
	}
	seen := map[string]bool{}
	models := []string{}
	for _, model := range b.Models {
		model = strings.TrimSpace(model)
		if model == "" || seen[strings.ToLower(model)] {
			continue
		}
		if len(model) > 100 {
			return fmt.Errorf("BPS model must be at most 100 bytes")
		}
		seen[strings.ToLower(model)] = true
		models = append(models, model)
	}
	if len(models) < 1 || len(models) > 50 {
		return fmt.Errorf("select 1-50 BPS models, or all models")
	}
	b.Models = models
	return nil
}

func QualityBPSTrigger(b *QualityBPSPolicy, streak int, usage float64, hasUsage bool) string {
	if b == nil {
		return ""
	}
	degraded := b.FailureThreshold > 0 && streak >= b.FailureThreshold
	overUsage := b.UsagePercent > 0 && hasUsage && usage >= b.UsagePercent
	if b.RequireAll && b.FailureThreshold > 0 && b.UsagePercent > 0 && !(degraded && overUsage) {
		return ""
	}
	if degraded {
		return "degraded"
	}
	if overUsage {
		return "usage"
	}
	return ""
}

func QualityBPSHoldForUsage(b *QualityBPSPolicy, usage float64, hasUsage bool) bool {
	return b != nil && b.HoldOnUsage && b.UsagePercent > 0 && hasUsage && !(b.RequireAll && b.FailureThreshold > 0) && usage >= b.UsagePercent
}

func QualityBPSPassThreshold(b *QualityBPSPolicy) int {
	if b == nil || b.PassThreshold < 1 {
		return 1
	}
	return b.PassThreshold
}
