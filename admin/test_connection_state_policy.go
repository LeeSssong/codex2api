package admin

import (
	"errors"
	"fmt"

	"github.com/codex2api/proxy"
)

// A local state gate does not establish an upstream account failure. In
// particular, marking it as an account error would prevent state acquisition.
func statePolicyTestFailure(err error, model string) (string, bool) {
	var policyErr *proxy.Error
	if !errors.As(err, &policyErr) || policyErr.Type != proxy.ErrorTypeServerError {
		return "", false
	}
	switch policyErr.Code {
	case "valid_state_required":
		return fmt.Sprintf("valid_state_required: 本地 State 策略拦截，模型 %s 的推理请求未发送到上游，未判定账号失效或额度不足。请在 State 管理中采集或导入该账号、该模型的有效 State；如需允许无 State 调用，可关闭“仅允许有有效 State 的账号调用”。本次拦截不修改账号状态。", model), true
	case "state_account_or_model_mismatch":
		return fmt.Sprintf("state_account_or_model_mismatch: 本地 State 归属校验未通过，模型 %s 的推理请求未发送到上游。请使用匹配当前账号和模型的 State；本次拦截不修改账号状态。", model), true
	case "ipv6_state_identity_unavailable":
		return fmt.Sprintf("ipv6_state_identity_unavailable: 本地无法确认模型 %s 的 State 账号身份，推理请求未发送到上游。请检查账号身份和 State 绑定；本次拦截不修改账号状态。", model), true
	default:
		return "", false
	}
}
