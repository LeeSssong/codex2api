package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"

	"github.com/codex2api/auth"
	"github.com/codex2api/ipv6state"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const ipv6StateDirectRoute = "direct://ipv6-state"

type IPv6StateProvider struct {
	EligibleAccounts func(string) (bool, map[int64]bool)
	Resolve          func(*auth.Account, string) (string, bool, error)
	Applies          func(*auth.Account, string) bool
	Guard            func(*auth.Account, string, string) error
}

var ipv6StateProvider atomic.Pointer[IPv6StateProvider]

func SetIPv6StateProvider(provider *IPv6StateProvider) { ipv6StateProvider.Store(provider) }

func withRequiredStateFilter(model string, filter auth.AccountFilter) auth.AccountFilter {
	provider := ipv6StateProvider.Load()
	if provider == nil || provider.EligibleAccounts == nil {
		return filter
	}
	strict, allowed := provider.EligibleAccounts(model)
	if !strict {
		return filter
	}
	return func(account *auth.Account) bool {
		return (!ipv6state.NativeAccount(account) || allowed[account.ID()]) && (filter == nil || filter(account))
	}
}

func ExecuteStateHeaderProbe(ctx context.Context, account *auth.Account, model string, route ipv6state.Route) (*http.Response, error) {
	if route.SourceIP != "" {
		return ExecuteIPv6StateProbe(ctx, account, model, route.SourceIP)
	}
	if route.ProxyURL == "" || account == nil || account.IsRelayStyle() || account.IsCodexAgentIdentity() {
		return nil, errors.New("invalid state capture route or identity")
	}
	body, err := stateHeaderProbeBody(model)
	if err != nil {
		return nil, err
	}
	account.Mu().RLock()
	token := account.AccessToken
	account.Mu().RUnlock()
	if token == "" {
		return nil, errors.New("missing OAuth credential")
	}
	ctx = WithFreshStateProbe(ctx, route.ForwardURL)
	client, finish, err := stateProbeClient(ctx, CodexEgress{Kind: CodexEgressProxy, DialProxyURL: route.ProxyURL})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, CodexBaseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		finish(nil)
		return nil, err
	}
	applyCodexRequestHeaders(req, account, token, "", "", nil, nil)
	req.Header.Del(codexTurnStateHeader)
	response, err := client.Do(req)
	finish(response)
	return response, err
}

func stateHeaderProbeBody(model string) ([]byte, error) {
	return json.Marshal(map[string]any{
		"model": model, "instructions": "", "store": false, "stream": true,
		"reasoning": map[string]string{"effort": "high"},
		"input":     []any{map[string]any{"role": "user", "content": []any{map[string]string{"type": "input_text", "text": "."}}}},
	})
}

func ipv6StateDirect(ctx context.Context, account *auth.Account, body []byte) bool {
	provider := ipv6StateProvider.Load()
	return provider != nil && ctx.Value(statePoolBypassKey{}) != true && provider.Applies(account, gjson.GetBytes(body, "model").String())
}

func applyIPv6State(ctx context.Context, account *auth.Account, body []byte, headers http.Header) ([]byte, http.Header, bool, error) {
	provider := ipv6StateProvider.Load()
	if provider == nil || ctx.Value(statePoolBypassKey{}) == true {
		return body, headers, false, nil
	}
	model := gjson.GetBytes(body, "model").String()
	if provider.Guard != nil {
		var incoming []string
		gjson.GetBytes(body, "client_metadata").ForEach(func(name, value gjson.Result) bool {
			if strings.EqualFold(name.String(), codexTurnStateHeader) {
				incoming = append(incoming, value.String())
			}
			return true
		})
		for name, values := range headers {
			if strings.EqualFold(name, codexTurnStateHeader) {
				incoming = append(incoming, values...)
			}
		}
		for _, value := range incoming {
			if err := provider.Guard(account, model, value); err != nil {
				return body, headers, true, &Error{Code: "state_account_or_model_mismatch", Message: "State belongs to another account or model", Type: ErrorTypeServerError, HTTPStatus: 409}
			}
		}
	}
	state, active, err := provider.Resolve(account, model)
	if !active {
		return body, headers, false, nil
	}
	if err != nil {
		if errors.Is(err, ipv6state.ErrStateRequired) {
			return body, headers, true, &Error{Code: "valid_state_required", Message: "A valid saved State is required for this account and exact model. Enable automatic state reuse and capture or import a matching State.", Type: ErrorTypeServerError, HTTPStatus: 503, Retryable: false}
		}
		return body, headers, true, &Error{Code: "ipv6_state_identity_unavailable", Message: "IPv6 state identity is unavailable", Type: ErrorTypeServerError, HTTPStatus: 503}
	}
	cloned := headers.Clone()
	if cloned == nil {
		cloned = http.Header{}
	}
	for name := range cloned {
		if strings.EqualFold(name, codexTurnStateHeader) {
			delete(cloned, name)
		}
	}
	metadata := gjson.GetBytes(body, "client_metadata")
	metadata.ForEach(func(name, value gjson.Result) bool {
		if strings.EqualFold(name.String(), codexTurnStateHeader) {
			body, err = sjson.DeleteBytes(body, "client_metadata."+name.String())
		}
		return err == nil
	})
	if err == nil && state != "" {
		body, err = sjson.SetBytes(body, "client_metadata.x-codex-turn-state", state)
		cloned.Set(codexTurnStateHeader, state)
	}
	if err != nil {
		return body, headers, true, ErrInternalError("Unable to apply IPv6 state", err)
	}
	return body, cloned, true, nil
}

// Collection uses a fresh IPv6 socket and never inherits account, global, Resin,
// or environment proxies. The caller cancels immediately upon receiving headers.
func ExecuteIPv6StateProbe(ctx context.Context, account *auth.Account, model, source string) (*http.Response, error) {
	ip, err := netip.ParseAddr(source)
	if err != nil || !ip.Is6() || ip.Is4In6() || account == nil || account.IsRelayStyle() || account.IsCodexAgentIdentity() {
		return nil, errors.New("invalid IPv6 probe identity or source")
	}
	account.Mu().RLock()
	token := account.AccessToken
	account.Mu().RUnlock()
	if token == "" {
		return nil, errors.New("missing OAuth credential")
	}
	body, err := stateHeaderProbeBody(model)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.IP(ip.AsSlice())}}
	transport := &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, address string) (net.Conn, error) {
		return dialer.DialContext(ctx, "tcp6", address)
	}}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, CodexBaseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	applyCodexRequestHeaders(req, account, token, "", "", nil, nil)
	req.Header.Del(codexTurnStateHeader)
	response, err := client.Do(req)
	if response != nil && response.Body != nil {
		response.Body = &stateProbeBody{ReadCloser: response.Body, release: transport.CloseIdleConnections}
	} else {
		transport.CloseIdleConnections()
	}
	return response, err
}
