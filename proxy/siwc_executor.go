package proxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"sync"

	"github.com/codex2api/auth"
	"github.com/codex2api/credentialops/siwc"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

type siwcResponseBody struct {
	io.ReadCloser
	release func()
	once    sync.Once
}

func (body *siwcResponseBody) Close() error {
	err := body.ReadCloser.Close()
	body.once.Do(body.release)
	return err
}

func executeSIWCRequest(ctx context.Context, account *auth.Account, body []byte) (*http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	payload, err := siwc.NormalizeRequest(body)
	if err != nil {
		return nil, routeLocalError("siwc_unsupported_request", err.Error())
	}
	tunnel, release, err := StartAccountEgressTunnel(account)
	if err != nil {
		return nil, routeLocalError("siwc_egress_unavailable", "Account authorization egress unavailable")
	}
	retained := false
	defer func() {
		if !retained {
			release()
		}
	}()
	client := auth.BuildUTLSHTTPClient(tunnel)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	grant, generation, err := resolveSIWCGrant(ctx, account, client)
	if err != nil {
		return nil, routeLocalError("siwc_authorization_required", err.Error())
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/responses", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+grant.AccessToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("User-Agent", "Codex2API")
	request.Header.Set("X-Client-Request-Id", uuid.NewString())
	if err := ConsumeAPIKeyModelRequestQuota(ctx, gjson.GetBytes(payload, "model").String()); err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, routeLocalError("siwc_network_failure", "SIWC request failed; no cross-channel retry was made")
	}
	if response.StatusCode == http.StatusUnauthorized {
		response.Body.Close()
		if service := credentialGrantProvider.Load(); service != nil {
			_, _ = service.db.InvalidateCredentialOpsProfileGrantCAS(ctx, account.ID(), "siwc", generation)
		}
		return nil, routeLocalError("siwc_authorization_required", "SIWC rejected its authorization; Codex credentials are unchanged")
	}
	response.Body = &siwcResponseBody{ReadCloser: response.Body, release: release}
	retained = true
	return response, nil
}
