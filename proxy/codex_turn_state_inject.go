package proxy

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/codex2api/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Credential-level injection runs after model and transport selection. HTTP
// headers and WebSocket frames share the same decision through the context.
// Managed state and probe requests take precedence over manual injection.

// codexTurnStateMetadataKey carries per-turn state on an existing WebSocket.
const codexTurnStateMetadataKey = "x-codex-turn-state"

type codexTurnStateInjectionKey struct{}
type codexClientModelKey struct{}

// WithCodexClientModel retains the original model for matching before aliases.
func WithCodexClientModel(ctx context.Context, model string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return ctx
	}
	return context.WithValue(ctx, codexClientModelKey{}, model)
}

func codexClientModelFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	model, _ := ctx.Value(codexClientModelKey{}).(string)
	return model
}

func withCodexTurnStateInjection(ctx context.Context, value string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, codexTurnStateInjectionKey{}, value)
}

// CodexTurnStateInjectionFromContext returns the selected outbound state.
func CodexTurnStateInjectionFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(codexTurnStateInjectionKey{}).(string)
	return value
}

// codexTurnStateInjectionDecision distinguishes an explicit empty managed
// selection from the absence of a selection (where observed thread state may win).
func codexTurnStateInjectionDecision(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	value, ok := ctx.Value(codexTurnStateInjectionKey{}).(string)
	return value, ok
}

// prepareCodexTurnStateInjection chooses managed state before configured state.
// The same selected value must reach both a new handshake and every WS frame.
func prepareCodexTurnStateInjection(ctx context.Context, account *auth.Account, requestBody []byte, headers http.Header, websocket bool) (context.Context, []byte, http.Header) {
	if account == nil {
		return ctx, requestBody, headers
	}
	upstreamModel := strings.TrimSpace(gjson.GetBytes(requestBody, "model").String())
	if ctx != nil && ctx.Value(statePoolBypassKey{}) == true {
		return ctx, requestBody, headers
	}
	selected := false
	injected := ""
	if provider := ipv6StateProvider.Load(); provider != nil && provider.Applies != nil && provider.Applies(account, upstreamModel) {
		injected, selected = headers.Get(codexTurnStateHeader), true
	} else if provider := verifiedStateProvider.Load(); provider != nil && provider.claims != nil && provider.claims(account, upstreamModel, gjson.GetBytes(requestBody, "reasoning.effort").String(), headers.Get(codexTurnStateHeader)) {
		injected, selected = headers.Get(codexTurnStateHeader), true
	} else {
		injected = account.CodexTurnStateInjection(codexClientModelFromContext(ctx), upstreamModel)
		selected = injected != ""
	}
	if !selected {
		return ctx, requestBody, headers
	}
	ctx = withCodexTurnStateInjection(ctx, injected)
	headers = headers.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	applyCodexTurnStateInjectionHeader(ctx, headers)
	if websocket {
		if injected == "" {
			requestBody, _ = sjson.DeleteBytes(requestBody, "client_metadata."+codexTurnStateMetadataKey)
		} else if updated, err := sjson.SetBytes(requestBody, "client_metadata."+codexTurnStateMetadataKey, injected); err == nil {
			requestBody = updated
		}
	}
	return ctx, requestBody, headers
}

// applyCodexTurnStateInjectionHeader applies the decision after custom headers.
func applyCodexTurnStateInjectionHeader(ctx context.Context, headers http.Header) {
	if headers == nil {
		return
	}
	if value, selected := codexTurnStateInjectionDecision(ctx); selected {
		if value == "" {
			headers.Del(codexTurnStateHeader)
		} else {
			headers.Set(codexTurnStateHeader, value)
		}
	}
}

// ApplyCodexTurnStateInjectionHeader exposes final header injection to wsrelay.
func ApplyCodexTurnStateInjectionHeader(ctx context.Context, headers http.Header) {
	applyCodexTurnStateInjectionHeader(ctx, headers)
}

// Oversized observed values are rejected rather than truncated.
const maxObservedCodexTurnStateBytes = 4096

// observedCodexTurnState accepts bounded single-line visible strings.
func observedCodexTurnState(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxObservedCodexTurnStateBytes || !utf8.ValidString(value) {
		return ""
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return value
}

var codexTurnStateFrameNeedles = [][]byte{[]byte("turn-state"), []byte("Turn-State")}

// codexTurnStateFromFrame searches known metadata locations after a cheap
// substring check, avoiding JSON parsing for unrelated WebSocket frames.
func codexTurnStateFromFrame(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	mentioned := false
	for _, needle := range codexTurnStateFrameNeedles {
		if bytes.Contains(payload, needle) {
			mentioned = true
			break
		}
	}
	if !mentioned {
		return ""
	}
	root := gjson.ParseBytes(payload)
	if !root.IsObject() {
		return ""
	}
	for _, path := range []string{"headers", "response.headers", "response.client_metadata", "client_metadata", "response.metadata", "metadata", "response", ""} {
		object := root
		if path != "" {
			object = root.Get(path)
		}
		if !object.IsObject() {
			continue
		}
		state := ""
		object.ForEach(func(key, value gjson.Result) bool {
			if strings.EqualFold(key.String(), codexTurnStateHeader) && value.Type == gjson.String {
				state = value.String()
				return false
			}
			return true
		})
		if state = observedCodexTurnState(state); state != "" {
			return state
		}
	}
	return ""
}

// ObserveCodexTurnStateFrame records and returns upstream state in the current trace.
func ObserveCodexTurnStateFrame(ctx context.Context, payload []byte) string {
	state := codexTurnStateFromFrame(payload)
	if state != "" {
		noteUpstreamTurnState(ctx, state)
		noteCodexThreadTurnStateFromContext(ctx, state)
	}
	if id := strings.TrimSpace(gjson.GetBytes(payload, "response.id").String()); id != "" {
		typ := gjson.GetBytes(payload, "type").String()
		if typ == "" || typ == "response.completed" || typ == "response.created" {
			noteCodexConversationResponseFromContext(ctx, id)
		}
	}
	return state
}
