package proxy

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/codex2api/database"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const codexRoutePrefixLimit = 64 * 1024
const codexRouteObservationLimit = 8 * 1024 * 1024
const codexUsageRejectedCode = "codex_upstream_usage_rejected"
const codexAmbiguousUsageMessage = "403: This request was blocked by our usage policy."

type codexRouteFailure struct {
	HTTPStatus     int
	ReportedStatus int
	Code           string
	Source         string
	Category       string
	Switch         bool
}

// Only trusted raw executor responses enter this classifier. Message-reported
// status never overwrites HTTP status; free text never proves entitlement.
func classifyCodexRouteFailure(status int, source string, body []byte) codexRouteFailure {
	f := codexRouteFailure{HTTPStatus: status, Source: source, Category: "unclassified"}
	if source != "upstream_http" && source != "upstream_sse" && source != "upstream_websocket" {
		f.Category = "local_or_translated"
		return f
	}
	if !gjson.ValidBytes(body) {
		f.Category = "network_or_waf"
		return f
	}
	code := firstGJSONString(body, "error.code", "response.error.code", "response.status_details.error.code")
	typ := firstGJSONString(body, "error.type", "response.error.type", "response.status_details.error.type")
	message := firstGJSONString(body, "error.message", "response.error.message", "response.status_details.error.message")
	if len(code) <= 80 && strings.IndexFunc(code, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-')
	}) < 0 {
		f.Code = code
	}
	if isExplicitUpstreamCyberPolicy(body) || isExplicitUpstreamSafetyPolicy(body) {
		f.Category = "explicit_safety_policy"
		return f
	}
	switch {
	case status == 401 || code == "invalid_api_key" || code == "token_expired":
		f.Category = "authentication"
	case isPermanentQuotaFailure(body) || status == 402:
		f.Category = "billing"
	case status == 429 || code == "rate_limit_exceeded":
		f.Category = "rate_limit"
	case status >= 500:
		f.Category = "transient_upstream"
	case code == "basispoints_protocol_error":
		f.Category = "protocol"
	case code == "invalid_encrypted_content":
		f.Category = "encrypted_content"
	case code == "basispoints_model_access_changed" || code == "model_access_denied":
		f.Category = "model_access"
		f.Switch = true
	case code == "codex_access_restricted" || code == "upstream_access_denied":
		f.Category = "upstream_access"
		f.Switch = true
	case codexNullErrorCode(body) && typ == "server_error" && message == codexAmbiguousUsageMessage && (status == 403 || status == 200 && source != "upstream_http"):
		f.Category = "ambiguous_usage_rejection"
		f.ReportedStatus = 403
		f.Switch = true
	}
	// Preserve failures with usage or output for existing accounting.
	if len(gjson.GetBytes(body, "response.output").Array()) > 0 || len(gjson.GetBytes(body, "output").Array()) > 0 || codexFailureHasUsage(body) {
		f.Switch = false
	}
	return f
}

func codexNullErrorCode(body []byte) bool {
	for _, field := range []string{"error.code", "response.error.code", "response.status_details.error.code"} {
		value := gjson.GetBytes(body, field)
		if value.Exists() {
			return value.Type == gjson.Null
		}
	}
	return false
}

func codexFailureHasUsage(body []byte) bool {
	for _, prefix := range []string{"usage.", "response.usage."} {
		for _, field := range []string{"total_tokens", "input_tokens", "output_tokens", "prompt_tokens", "completion_tokens"} {
			if gjson.GetBytes(body, prefix+field).Int() > 0 {
				return true
			}
		}
	}
	return false
}

type codexPrefixBody struct {
	io.Reader
	closer io.Closer
}

func (b *codexPrefixBody) Close() error { return b.closer.Close() }

func routeNormalizedFailure(payload []byte, f codexRouteFailure) []byte {
	if f.Category != "ambiguous_usage_rejection" && f.Category != "upstream_access" && f.Category != "model_access" && !(f.Category == "unclassified" && f.HTTPStatus == 403) {
		return payload
	}
	field := "error.code"
	if gjson.GetBytes(payload, "response.error").Exists() {
		field = "response.error.code"
	}
	if gjson.GetBytes(payload, "response.status_details.error").Exists() {
		field = "response.status_details.error.code"
	}
	code := codexUsageRejectedCode
	if f.Category == "model_access" {
		code = "basispoints_model_access_changed"
	}
	if f.Category == "unclassified" {
		code = "codex_path_temporarily_unavailable"
	}
	payload, _ = sjson.SetBytes(payload, field, code)
	return payload
}

// Inspect a prefix of at most 128 KiB / 16 events (one 64 KiB line of lookahead). Any output, tool or unknown event ends
// inspection immediately. BPS invokes this before its bridge hides tool frames.
func inspectCodexRouteResponse(ctx context.Context, resp *http.Response) {
	a := codexAttemptFromContext(ctx)
	if a == nil || a.inspected || resp == nil || resp.Body == nil {
		return
	}
	a.inspected = true
	original := resp.Body
	if resp.StatusCode >= 400 {
		prefix, err := io.ReadAll(io.LimitReader(original, codexRoutePrefixLimit+1))
		if err == nil && len(prefix) <= codexRoutePrefixLimit {
			f := classifyCodexRouteFailure(resp.StatusCode, "upstream_http", prefix)
			a.failure = &f
			prefix = routeNormalizedFailure(prefix, f)
		}
		resp.Body = &codexPrefixBody{Reader: replayCodexPrefix(prefix, err, original), closer: original}
		resp.ContentLength = -1
		resp.Header.Del("Content-Length")
		return
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "event-stream") && a.path != database.CodexPathBasispoints && !a.websocket {
		return
	}
	reader := bufio.NewReaderSize(original, codexRoutePrefixLimit)
	var prefix bytes.Buffer
	var event bytes.Buffer
	eventStart := 0
	events := 0
	var readErr error
	for prefix.Len() < codexRoutePrefixLimit && events < 16 {
		line, err := reader.ReadSlice(10)
		prefix.Write(line)
		event.Write(line)
		if err != nil {
			if err != bufio.ErrBufferFull {
				readErr = err
			} else {
				a.decision.commit()
			}
			break
		}
		if len(bytes.TrimSpace(line)) != 0 {
			continue
		}
		events++
		payload := codexSSEPayload(event.Bytes())
		event.Reset()
		if len(payload) == 0 {
			eventStart = prefix.Len()
			continue
		}
		kind := gjson.GetBytes(payload, "type").String()
		if kind == "response.created" || kind == "response.in_progress" {
			if len(gjson.GetBytes(payload, "response.output").Array()) == 0 {
				eventStart = prefix.Len()
				continue
			}
		}
		if kind == "response.failed" || kind == "error" {
			source := "upstream_sse"
			if a.websocket {
				source = "upstream_websocket"
			}
			f := classifyCodexRouteFailure(resp.StatusCode, source, payload)
			a.failure = &f
			normalized := routeNormalizedFailure(payload, f)
			if !bytes.Equal(payload, normalized) {
				raw := append([]byte(nil), prefix.Bytes()[:eventStart]...)
				prefix.Reset()
				prefix.Write(raw)
				prefix.WriteString("data: ")
				prefix.Write(normalized)
				prefix.WriteString("\n\n")
			}
		} else {
			a.decision.commit()
		}
		break
	}
	if prefix.Len() >= codexRoutePrefixLimit || events >= 16 {
		a.decision.commit()
	}
	resp.Body = &codexPrefixBody{Reader: replayCodexPrefix(prefix.Bytes(), readErr, reader), closer: original}
	resp.ContentLength = -1
	resp.Header.Del("Content-Length")
}

// Prefix inspection must replay transport errors, including WebSocket close codes.
// Swallowing them would silently disable the existing HTTP recovery mechanism.
type codexReadError struct{ err error }

func (r *codexReadError) Read([]byte) (int, error) {
	if r.err == nil {
		return 0, io.EOF
	}
	err := r.err
	r.err = nil
	return 0, err
}

func replayCodexPrefix(prefix []byte, err error, rest io.Reader) io.Reader {
	if err != nil && err != io.EOF {
		return io.MultiReader(bytes.NewReader(prefix), &codexReadError{err: err}, rest)
	}
	return io.MultiReader(bytes.NewReader(prefix), rest)
}

func codexSSEPayload(event []byte) []byte {
	var data []byte
	for _, line := range bytes.Split(event, []byte{10}) {
		if bytes.HasPrefix(line, []byte("data:")) {
			if len(data) > 0 {
				data = append(data, 10)
			}
			data = append(data, bytes.TrimSpace(line[5:])...)
		}
	}
	return data
}

type codexObservedBody struct {
	io.ReadCloser
	attempt   *codexRouteAttemptState
	pending   []byte
	json      bool
	oversized bool
	failed    bool
	success   sync.Once
	history   map[string]bool
}

func observeCodexRouteBody(body io.ReadCloser, a *codexRouteAttemptState, contentType string) io.ReadCloser {
	return &codexObservedBody{ReadCloser: body, attempt: a, json: strings.Contains(contentType, "application/json"), history: make(map[string]bool)}
}

func (b *codexObservedBody) observe(payload []byte) {
	kind := gjson.GetBytes(payload, "type").String()
	response := gjson.GetBytes(payload, "response")
	status := response.Get("status").String()
	if kind == "response.failed" || kind == "response.incomplete" || kind == "error" || status != "" && status != "completed" && kind == "response.completed" || codexCompletedHasError(response) || codexCompletedHasError(gjson.ParseBytes(payload)) {
		b.failed = true
	}
	if kind != "" && kind != "response.created" && kind != "response.in_progress" && kind != "response.failed" && kind != "error" {
		b.attempt.decision.commit()
	}
	if !b.failed && (kind == "response.output_item.done" || kind == "response.completed" || b.json && gjson.GetBytes(payload, "object").String() == "response.compaction") {
		b.attempt.recordHistoryRoute(payload, b.history)
	}
	if !b.failed && (kind == "response.completed" && response.IsObject() || b.json && gjson.GetBytes(payload, "object").String() == "response.compaction" && !codexCompletedHasError(gjson.ParseBytes(payload))) {
		b.success.Do(func() {
			a := b.attempt
			a.account.ObserveCodexPath(context.WithoutCancel(a.decision.client), database.CodexCapability{Upstream: a.path, Model: a.model, Capability: database.CapabilitySupported, Source: "upstream_completed", Reason: "success", ObservedAt: a.started.UnixNano(), CredentialGeneration: a.generation})
		})
	}
}

func (b *codexObservedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if !b.oversized {
		b.pending = append(b.pending, p[:n]...)
		if b.json {
			if len(b.pending) > codexRouteObservationLimit {
				b.pending = nil
				b.oversized = true
			} else if err == io.EOF {
				b.observe(b.pending)
			}
		} else {
			for {
				index := bytes.IndexByte(b.pending, 10)
				if index < 0 {
					break
				}
				line := b.pending[:index]
				if len(line) <= codexRouteObservationLimit && bytes.HasPrefix(line, []byte("data:")) {
					b.observe(bytes.TrimSpace(line[5:]))
				}
				b.pending = b.pending[index+1:]
			}
			if len(b.pending) > codexRouteObservationLimit {
				b.pending = nil
				b.oversized = true
				b.attempt.decision.commit()
			}
		}
	}
	if err != nil {
		b.attempt.release()
	}
	return n, err
}

func (b *codexObservedBody) Close() error { b.attempt.release(); return b.ReadCloser.Close() }
