package statepool

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/codex2api/auth"
	"github.com/google/uuid"
)

func (m *Manager) acquire(ctx context.Context, job Job) (*auth.Account, string, string) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		account := m.store.FindByID(job.AccountID)
		if account == nil || !account.IsAvailable() || account.ModelCooldownRemaining(job.Model) > 0 {
			return nil, "", "account_unavailable_or_cooling_down"
		}
		proxyURL := m.store.ResolveProxyForAccount(account)
		identity, err := Snapshot(account, proxyURL)
		if err != nil || identity != job.Identity {
			return nil, "", "account_identity_changed"
		}
		acquired := m.store.TakePreferredAccountWithFilter(job.AccountID, 0, nil,
			m.store.WithModelCooldownFilter(job.Model, nil))
		if acquired != nil {
			return acquired, proxyURL, ""
		}
		select {
		case <-ctx.Done():
			return nil, "", "cancelled"
		case <-ticker.C:
		}
	}
}

type terminalResponse struct {
	Status string `json:"status"`
	Model  string `json:"model"`
	Error  struct {
		Code string `json:"code"`
		Type string `json:"type"`
	} `json:"error"`
	Output []struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
	Usage struct {
		Input  int64 `json:"input_tokens"`
		Output int64 `json:"output_tokens"`
	} `json:"usage"`
}

func parseRetryAt(header string, now time.Time) int64 {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(header), 10, 64); err == nil && seconds > 0 && seconds < 1<<40 {
		return now.Unix() + seconds
	}
	if date, err := http.ParseTime(header); err == nil && date.After(now) {
		return date.Unix()
	}
	return now.Add(15 * time.Second).Unix()
}

func errorCode(body []byte) string {
	var data struct {
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
		Detail struct {
			Code string `json:"code"`
		} `json:"detail"`
	}
	_ = json.Unmarshal(body, &data)
	return safeCode(firstError(data.Error.Code, firstError(data.Error.Type, data.Detail.Code)))
}

func safeCode(value string) string {
	if len(value) > 100 {
		return "upstream_error"
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-') {
			return "upstream_error"
		}
	}
	return value
}

func (m *Manager) call(ctx context.Context, job Job, phase, state, prompt string) (result Check, captured string, capturedAt int64) {
	start := m.now()
	result.Phase = phase
	defer func() { result.DurationMS = m.now().Sub(start).Milliseconds() }()
	account, proxyURL, failure := m.acquire(ctx, job)
	if failure != "" {
		result.Error = failure
		result.RetryAt = m.retryAt(m.store.FindByID(job.AccountID), job.Model)
		return
	}
	defer m.store.Release(account)
	ref, routeErr := m.businessProxyRef(ctx, proxyURL)
	if routeErr != nil {
		result.Error = "proxy_lookup_failed"
		return
	}
	if phase == "capture" {
		var err error
		proxyURL, ref, err = m.resolveCaptureProxy(ctx, job.CaptureProxy, proxyURL)
		if err != nil {
			result.Error = err.Error()
			return
		}
	}
	result.ProxyID, result.ProxyName, result.LastTestIP = ref.ID, ref.Name, ref.LastTestIP
	result.SessionID = ref.SessionID
	routeKey := routeReservationKey(ref, proxyURL)
	forwardURL, forwardKey := "", ""
	if phase == "capture" && job.ForwardProxy.ID != 0 {
		var forwardRef ProxyRef
		var err error
		forwardURL, forwardRef, err = m.resolveCaptureProxy(ctx, job.ForwardProxy, "")
		if err != nil {
			result.Error = "forward_proxy_changed_or_unavailable"
			return
		}
		forwardKey = routeReservationKey(forwardRef, forwardURL)
		result.ForwardProxyName = forwardRef.Name
		ctx = context.WithValue(ctx, forwardProxyKey{}, forwardURL)
	}
	if err := m.reserveRoute(ctx, job, routeKey, forwardKey); err != nil {
		result.Error = "route_reservation_cancelled"
		return
	}
	defer func() { _ = m.db.ReleaseStatePoolRoute(context.Background(), job.ID, m.owner) }()
	// An account may enter cooldown while waiting for another proxy request.
	if !account.IsAvailable() || account.ModelCooldownRemaining(job.Model) > 0 {
		result.Error, result.RetryAt = "account_unavailable_or_cooling_down", m.retryAt(account, job.Model)
		return
	}
	current, identityErr := Snapshot(account, m.store.ResolveProxyForAccount(account))
	if identityErr != nil || current != job.Identity {
		result.Error = "account_identity_changed"
		return
	}
	if phase == "capture" && job.CaptureProxy.ID != 0 {
		resolved, _, err := m.resolveCaptureProxy(ctx, job.CaptureProxy, proxyURL)
		if err != nil || resolved != proxyURL {
			result.Error = "capture_proxy_changed_or_unavailable"
			return
		}
	}
	if forwardURL != "" {
		resolved, _, err := m.resolveCaptureProxy(ctx, job.ForwardProxy, "")
		if err != nil || resolved != forwardURL {
			result.Error = "forward_proxy_changed_or_unavailable"
			return
		}
	}
	defer func() {
		if result.HTTPStatus == 401 || result.HTTPStatus == 403 || result.HTTPStatus == 429 || result.RetryAt > m.now().Unix() ||
			result.Error == "rate_limit_exceeded" || result.Error == "usage_limit_reached" || result.Error == "token_revoked" || result.Error == "deactivated_workspace" {
			_ = m.db.HaltStatePoolAccount(context.Background(), job.AccountID, job.ID)
			m.cancelStopped(context.Background())
		}
	}()
	body := map[string]any{"model": job.Model, "instructions": "", "store": false, "stream": true,
		"input":     []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": prompt}}}},
		"reasoning": map[string]string{"effort": job.Effort, "summary": "auto"}, "tools": []any{}, "prompt_cache_key": uuid.NewString()}
	headers := http.Header{}
	if state != "" {
		headers.Set(Header, state)
		body["client_metadata"] = map[string]string{"x-codex-turn-state": state}
	}
	encoded, _ := json.Marshal(body)
	response, err := m.execute(ctx, account, encoded, proxyURL, headers)
	if err != nil {
		result.Error = transportFailure(err)
		if ctx.Err() != nil {
			result.Error = "cancelled"
		}
		return
	}
	defer func() { _ = response.Body.Close() }()
	result.HTTPStatus = response.StatusCode
	captured = response.Header.Get(Header)
	if captured != "" {
		capturedAt = m.now().Unix()
	}
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		result.Error = fmt.Sprintf("HTTP %d: %s", response.StatusCode, firstError(errorCode(data), "upstream_error"))
		if response.StatusCode == http.StatusTooManyRequests {
			result.RetryAt = parseRetryAt(response.Header.Get("Retry-After"), m.now())
		}
		if m.failure != nil {
			m.failure(account, job.Model, response, data)
		}
		result.RetryAt = max(result.RetryAt, m.retryAt(account, job.Model))
		return
	}
	scanner := bufio.NewScanner(io.LimitReader(response.Body, 4<<20))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var dataLines []string
	var output strings.Builder
	consume := func() bool {
		data := strings.Join(dataLines, "\n")
		dataLines = nil
		if data == "[DONE]" {
			return true
		}
		var event struct {
			Type     string            `json:"type"`
			Delta    string            `json:"delta"`
			Headers  map[string]string `json:"headers"`
			Response terminalResponse  `json:"response"`
			Error    struct {
				Code string `json:"code"`
				Type string `json:"type"`
			} `json:"error"`
			Code string `json:"code"`
		}
		if json.Unmarshal([]byte(data), &event) != nil {
			result.Error = "invalid_stream_event"
			return true
		}
		if captured == "" {
			for name, value := range event.Headers {
				if strings.EqualFold(name, Header) && value != "" {
					captured, capturedAt = value, m.now().Unix()
				}
			}
		}
		if event.Type == "response.output_text.delta" {
			output.WriteString(event.Delta)
			if output.Len() > 128<<10 {
				result.Error = "answer_too_large"
				return true
			}
		}
		switch event.Type {
		case "response.completed", "response.done", "response.failed", "response.incomplete", "error":
			result.Terminal, result.Model = event.Type, event.Response.Model
			result.InputTokens, result.OutputTokens = event.Response.Usage.Input, event.Response.Usage.Output
			code := firstError(event.Error.Code, firstError(event.Error.Type, firstError(event.Response.Error.Code, firstError(event.Response.Error.Type, event.Code))))
			if code != "" || event.Type == "error" || event.Type == "response.failed" || event.Type == "response.incomplete" {
				result.Error = firstError(safeCode(code), event.Type)
				if m.failure != nil {
					m.failure(account, job.Model, response, []byte(data))
				}
				result.RetryAt = m.retryAt(account, job.Model)
				return true
			}
			if event.Response.Status != "" && event.Response.Status != "completed" {
				result.Error = "response_not_completed"
			}
			if result.Model != job.Model {
				result.Error = "response_model_mismatch"
			}
			if output.Len() == 0 {
				for _, item := range event.Response.Output {
					for _, content := range item.Content {
						if content.Type == "output_text" {
							output.WriteString(content.Text)
						}
					}
				}
			}
			return true
		}
		return false
	}
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		} else if line == "" && len(dataLines) > 0 && consume() {
			break
		}
	}
	if result.Terminal == "" && result.Error == "" && len(dataLines) > 0 {
		consume()
	}
	result.Answer = output.String()
	// Answers are untrusted upstream text; never persist accidentally echoed secrets.
	for _, secret := range []string{account.GetAccessToken(), state, captured} {
		if secret != "" {
			result.Answer = strings.ReplaceAll(result.Answer, secret, "[REDACTED]")
		}
	}
	if result.Error == "" {
		if scanner.Err() != nil || result.Terminal != "response.completed" && result.Terminal != "response.done" {
			result.Error = "incomplete_stream"
		} else {
			result.Error = GradeFailure(result.Answer, phase == "verify")
			result.Passed = result.Error == ""
		}
	}
	return
}

func (m *Manager) retryAt(account *auth.Account, model string) int64 {
	if account == nil {
		return 0
	}
	_, until := account.GetCooldownSnapshot()
	if remaining := account.ModelCooldownRemaining(model); remaining > 0 {
		if modelUntil := m.now().Add(remaining); modelUntil.After(until) {
			until = modelUntil
		}
	}
	if until.After(m.now()) {
		return until.Unix()
	}
	return 0
}
