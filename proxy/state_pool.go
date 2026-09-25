package proxy

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/codex2api/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type StatePoolResolver func(*auth.Account, string, string, string, string) (string, error)
type StatePoolClaim func(*auth.Account, string, string, string) bool
type statePoolProvider struct {
	resolve StatePoolResolver
	claims  StatePoolClaim
}
type statePoolBypassKey struct{}

var verifiedStateProvider atomic.Pointer[statePoolProvider]

func SetStatePoolResolver(resolve StatePoolResolver, claims ...StatePoolClaim) {
	if resolve == nil {
		verifiedStateProvider.Store(nil)
	} else {
		provider := &statePoolProvider{resolve: resolve}
		if len(claims) > 0 {
			provider.claims = claims[0]
		}
		verifiedStateProvider.Store(provider)
	}
}

func WithoutStatePool(ctx context.Context) context.Context {
	return context.WithValue(ctx, statePoolBypassKey{}, true)
}

func applyVerifiedState(ctx context.Context, account *auth.Account, body []byte, headers http.Header, proxyURL string) ([]byte, http.Header, error) {
	if updated, outgoing, active, err := applyIPv6State(ctx, account, body, headers); active || err != nil {
		return updated, outgoing, err
	}
	provider := verifiedStateProvider.Load()
	if provider == nil || ctx.Value(statePoolBypassKey{}) == true || account == nil || account.IsRelayStyle() ||
		responsesBodyRequestsImageGeneration(body) || gjson.GetBytes(body, "compact").Bool() {
		return body, headers, nil
	}
	if proxyURL == "" {
		account.Mu().RLock()
		proxyURL = account.ProxyURL
		account.Mu().RUnlock()
	}
	model := gjson.GetBytes(body, "model").String()
	effort := strings.ToLower(gjson.GetBytes(body, "reasoning.effort").String())
	state, err := provider.resolve(account, model, effort, proxyURL, codexTurnContinuationToken(headers, body))
	if err != nil {
		return body, headers, &Error{Code: err.Error(), Message: "Validated state unavailable for this account and model; recapture or disable strict state reuse.",
			Type: ErrorTypeServerError, HTTPStatus: http.StatusServiceUnavailable, Retryable: false}
	}
	if state == "" {
		return body, headers, nil
	}
	updated, err := sjson.SetBytes(body, "client_metadata.x-codex-turn-state", state)
	if err != nil {
		return body, headers, ErrInternalError("Unable to apply validated state", err)
	}
	cloned := headers.Clone()
	if cloned == nil {
		cloned = http.Header{}
	}
	cloned.Set(codexTurnStateHeader, state)
	return updated, cloned, nil
}
