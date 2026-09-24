package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/codex2api/auth"
	"github.com/codex2api/internal/basispoints"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var basispointsReplay basispoints.ReplayCache

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
	RecordObservedInstructions(requestBody, headers)
	requestBody = ApplyPayloadRulesToBody(requestBody, gjson.GetBytes(requestBody, "model").String(), headers, PayloadRuleIdentityFromContext(ctx))
	// Native stateless session IDs change per request; they cannot identify a tool loop.
	if gjson.GetBytes(requestBody, "prompt_cache_key").String() == "" && headers.Get("Session_id") != "" {
		requestBody, _ = sjson.SetBytes(requestBody, "prompt_cache_key", headers.Get("Session_id"))
	}
	scope := fmt.Sprintf("%d|%x", account.ID(), sha256.Sum256([]byte(apiKey)))
	body, bridge, err := basispoints.Prepare(requestBody, scope, &basispointsReplay)
	if err != nil {
		return nil, ErrBadRequest(err.Error())
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
	resp.Header.Set("X-Codex2API-Upstream", "basispoints")
	resp.Header.Set("X-Codex2API-Reasoning-Effort", bridge.Effort)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		resp.Body = bridge.Stream(resp.Body)
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

func effectiveReasoningEffortForAccount(account *auth.Account, requested string) string {
	if CurrentRuntimeSettings().CodexBasispointsEnabled && account != nil && !account.IsRelayStyle() {
		if effort, err := basispoints.NormalizeEffort(requested); err == nil {
			return effort
		}
	}
	return requested
}
