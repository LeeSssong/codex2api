package proxy

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/tidwall/gjson"
)

type CodexCapabilityProbeOptions struct {
	Model    string
	Level    string
	ProxyURL string
}

type CodexProbeResult = database.CodexProbeResult

const codexProbeTool = "codex2api_probe_echo"

// ProbeCodexCapability deliberately calls the BPS executor directly. No native
// executor, scheduler, model substitution or business-Key capability gate exists
// in this path. It exercises the same BPS request/stream bridge as real traffic.
func ProbeCodexCapability(ctx context.Context, account *auth.Account, options CodexCapabilityProbeOptions) (result CodexProbeResult) {
	if ctx == nil {
		ctx = context.Background()
	}
	result = CodexProbeResult{Model: strings.TrimSpace(options.Model), Upstream: database.CodexPathBasispoints, Level: options.Level, Outcome: "error", Capability: database.CapabilityUnknown, BasicOutcome: "not_run", ToolsOutcome: "not_run", StartedAt: time.Now().UTC()}
	if result.Level == "" {
		result.Level = "basic"
	}
	var generation int64
	var evidence *database.CodexCapability
	if account != nil {
		result.AccountID = account.ID()
		generation = account.GetCredentialGeneration()
	}
	defer func() {
		result.FinishedAt = time.Now().UTC()
		result.DurationMS = result.FinishedAt.Sub(result.StartedAt).Milliseconds()
		if account == nil || result.Model == "" || len(result.Model) > 200 || (result.Level != "basic" && result.Level != "tools") || result.ErrorCode == "probe_in_progress" {
			return
		}
		if evidence != nil {
			result.Capability = evidence.Capability
		}
		// Cancellation still records the bounded diagnostic outcome; it never
		// makes another upstream request or extends a canceled request's work.
		applied, err := account.SaveCodexCapabilityProbeResult(context.WithoutCancel(ctx), generation, result, evidence)
		if err != nil {
			result.Message += "; result persistence failed"
			log.Printf("[CodexProbe] persistence failed account=%d", account.ID())
		} else if !applied {
			result.Outcome, result.Capability, result.ErrorCode, result.Message = "skipped", database.CapabilityUnknown, "stale_probe_result", "A newer test or credential replacement superseded this result"
			if account.GetCredentialGeneration() != generation {
				result.ErrorCode, result.Message = "credentials_changed", "Credentials changed during the test; result discarded"
			}
		}
	}()
	setFailure := func(outcome, code, message string) {
		result.Outcome, result.ErrorCode, result.Message = outcome, code, message
	}
	if account == nil || result.Model == "" || len(result.Model) > 200 || strings.ContainsAny(result.Model, "\r\n\x00") || (result.Level != "basic" && result.Level != "tools") {
		setFailure("error", "invalid_probe_options", "A valid account, exact model and basic/tools level are required")
		return
	}
	if ctx.Err() != nil {
		setFailure("canceled", "probe_canceled", "Test canceled")
		return
	}
	if !basispointsActiveForModel(result.Model) {
		setFailure("blocked", "basispoints_disabled_or_model_not_allowed", "BPS is disabled or this exact model is not allowed")
		return
	}
	if account.IsRelayStyle() || account.IsCodexAgentIdentity() || account.GetAccessToken() == "" || account.EffectiveAccountID() == "" {
		setFailure("blocked", "oauth_identity_required", "BPS requires a ChatGPT OAuth account and workspace identity")
		return
	}
	if err := account.ReloadCodexRoutes(ctx); err != nil {
		setFailure("blocked", "configuration_unavailable", "Routing configuration is unavailable")
		return
	}
	result.Capability = account.CodexPathSnapshot(result.Upstream, result.Model, time.Now()).Capability
	release, reason := account.BeginCodexCapabilityProbe(ctx, result.Model)
	if reason != "" {
		setFailure("blocked", reason, "Account, route or cooldown policy prevents this test")
		return
	}
	defer release()
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		setFailure("error", "nonce_generation_failed", "Could not create test nonce")
		return
	}
	nonce := "c2a_probe_" + hex.EncodeToString(random[:])
	session := "bps-strong-" + hex.EncodeToString(random[:])
	basePrompt := "Reply with exactly this nonce and nothing else: " + nonce
	body := codexProbeBody(result.Model, basePrompt, nil)
	first := executeCodexProbeStep(ctx, account, generation, options.ProxyURL, session, result.Model, body)
	applyCodexProbeStep(&result, first)
	if first.outcome == "supported" && !codexProbeTextMatches(first.response, nonce) {
		setFailure("protocol_error", "unexpected_test_output", "Completed response did not contain the expected nonce")
	}
	result.BasicOutcome = result.Outcome
	if result.Outcome != "supported" {
		if first.outcome == "unsupported" {
			evidence = codexProbeEvidence(result, database.CapabilityUnsupported, first.code)
		}
		return
	}
	evidence = codexProbeEvidence(result, database.CapabilitySupported, "basic_completed")
	if result.Level == "basic" {
		return
	}
	toolPrompt := "Call " + codexProbeTool + " exactly once with nonce " + nonce + ". After receiving the tool result, reply with exactly the returned nonce and nothing else. Do not run any other tool."
	tool := map[string]any{"type": "function", "name": codexProbeTool, "description": "Harmless local test: echoes a nonce without any external action.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"nonce": map[string]any{"type": "string"}}, "required": []string{"nonce"}, "additionalProperties": false}}
	body = codexProbeBody(result.Model, toolPrompt, []any{tool})
	callStep := executeCodexProbeStep(ctx, account, generation, options.ProxyURL, session, result.Model, body)
	applyCodexProbeStep(&result, callStep)
	result.ToolsOutcome = result.Outcome
	if callStep.outcome != "supported" {
		if callStep.outcome == "unsupported" {
			evidence = codexProbeEvidence(result, database.CapabilityUnsupported, callStep.code)
		}
		return
	}
	call, ok := codexProbeValidatedCall(callStep.response, nonce)
	if !ok {
		setFailure("protocol_error", "invalid_tool_call", "Expected exactly one nonce echo call with encrypted_function_args: []")
		result.ToolsOutcome = result.Outcome
		return
	}
	var outputs []any
	if err := json.Unmarshal([]byte(callStep.response.Get("output").Raw), &outputs); err != nil {
		setFailure("protocol_error", "invalid_tool_history", "Tool history could not be decoded")
		result.ToolsOutcome = result.Outcome
		return
	}
	input := []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": toolPrompt}}}}
	input = append(input, outputs...)
	input = append(input, map[string]any{"type": "function_call_output", "call_id": call.Get("call_id").String(), "output": nonce})
	body = codexProbeBody(result.Model, input, []any{tool})
	last := executeCodexProbeStep(ctx, account, generation, options.ProxyURL, session, result.Model, body)
	applyCodexProbeStep(&result, last)
	if last.outcome == "unsupported" {
		evidence = codexProbeEvidence(result, database.CapabilityUnsupported, last.code)
	}
	if last.outcome == "supported" && !codexProbeTextMatches(last.response, nonce) {
		setFailure("protocol_error", "unexpected_tool_output", "Tool roundtrip did not return the expected nonce")
	}
	result.ToolsOutcome = result.Outcome
	if result.Outcome == "supported" {
		result.Message = "BPS basic request and tool roundtrip completed successfully"
	}
	return
}

func codexProbeEvidence(result CodexProbeResult, capability, reason string) *database.CodexCapability {
	return &database.CodexCapability{Upstream: result.Upstream, Model: result.Model, Capability: capability, Source: "bps_strong_probe", Reason: reason, ObservedAt: result.StartedAt.UnixNano()}
}

func codexProbeBody(model string, input any, tools []any) []byte {
	body := map[string]any{"model": model, "input": input, "instructions": "Follow the diagnostic request exactly.", "stream": true, "store": false, "reasoning": map[string]string{"effort": "low"}}
	if tools != nil {
		body["tools"] = tools
	}
	raw, _ := json.Marshal(body)
	return raw
}

type codexProbeStep struct {
	outcome, code, message string
	status, reported       int
	response               gjson.Result
	attempted              bool
}

func applyCodexProbeStep(result *CodexProbeResult, step codexProbeStep) {
	if step.attempted {
		result.Attempts++
	}
	result.Outcome, result.ErrorCode, result.Message = step.outcome, step.code, step.message
	result.HTTPStatus, result.ReportedStatus = step.status, step.reported
}

func executeCodexProbeStep(ctx context.Context, account *auth.Account, generation int64, proxyURL, session, model string, body []byte) (result codexProbeStep) {
	if ctx.Err() != nil {
		return codexProbeStep{outcome: "canceled", code: "probe_canceled", message: "Test canceled"}
	}
	if account.GetCredentialGeneration() != generation {
		return codexProbeStep{outcome: "skipped", code: "credentials_changed", message: "Credentials changed during the test"}
	}
	if !basispointsActiveForModel(model) {
		return codexProbeStep{outcome: "blocked", code: "basispoints_disabled_or_model_not_allowed", message: "BPS is disabled or this exact model is not allowed"}
	}
	if err := account.ReloadCodexRoutes(ctx); err != nil {
		return codexProbeStep{outcome: "blocked", code: "configuration_unavailable", message: "Routing configuration is unavailable"}
	}
	path := account.CodexPathSnapshot(database.CodexPathBasispoints, model, time.Now())
	if !account.IsAvailable() || account.IsModelRateLimited(model) || !path.Allowed {
		return codexProbeStep{outcome: "blocked", code: "account_or_route_unavailable", message: "Account or route permissions changed during the test"}
	}
	if path.Health == "cooldown" || path.Health == "recovering" {
		return codexProbeStep{outcome: "blocked", code: "path_cooldown", message: "BPS path is cooling down or recovering"}
	}
	defer func() { result.attempted = true }()
	d := &CodexRouteDecision{RequestedModel: model, EffectiveModel: model, Policy: database.CodexRouteBasispointsOnly, Paths: []string{database.CodexPathBasispoints}, Preferred: database.CodexPathBasispoints, NoSwitch: true, client: ctx}
	a := &codexRouteAttemptState{decision: d, account: account, path: database.CodexPathBasispoints, model: model, started: time.Now(), release: func() {}}
	ctx = context.WithValue(context.WithValue(ctx, codexRouteKey{}, d), codexAttemptKey{}, a)
	resp, err := executeBasispointsRequest(ctx, account, body, session, proxyURL, "", http.Header{"Session-Id": []string{session}})
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return codexProbeStep{outcome: "canceled", code: "probe_canceled", message: "Test canceled"}
		}
		var local *Error
		if errors.As(err, &local) && local.Code == ErrorCodeBasispointsInvalidRequest {
			return codexProbeStep{outcome: "protocol_error", code: local.Code, message: "BPS request preparation failed"}
		}
		return codexProbeStep{outcome: "network_error", code: "upstream_transport_error", message: "BPS transport failed"}
	}
	if resp == nil || resp.Body == nil {
		return codexProbeStep{outcome: "protocol_error", code: "empty_response", message: "BPS returned no response"}
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("[CodexProbe] response close failed account=%d", account.ID())
		}
	}()
	if a.failure != nil {
		return codexProbeFailure(*a.failure)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return codexProbeFailure(codexRouteFailure{HTTPStatus: resp.StatusCode})
	}
	step := consumeCodexProbeSSE(resp.Body, resp.StatusCode)
	if reported := step.response.Get("model").String(); step.outcome == "supported" && reported != "" && reported != model {
		step.outcome, step.code, step.message = "protocol_error", "response_model_mismatch", "BPS completed a different model than the requested exact model"
	}
	if ctx.Err() != nil {
		step.outcome, step.code, step.message = "canceled", "probe_canceled", "Test canceled"
	}
	return step
}

func codexProbeFailure(f codexRouteFailure) codexProbeStep {
	s := codexProbeStep{outcome: "blocked", code: f.Code, status: f.HTTPStatus, reported: f.ReportedStatus, message: "BPS access was rejected; existing capability evidence is preserved"}
	switch {
	case f.Code == "deactivated_workspace":
		s.outcome, s.message = "workspace_deactivated", "Workspace is deactivated"
	case f.Category == "authentication" || f.HTTPStatus == 401:
		s.outcome, s.message = "unauthorized", "BPS authentication failed"
	case f.Category == "rate_limit" || f.HTTPStatus == 429:
		s.outcome, s.message = "rate_limited", "BPS is rate limited"
	case f.Category == "model_access":
		s.outcome, s.message = "unsupported", "BPS explicitly denied access to this exact model"
	case f.Category == "protocol" || f.Category == "encrypted_content":
		s.outcome, s.message = "protocol_error", "BPS protocol or encryption validation failed"
	case f.HTTPStatus >= 500 || f.Category == "network_or_waf":
		s.outcome, s.message = "network_error", "BPS upstream or network failed"
	}
	return s
}

func consumeCodexProbeSSE(reader io.Reader, status int) codexProbeStep {
	fail := func(code, message string) codexProbeStep {
		return codexProbeStep{outcome: "protocol_error", code: code, message: message, status: status}
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var data strings.Builder
	consume := func() (codexProbeStep, bool) {
		payload := strings.TrimSpace(data.String())
		data.Reset()
		if payload == "" || payload == "[DONE]" {
			return codexProbeStep{}, false
		}
		if !gjson.Valid(payload) {
			return fail("invalid_sse_json", "BPS emitted invalid JSON"), true
		}
		typ := gjson.Get(payload, "type").String()
		if typ == "response.failed" || typ == "error" {
			f := classifyCodexRouteFailure(status, "upstream_sse", []byte(payload))
			// These events have already passed the bridge. Only raw prefix
			// inspection is permitted to establish a permanent model denial.
			if f.Category == "model_access" {
				f.Category = "protocol"
			}
			return codexProbeFailure(f), true
		}
		if typ == "response.incomplete" {
			return fail("incomplete_response", "BPS response was incomplete"), true
		}
		if typ != "response.completed" {
			return codexProbeStep{}, false
		}
		response := gjson.Get(payload, "response")
		if response.Get("status").String() != "completed" || codexCompletedHasError(response) || codexCompletedHasError(gjson.Parse(payload)) || !response.Get("output").IsArray() {
			return fail("invalid_terminal_response", "BPS completed event contains failed, incomplete or invalid response data"), true
		}
		return codexProbeStep{outcome: "supported", status: status, response: response, message: "BPS completed the exact-model request successfully"}, true
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if step, done := consume(); done {
				return step
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data.WriteString(strings.TrimSpace(line[5:]))
			data.WriteByte('\n')
			if data.Len() > 1<<20 {
				return fail("response_too_large", "BPS diagnostic response exceeded its size limit")
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return codexProbeStep{outcome: "network_error", code: "stream_interrupted", message: "BPS stream ended before successful completion", status: status}
	}
	if step, done := consume(); done {
		return step
	}
	return codexProbeStep{outcome: "network_error", code: "missing_terminal_response", message: "BPS stream ended without a complete response", status: status}
}

func codexCompletedHasError(response gjson.Result) bool {
	for _, path := range []string{"error", "status_details.error", "incomplete_details"} {
		v := response.Get(path)
		if v.Exists() && v.Type != gjson.Null {
			return true
		}
	}
	return false
}

func codexProbeTextMatches(response gjson.Result, nonce string) bool {
	var text strings.Builder
	for _, output := range response.Get("output").Array() {
		if output.Get("type").String() == "function_call" || output.Get("type").String() == "custom_tool_call" {
			return false
		}
		for _, content := range output.Get("content").Array() {
			if content.Get("type").String() == "output_text" {
				text.WriteString(content.Get("text").String())
			}
		}
	}
	return strings.TrimSpace(text.String()) == nonce
}

func codexProbeValidatedCall(response gjson.Result, nonce string) (gjson.Result, bool) {
	var call gjson.Result
	for _, output := range response.Get("output").Array() {
		if output.Get("type").String() == "custom_tool_call" {
			return call, false
		}
		if output.Get("type").String() != "function_call" {
			continue
		}
		if call.Exists() {
			return call, false
		}
		call = output
	}
	marker := call.Get("encrypted_function_args")
	args := call.Get("arguments").String()
	return call, call.Get("name").String() == codexProbeTool && call.Get("call_id").String() != "" && marker.IsArray() && len(marker.Array()) == 0 && gjson.Valid(args) && gjson.Get(args, "nonce").String() == nonce
}
