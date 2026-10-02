package admin

import (
	"context"
	"errors"
	"fmt"
	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/codex2api/proxy"
	"github.com/codex2api/smartops"
	"github.com/gin-gonic/gin"
	"net/http"
	"strings"
	"sync"
)

func (h *Handler) InitSmartOps(ctx context.Context) error {
	if h.db == nil || h.store == nil {
		return errors.New("native smart operations stores unavailable")
	}
	r := smartops.NewRuntime(h.PluginEnabled)
	h.smartOps = r
	if e := r.Start(ctx, h.db, h.executePelicanSample); e != nil {
		return e
	}
	h.db.SetSmartOpsOAuthDefaultsProvider(func(ctx context.Context) (smartops.OAuthAutoConfig, bool) {
		c, _, e := r.Config(ctx)
		return c, e == nil
	})
	proxy.ConfigureSmartOpsBPSProxyResolver(h.store.ResolveSmartOpsBPSSessionProxy)
	initial, _, e := r.Config(ctx)
	if e != nil {
		return e
	}
	h.db.SetSmartOpsBilling(initial.ModelBilling, func() bool { return h.PluginEnabled(context.Background(), smartops.PluginAutoConfig) })
	h.store.StartSmartOps(ctx, h.PluginEnabled, func(ctx context.Context) (smartops.OAuthAutoConfig, smartops.PriorityConfig, error) {
		return r.Config(ctx)
	})
	return nil
}
func (h *Handler) RegisterSmartOps(r *gin.RouterGroup) {
	if h.smartOps != nil {
		RegisterSmartOpsRoutes(r, h.smartOps)
	}
}
func (h *Handler) WaitSmartOps() {
	if h.smartOps != nil {
		h.smartOps.Wait()
	}
	if h.store != nil {
		h.store.WaitSmartOps()
	}
	if h.db != nil {
		h.db.SetSmartOpsOAuthDefaultsProvider(nil)
	}
}

func (h *Handler) insertSmartOpsOAuthAccount(ctx context.Context, name string, credentials map[string]interface{}, proxyURL string) (int64, error) {
	cfg, e := h.db.LoadOAuthAutoConfig(ctx)
	if e != nil {
		return 0, e
	}
	if !h.PluginEnabled(ctx, smartops.PluginAutoConfig) || !cfg.Enabled || cfg.Platform != "openai" {
		return h.db.InsertAccountWithCredentials(ctx, name, credentials, proxyURL)
	}
	// Validate configured groups before creating an identity. Existing identities
	// never enter this path, so reauthorization preserves administrator overrides.
	if e = h.db.SaveOAuthAutoConfig(ctx, cfg); e != nil {
		return 0, e
	}
	return h.db.InsertAutoConfiguredOAuthAccount(ctx, name, "openai", "oauth", credentials, proxyURL, cfg)
}

func (h *Handler) executePelicanSample(ctx context.Context, j smartops.PelicanJob, sample int) (smartops.PelicanResult, error) {
	result := smartops.PelicanResult{}
	if !h.PluginEnabled(ctx, smartops.PluginPelicanTests) {
		return result, smartops.ErrDisabled
	}
	id := j.AccountID
	if len(j.GroupIDs) > 0 {
		members, e := h.db.ListAccountIDsInGroups(ctx, j.GroupIDs)
		if e != nil {
			return result, e
		}
		allowed := map[int64]bool{}
		for _, n := range members {
			allowed[n] = true
		}
		a := h.store.NextExcludingWithDispatch(0, nil, func(a *auth.Account) bool { return allowed[a.DBID] && h.accountSupportsPelican(ctx, a, j) }, auth.DispatchPolicyStandard, j.Model)
		if a == nil {
			return result, errors.New("no eligible group account supports the requested model")
		}
		id = a.DBID
		defer h.store.Release(a)
	} else {
		a := h.store.TakePreferredAccountWithFilter(id, 0, nil, func(a *auth.Account) bool { return h.accountSupportsPelican(ctx, a, j) })
		if a == nil || !h.accountSupportsPelican(ctx, a, j) {
			return result, errors.New("account unavailable or model unsupported")
		}
		defer h.store.Release(a)
	}
	result.AccountID = id
	prompt := strings.TrimSpace(j.Prompt)
	if prompt == "" {
		prompt = "Create a complete HTML page showing a pelican riding a bicycle. Use inline SVG, CSS and JavaScript only."
	}
	req := qualityTestRequest{Model: j.Model, Prompt: prompt, ReasoningEffort: j.ReasoningEffort}
	writer := &qualityJobWriter{header: make(http.Header)}
	var mu sync.Mutex
	complete := false
	secrets := codexTestSecrets(h.store.FindByID(id))
	writer.emit = func(event testEvent) {
		mu.Lock()
		defer mu.Unlock()
		switch event.Type {
		case "content":
			if len(result.Output)+len(event.Text) > 1<<20 {
				result.Error = "output exceeds 1 MiB"
			} else {
				result.Output += sanitizeCodexTestText(event.Text, secrets)
			}
		case "error":
			result.Error = sanitizeCodexTestText(event.Error, secrets)
		case "test_complete":
			complete = event.Success
		}
		if d := event.CodexDiagnostics; d != nil {
			result.FirstContentMS = d.FirstContentMS
			if d.Usage != nil {
				result.InputTokens = d.Usage.InputTokens
				result.OutputTokens = d.Usage.OutputTokens
				in := connectionTestUsageFromCodex(d, j.Model)
				cost := database.UsageLogBilledCost(&database.UsageLogInput{Model: in.Model, EffectiveModel: in.EffectiveModel, InputTokens: in.InputTokens, OutputTokens: in.OutputTokens, CachedTokens: in.CachedTokens, ReasoningTokens: in.ReasoningTokens})
				result.CostUSD = &cost
			}
		}
		if d := event.Diagnostics; d != nil {
			result.FirstContentMS = d.FirstContentMS
			if d.Usage != nil {
				result.InputTokens = d.Usage.InputTokens
				result.OutputTokens = d.Usage.OutputTokens
				in := connectionTestUsageFromClaude(d, j.Model)
				cost := database.UsageLogBilledCost(&database.UsageLogInput{Model: in.Model, EffectiveModel: in.EffectiveModel, InputTokens: in.InputTokens, OutputTokens: in.OutputTokens, CachedTokens: in.CachedTokens, CacheWrite5mTokens: in.CacheWrite5m, CacheWrite1hTokens: in.CacheWrite1h})
				result.CostUSD = &cost
			}
		}
	}
	router := gin.New()
	router.POST("/accounts/:id/test", func(c *gin.Context) { h.testConnection(c, &req) })
	request, e := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("/accounts/%d/test", id), nil)
	if e != nil {
		return result, e
	}
	router.ServeHTTP(writer, request)
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if result.Error != "" {
		return result, errors.New(result.Error)
	}
	if !complete {
		return result, errors.New("native quality executor did not complete")
	}
	return result, nil
}
func (h *Handler) accountSupportsPelican(ctx context.Context, a *auth.Account, j smartops.PelicanJob) bool {
	return h.validateQualityTestForAccount(ctx, a, qualityTestRequest{Model: j.Model, ReasoningEffort: j.ReasoningEffort}) == nil
}
