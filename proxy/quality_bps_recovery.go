package proxy

import (
	"context"
	"io"
	"net/http"

	"github.com/codex2api/auth"
	"github.com/tidwall/gjson"
)

// Only the leased, owned recovery worker calls this direct BPS observation.
// It bypasses the 403 permission flag but never changes accounts or routes.
func ProbeQualityBPSRecovery(ctx context.Context, account *auth.Account, model, proxyURL string) bool {
	if account == nil || account.IsCodexAgentIdentity() || account.GetAccessToken() == "" || account.EffectiveAccountID() == "" || !basispointsModelAllowed(model) {
		return false
	}
	body := codexProbeBody(model, "Reply OK.", nil)
	resp, err := executeBasispointsRequest(ctx, account, body, "", proxyURL, "", make(http.Header))
	if err != nil || resp == nil || resp.Body == nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32768))
		return false
	}
	step := consumeCodexProbeSSE(resp.Body, resp.StatusCode)
	reported := gjson.Get(step.response.Raw, "model").String()
	return ctx.Err() == nil && step.outcome == "supported" && (reported == "" || reported == model)
}
