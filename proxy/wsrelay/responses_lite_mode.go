package wsrelay

import (
	"context"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

const websocketResponsesLiteMetadataPath = "client_metadata.ws_request_header_x_openai_internal_codex_responses_lite"

type websocketResponsesLiteContextKey struct{}

// The native executor has already applied model/account gates and moved the
// effective HTTP Lite signal to this final WS frame. Do not infer mode from the
// model name or an earlier ingress header: either can disagree after mapping.
func withWebsocketResponsesLiteMode(ctx context.Context, body []byte) context.Context {
	lite := strings.EqualFold(strings.TrimSpace(gjson.GetBytes(body, websocketResponsesLiteMetadataPath).String()), "true")
	return context.WithValue(ctx, websocketResponsesLiteContextKey{}, lite)
}

func websocketResponsesLiteFromContext(ctx context.Context) bool {
	lite, _ := ctx.Value(websocketResponsesLiteContextKey{}).(bool)
	return lite
}

// Lite is a connection-level compatibility boundary even though its request
// signal lives on each frame. Once routed to rustponsesapi, an upstream socket
// may stay there when the next frame omits the marker. Include the mode in BOTH
// pool selection and immutable dial identity (also checked by continuations).
// Keep existing standard-mode identities/keys unchanged. This scope never goes
// into an upstream header, prompt cache key, thread ID or conversation payload.
func websocketTransportIdentity(headers http.Header, lite bool) string {
	identity := websocketClientIdentity(headers)
	if lite {
		identity += "|responses:lite"
	}
	return identity
}

func websocketTransportPoolKey(key string, headers http.Header, lite bool) string {
	if key == "" {
		return key
	}
	return key + "|client:" + websocketTransportIdentity(headers, lite)
}
