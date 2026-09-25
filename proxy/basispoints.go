package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/codex2api/internal/basispoints"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var basispointsReplay basispoints.ReplayCache

const (
	ErrorCodeBasispointsInvalidRequest = "basispoints_invalid_request"
	basispointsBypassHeader            = "X-Codex2API-Basispoints-Bypass"
	basispointsNativeFallbackEnv       = "BASISPOINTS_NATIVE_FALLBACK"
	basispointsModelsEnv               = "BASISPOINTS_MODELS"
)

// defaultBasispointsModels are the Codex models verified to work on the
// Basispoints channel. With the switch on, every other model is served by the
// original Codex endpoint instead, because Basispoints rejects them with
// basispoints_model_access_changed. Override with BASISPOINTS_MODELS (comma
// separated); "*" or "all" keeps every model on Basispoints (the earlier
// pool-wide behavior).
var defaultBasispointsModels = []string{"gpt-5.6-sol", "gpt-6-astra"}

func basispointsAllowedModels() (models []string, all bool) {
	raw := strings.TrimSpace(os.Getenv(basispointsModelsEnv))
	if raw == "" {
		return defaultBasispointsModels, false
	}
	if raw == "*" || strings.EqualFold(raw, "all") {
		return nil, true
	}
	for _, part := range strings.Split(raw, ",") {
		if p := strings.ToLower(strings.TrimSpace(part)); p != "" {
			models = append(models, p)
		}
	}
	if len(models) == 0 {
		return defaultBasispointsModels, false
	}
	return models, false
}

// basispointsModelAllowed reports whether the Basispoints channel should serve
// this model. Matching is case-insensitive and accepts only exact names or
// valid YYYY-MM-DD snapshots of the configured family.
func basispointsModelAllowed(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return false
	}
	models, all := basispointsAllowedModels()
	if all {
		return true
	}
	for _, allowed := range models {
		if m == allowed || validBasispointsSnapshot(m, allowed) {
			return true
		}
	}
	return false
}

// basispointsActiveForModel is the per-request truth: the switch is on and this
// model is one Basispoints serves. Every request-scoped Basispoints decision
// keys on this so a non-served model behaves exactly like the original Codex
// channel would with the switch off.
func basispointsActiveForModel(model string) bool {
	return CurrentRuntimeSettings().CodexBasispointsEnabled && basispointsModelAllowed(model)
}

// basispointsPreparationCategory deliberately returns only fixed labels. The
// original validation message may contain caller-controlled tool names or modes.
func basispointsPreparationCategory(err error) string {
	return basispoints.Category(err)
}

func newBasispointsPreparationError(err error) *Error {
	return &Error{
		Code: ErrorCodeBasispointsInvalidRequest, Message: basispoints.UserMessage(err),
		Type: ErrorTypeInvalidRequest, HTTPStatus: http.StatusBadRequest,
	}
}

// basispointsNativeFallbackEnabled reports whether requests Basispoints cannot
// serve (explicit web search, hosted image tools, structured output, forced tool
// choice, embedded images without an image host) use the original Codex channel
// instead of failing. BASISPOINTS_NATIVE_FALLBACK=off keeps the pool strictly on
// Basispoints.
func basispointsNativeFallbackEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(basispointsNativeFallbackEnv))) {
	case "0", "off", "false", "no", "disabled":
		return false
	default:
		return true
	}
}

// stripBasispointsRoutingFields restores the Codex wire shape: ingress keeps
// web_search.external_web_access only so the route decision can see it.
func stripBasispointsRoutingFields(body []byte) []byte {
	for _, field := range []string{"codex_route_policy", "codex_capability_filter"} {
		body, _ = sjson.DeleteBytes(body, field)
	}
	for index, tool := range gjson.GetBytes(body, "tools").Array() {
		if strings.HasPrefix(tool.Get("type").String(), "web_search") && tool.Get(codexWebSearchExternalAccessField).Exists() {
			body, _ = sjson.DeleteBytes(body, fmt.Sprintf("tools.%d.%s", index, codexWebSearchExternalAccessField))
		}
	}
	return body
}

func markBasispointsNativeRoute(resp *http.Response, reason string) {
	if resp == nil || reason == "" {
		return
	}
	if resp.Header == nil {
		resp.Header = make(http.Header)
	}
	resp.Header.Set("X-Codex2API-Upstream", "codex")
	resp.Header.Set(basispointsBypassHeader, reason)
}

func basispointsRequestErrorCode(body []byte) string {
	code := firstGJSONString(body, "error.code", "response.error.code", "response.status_details.error.code")
	if code == "basispoints_model_access_changed" || code == "basispoints_protocol_error" || code == codexUsageRejectedCode || code == "codex_path_temporarily_unavailable" {
		return code
	}
	return ""
}

// basispointsClientErrorMessage explains request-scoped Basispoints failures to the
// caller. Protocol failures are already explained by the bridge; the upstream
// model rejection is the only one that still arrives in English.
func basispointsClientErrorMessage(code, upstreamMessage string) string {
	if code != "basispoints_model_access_changed" {
		return upstreamMessage
	}
	message := "当前模型在 Basispoints 渠道不可用，请更换模型，或关闭 Basispoints 后新建会话"
	if upstreamMessage = strings.TrimSpace(upstreamMessage); upstreamMessage != "" {
		message += "（" + upstreamMessage + "）"
	}
	return message
}

func executeBasispointsRequest(ctx context.Context, account *auth.Account, requestBody []byte, sessionID, proxyOverride, apiKey string, headers http.Header) (*http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	account.Mu().RLock()
	token, proxyURL := account.AccessToken, account.ProxyURL
	account.Mu().RUnlock()
	accountID := account.EffectiveAccountID()
	if strings.TrimSpace(token) == "" || accountID == "" || account.IsCodexAgentIdentity() {
		return nil, ErrBadRequest("Basispoints 需要 ChatGPT access token 和账号 ID，不支持 Agent Identity 凭据")
	}
	if proxyOverride != "" {
		proxyURL = proxyOverride
	}
	resetUpstreamUserAgentAudit(ctx)
	resetWsAcquireAudit(ctx)
	if codexAttemptFromContext(ctx) == nil {
		RecordObservedInstructions(requestBody, headers)
		requestBody = ApplyPayloadRulesToBody(requestBody, gjson.GetBytes(requestBody, "model").String(), headers, PayloadRuleIdentityFromContext(ctx))
	}
	// Native stateless session IDs change per request; they cannot identify a tool loop.
	explicitSessionID := ResolveExplicitSessionID(headers, requestBody)
	if explicitSessionID != "" {
		requestBody, _ = sjson.SetBytes(requestBody, "prompt_cache_key", explicitSessionID)
	}
	// A session whose replayed ciphertext Basispoints already rejected keeps
	// replaying it every turn; strip it before the upstream fails again. The
	// streaming handler still recovers the first, unremembered rejection.
	if basispointsEncryptedRejections.rejected(explicitSessionID) {
		if stripped, changed := stripInvalidEncryptedContentFromResponsesBody(requestBody); changed {
			requestBody = stripped
			log.Printf("[Basispoints] stage=prepare result=stripped_encrypted account=%d", account.ID())
		}
	}
	scope := fmt.Sprintf("%d|%x", account.ID(), sha256.Sum256([]byte(apiKey)))
	if conversation := gjson.GetBytes(requestBody, "prompt_cache_key").String(); conversation != "" {
		scope += fmt.Sprintf("|%x", sha256.Sum256([]byte(conversation)))
	}
	body, bridge, err := basispoints.Prepare(requestBody, scope, &basispointsReplay)
	if err != nil {
		log.Printf("[Basispoints] stage=prepare result=rejected code=%s category=%s account=%d", ErrorCodeBasispointsInvalidRequest, basispointsPreparationCategory(err), account.ID())
		return nil, newBasispointsPreparationError(err)
	}
	endpoint := basispoints.ResponsesURL
	client, err := getBasispointsClient(account, proxyURL)
	if err != nil {
		return nil, ErrInternalError("配置 Basispoints 代理失败", err)
	}
	if IsResinEnabled() {
		endpoint = BuildReverseProxyURL(endpoint)
		client = getResinHTTPClient(account)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, ErrInternalError("创建 Basispoints 请求失败", err)
	}
	applyAccountCustomHeaders(req, account)
	// Authentication and transport describe this request, never the Codex WS lane.
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Chatgpt-Account-Id", accountID)
	req.Header.Set("X-OpenAI-Account-Id", accountID)
	req.Header.Set("X-Basispoints-Auth-Mode", "chatgpt")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Del("Content-Encoding")
	req.Header.Del("Content-Length")
	req.Header.Del("X-Codex-Turn-State")
	req.Header.Set("X-OpenAI-Internal-Basispoints-Client-Product", "basispoints-excel-plugin")
	req.Header.Set("X-OpenAI-Internal-Basispoints-Client-Agent-Profile", "excel")
	if IsResinEnabled() {
		req.Header.Set("X-Resin-Account", ResinAccountID(account))
	}
	if bridge.RequestedEffort != "" && bridge.RequestedEffort != bridge.Effort {
		log.Printf("[Basispoints] account=%d requested_effort=%s effective_effort=%s", account.ID(), bridge.RequestedEffort, bridge.Effort)
	}
	if err := ConsumeAPIKeyModelRequestQuota(ctx, gjson.GetBytes(body, "model").String()); err != nil {
		return nil, err
	}
	resp, err := doTracedUpstreamRequest(client, req, account, proxyURL)
	if err != nil {
		if shouldRecyclePooledClient(err) {
			recycleBasispointsClient(account, proxyURL)
		}
		return nil, ErrUpstream(0, "请求 Basispoints 上游失败", err)
	}
	if resp.Header == nil {
		resp.Header = make(http.Header)
	}
	inspectCodexRouteResponse(ctx, resp)
	resp.Header.Set("X-Codex2API-Upstream", "basispoints")
	resp.Header.Set("X-Codex2API-Reasoning-Effort", bridge.Effort)
	if len(bridge.Warnings) > 0 {
		resp.Header.Set("X-Codex2API-Basispoints-Warnings", strings.Join(bridge.Warnings, "; "))
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		resp.Body = bridge.Stream(resp.Body)
		if explicitSessionID != "" {
			resp.Body = observeBasispointsEncryptedRejection(resp.Body, explicitSessionID)
		}
		resp.ContentLength = -1
		resp.Header.Del("Content-Length")
		resp.Header.Set("Content-Type", "text/event-stream")
	}
	return resp, nil
}

func executeBasispointsCompactRequest(ctx context.Context, account *auth.Account, body []byte, sessionID, proxyURL, apiKey string, headers http.Header) (*http.Response, error) {
	resp, err := executeBasispointsRequest(ctx, account, appendCompactionTriggerToResponsesBody(body), sessionID, proxyURL, apiKey, headers)
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp, err
	}
	stream := resp.Body
	defer func() {
		if err := stream.Close(); err != nil {
			log.Printf("[Basispoints] close compact stream: %v", err)
		}
	}()
	result, failed, err := collectCompactResponsesSSE(resp.Body)
	if err != nil {
		return nil, ErrUpstream(http.StatusBadGateway, "读取 Basispoints 压缩响应失败", err)
	}
	if len(failed) > 0 {
		resp.StatusCode = http.StatusBadGateway
		result = failed
	}
	resp.Body = io.NopCloser(bytes.NewReader(result))
	resp.ContentLength = int64(len(result))
	resp.Header.Set("Content-Type", "application/json")
	return resp, nil
}

func effectiveReasoningEffortForAccount(account *auth.Account, requested string, contexts ...context.Context) string {
	useBasispoints := CurrentRuntimeSettings().CodexBasispointsEnabled
	if len(contexts) > 0 {
		if d := codexRouteFromContext(contexts[0]); d != nil {
			d.mu.Lock()
			useBasispoints = d.FinalPath == database.CodexPathBasispoints
			d.mu.Unlock()
		}
	}
	if useBasispoints && account != nil && !account.IsRelayStyle() {
		if effort, err := basispoints.NormalizeEffort(requested); err == nil {
			return effort
		}
	}
	return requested
}

func validBasispointsSnapshot(model, family string) bool {
	if !strings.HasPrefix(model, family+"-") {
		return false
	}
	suffix := strings.TrimPrefix(model, family+"-")
	parsed, err := time.Parse("2006-01-02", suffix)
	return err == nil && parsed.Format("2006-01-02") == suffix
}
