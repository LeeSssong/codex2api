package proxy

import (
	"net/http"
	"strings"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/codex2api/internal/basispoints"
)

// The preferred path is resolved once from the effective model's catalog entry.
// Neither failed probes nor a disabled transport can turn it into a fallback.
func (d *CodexRouteDecision) requiresVerifiedBasispoints() bool {
	return d.Policy == database.CodexRouteBasispointsModelsOnly && d.Preferred == database.CodexPathBasispoints
}

func (d *CodexRouteDecision) relayViolatesModelPolicy(account *auth.Account, endpoint string, body []byte) bool {
	if d.Policy != database.CodexRouteBasispointsModelsOnly {
		return false
	}
	if d.requiresVerifiedBasispoints() {
		return true
	}
	models := []string{d.RequestedModel, d.EffectiveModel}
	if strings.HasSuffix(endpoint, "/messages") {
		models = []string{d.EffectiveModel}
	}
	var mapped string
	var ok bool
	if strings.HasSuffix(endpoint, "/compact") || requestBodyCompactionMeta(body).ProtocolTriggered {
		mapped, ok = resolveAccountCompactModelMappingForCandidates(account, compactMappingCandidates(models...))
	} else {
		mapped, ok = resolveAccountModelMappingForCandidates(account, models...)
	}
	return ok && basispointsModelAllowed(mapped)
}

func (d *CodexRouteDecision) validateModelPolicy(body []byte) {
	if !d.requiresVerifiedBasispoints() {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.policyError != nil {
		return
	}
	if !CurrentRuntimeSettings().CodexBasispointsEnabled {
		d.policyError = routeLocalError("codex_route_basispoints_disabled", "This model requires verified Basispoints accounts, but Basispoints is disabled. Enable Basispoints; native and relay fallback are not permitted by this Key.")
		return
	}
	if reason := basispoints.NativeCodexReason(body, basispointsImageHostAvailable()); reason != "" {
		d.policyError = routeLocalError("codex_route_basispoints_protocol", "This model is restricted to Basispoints, but the request requires native Codex ("+reason+"). Remove the incompatible feature or use a separately authorized native Key; account retries cannot resolve this conflict.")
		d.policyError.HTTPStatus = http.StatusBadRequest
	}
}
