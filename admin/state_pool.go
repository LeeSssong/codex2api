package admin

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/proxy"
	"github.com/codex2api/statepool"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func (h *Handler) StartStatePool(ctx context.Context) error {
	h.statePool = statepool.New(h.db, h.store,
		func(ctx context.Context, account *auth.Account, body []byte, proxyURL string, headers http.Header) (*http.Response, error) {
			return proxy.ExecuteRequest(proxy.WithFreshStateProbe(ctx, statepool.CaptureForwardProxy(ctx)), account, body, "", proxyURL, "", nil, headers, false)
		}, func(account *auth.Account, model string, response *http.Response, body []byte) {
			code := gjson.GetBytes(body, "error.code").String()
			if code == "" {
				code = gjson.GetBytes(body, "response.error.code").String()
			}
			if code == "" {
				code = gjson.GetBytes(body, "code").String()
			}
			switch {
			case response.StatusCode == 429 || code == "rate_limit_exceeded" || code == "usage_limit_reached":
				proxy.Apply429Cooldown(h.store, account, body, response, model)
			case response.StatusCode == 401 || code == "token_revoked":
				h.store.MarkCooldownWithError(account, 24*time.Hour, "unauthorized", "State validation: authorization failed")
			case code == "deactivated_workspace" || gjson.GetBytes(body, "detail.code").String() == "deactivated_workspace":
				h.store.MarkDeactivatedWorkspace(account, "State validation: workspace deactivated")
			case response.StatusCode == 403:
				h.store.MarkCooldownWithError(account, 15*time.Minute, "forbidden", "State validation: upstream access denied")
			}
		})
	if err := h.statePool.Start(ctx); err != nil {
		return err
	}
	proxy.SetStatePoolResolver(h.statePool.Resolve)
	return nil
}

func (h *Handler) StopStatePool() {
	proxy.SetStatePoolResolver(nil)
	if h.statePool != nil {
		h.statePool.Stop()
	}
}

func (h *Handler) registerStatePoolRoutes(api *gin.RouterGroup) {
	group := api.Group("/state-pool")
	group.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4<<20)
		if h.statePool == nil {
			c.AbortWithStatusJSON(503, gin.H{"error": "State pool is not available"})
		}
	})
	group.GET("", h.statePoolList)
	group.PUT("/limits", h.statePoolLimits)
	group.POST("/capture", h.statePoolCapture)
	group.POST("/import", h.statePoolImport)
	group.POST("/import/preview", func(c *gin.Context) {
		var pack statepool.Package
		if statePoolError(c, c.ShouldBindJSON(&pack)) {
			return
		}
		items, err := h.statePool.PreviewImport(pack)
		if !statePoolError(c, err) {
			c.JSON(200, gin.H{"items": items})
		}
	})
	group.POST("/export", h.statePoolExport)
	group.POST("/jobs/:id/cancel", h.statePoolCancel)
	group.POST("/groups/:id/cancel", func(c *gin.Context) {
		if !statePoolError(c, h.statePool.CancelGroup(c.Request.Context(), c.Param("id"))) {
			c.JSON(200, gin.H{"ok": true})
		}
	})
	group.PATCH("/entries/:id", h.statePoolConfigure)
	group.DELETE("/entries/:id", h.statePoolDelete)
}

func statePoolError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	return true
}

func (h *Handler) statePoolList(c *gin.Context) {
	jobs, err := h.statePool.Jobs(c.Request.Context())
	if statePoolError(c, err) {
		return
	}
	global, perAccount, err := h.db.StatePoolLimits(c.Request.Context())
	if statePoolError(c, err) {
		return
	}
	perProxy, err := h.db.StatePoolProxyLimit(c.Request.Context())
	if statePoolError(c, err) {
		return
	}
	proxyRows, err := h.db.ListProxies(c.Request.Context())
	if statePoolError(c, err) {
		return
	}
	proxies := []gin.H{}
	for _, row := range proxyRows {
		proxies = append(proxies, gin.H{"id": row.ID, "name": row.Label, "enabled": row.Enabled, "last_test_ip": row.TestIP, "test_status": row.TestStatus,
			"supports_session_rotation": statepool.SupportsSessionRotation(row.URL)})
	}
	accounts := []gin.H{}
	for _, account := range h.store.Accounts() {
		if _, err := statepool.Snapshot(account, h.store.ResolveProxyForAccount(account)); err != nil {
			continue
		}
		account.Mu().RLock()
		id, email, plan := account.DBID, account.Email, account.PlanType
		account.Mu().RUnlock()
		accounts = append(accounts, gin.H{"id": id, "name": email, "plan": plan, "available": account.IsAvailable()})
	}
	c.JSON(200, gin.H{"entries": h.statePool.Entries(), "jobs": jobs, "accounts": accounts,
		"proxies": proxies, "resin_enabled": proxy.IsResinEnabled(), "models": statepool.Models,
		"limits": gin.H{"concurrency": global, "per_account": perAccount, "per_proxy": perProxy}, "server_time": time.Now().Unix()})
}

func (h *Handler) statePoolLimits(c *gin.Context) {
	var req struct {
		Concurrency int `json:"concurrency"`
		PerAccount  int `json:"per_account"`
		PerProxy    int `json:"per_proxy"`
	}
	if statePoolError(c, c.ShouldBindJSON(&req)) {
		return
	}
	if req.PerProxy == 0 {
		req.PerProxy = 2
	}
	if statePoolError(c, h.db.SetStatePoolLimits(c.Request.Context(), req.Concurrency, req.PerAccount, req.PerProxy)) {
		return
	}
	c.JSON(200, req)
}

func (h *Handler) statePoolCapture(c *gin.Context) {
	if proxy.IsResinEnabled() {
		statePoolError(c, errors.New("Resin overrides proxy selection; use directly selectable proxy egress for state capture"))
		return
	}
	var req struct {
		AccountIDs []int64  `json:"account_ids"`
		Models     []string `json:"models"`
		Enable     bool     `json:"enable"`
		Strict     bool     `json:"strict"`
		statepool.CaptureOptions
	}
	if statePoolError(c, c.ShouldBindJSON(&req)) {
		return
	}
	ids, err := h.statePool.Capture(c.Request.Context(), req.AccountIDs, req.Models, req.Enable, req.Strict, req.CaptureOptions)
	if !statePoolError(c, err) {
		c.JSON(http.StatusAccepted, gin.H{"job_ids": ids})
	}
}

func (h *Handler) statePoolImport(c *gin.Context) {
	if proxy.IsResinEnabled() {
		statePoolError(c, errors.New("Resin egress is not supported for state validation"))
		return
	}
	var req struct {
		Package      *statepool.Package `json:"package"`
		AccountID    int64              `json:"account_id"`
		Model        string             `json:"model"`
		Value        string             `json:"value"`
		CapturedAt   int64              `json:"captured_at"`
		Enable       bool               `json:"enable"`
		Strict       bool               `json:"strict"`
		AllowPartial bool               `json:"allow_partial"`
	}
	if statePoolError(c, c.ShouldBindJSON(&req)) {
		return
	}
	var ids []string
	var err error
	var preview []statepool.ImportPreviewItem
	if req.Package != nil {
		preview, err = h.statePool.PreviewImport(*req.Package)
		if statePoolError(c, err) {
			return
		}
		ids, err = h.statePool.Import(c.Request.Context(), *req.Package, req.Enable, req.Strict, req.AllowPartial)
	} else {
		ids, err = h.statePool.ImportRaw(c.Request.Context(), req.AccountID, req.Model, req.Value, req.CapturedAt, req.Enable, req.Strict)
	}
	if !statePoolError(c, err) {
		c.JSON(http.StatusAccepted, gin.H{"job_ids": ids, "items": preview})
	}
}

func (h *Handler) statePoolExport(c *gin.Context) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if statePoolError(c, c.ShouldBindJSON(&req)) {
		return
	}
	pack, err := h.statePool.Export(req.IDs)
	if !statePoolError(c, err) {
		c.JSON(200, pack)
	}
}

func (h *Handler) statePoolConfigure(c *gin.Context) {
	var req struct {
		Enabled bool `json:"enabled"`
		Strict  bool `json:"strict"`
	}
	if statePoolError(c, c.ShouldBindJSON(&req)) || statePoolError(c, h.statePool.Configure(c.Request.Context(), c.Param("id"), req.Enabled, req.Strict)) {
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

func (h *Handler) statePoolCancel(c *gin.Context) {
	if !statePoolError(c, h.statePool.Cancel(c.Request.Context(), c.Param("id"))) {
		c.JSON(200, gin.H{"ok": true})
	}
}

func (h *Handler) statePoolDelete(c *gin.Context) {
	if !statePoolError(c, h.statePool.Delete(c.Request.Context(), c.Param("id"))) {
		c.JSON(200, gin.H{"ok": true})
	}
}
