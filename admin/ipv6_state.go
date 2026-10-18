package admin

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/codex2api/ipv6state"
	"github.com/codex2api/proxy"
	"github.com/codex2api/statepool"
	"github.com/gin-gonic/gin"
)

func (h *Handler) startIPv6State(ctx context.Context) error {
	h.ipv6State = ipv6state.New(h.db, h.store, proxy.ExecuteStateHeaderProbe, h.ipv6StateRoute, func(account *auth.Account, model string, response *http.Response) {
		switch response.StatusCode {
		case 429:
			proxy.Apply429Cooldown(h.store, account, nil, response, model)
		case 401:
			h.store.MarkCooldownWithError(account, 24*time.Hour, "unauthorized", "IPv6 state capture: authorization failed")
		case 403:
			h.store.MarkCooldownWithError(account, 15*time.Minute, "forbidden", "IPv6 state capture: upstream access denied")
		}
	})
	if err := h.ipv6State.Start(ctx); err != nil {
		return err
	}
	proxy.SetIPv6StateProvider(&proxy.IPv6StateProvider{Resolve: h.ipv6State.Resolve, Applies: h.ipv6State.Applies, Guard: h.ipv6State.Guard, EligibleAccounts: h.ipv6State.EligibleAccounts})
	return nil
}

func (h *Handler) ipv6StateRoute(ctx context.Context, config ipv6state.Config, attempt int64) (ipv6state.Route, error) {
	rows, err := h.db.ListProxies(ctx)
	if err != nil {
		return ipv6state.Route{}, errors.New("proxy_lookup_failed")
	}
	if len(config.ProxyIDs) == 0 {
		return ipv6state.Route{}, errors.New("no_capture_proxy")
	}
	available := make(map[int64]*database.ProxyRow, len(rows))
	for _, row := range rows {
		if row.Enabled {
			available[row.ID] = row
		}
	}
	route := ipv6state.Route{}
	for offset := range config.ProxyIDs {
		id := config.ProxyIDs[(attempt+int64(offset))%int64(len(config.ProxyIDs))]
		if row := available[id]; row != nil {
			route.ProxyID, route.ProxyName, route.ProxyURL = row.ID, row.Label, row.URL
			break
		}
	}
	if row := available[config.ForwardProxyID]; row != nil {
		route.ForwardURL = row.URL
	}
	if route.ProxyURL == "" || config.ForwardProxyID != 0 && route.ForwardURL == "" {
		return ipv6state.Route{}, errors.New("capture_proxy_unavailable")
	}
	if config.NewSession {
		route.ProxyURL, route.SessionID, err = statepool.RotateCaptureSession(route.ProxyURL)
		if err != nil {
			return ipv6state.Route{}, errors.New("proxy_session_generation_failed")
		}
	}
	return route, nil
}

func (h *Handler) registerIPv6StateRoutes(group *gin.RouterGroup) {
	v6 := group.Group("/ipv6")
	v6.Use(func(c *gin.Context) {
		if h.ipv6State == nil {
			c.AbortWithStatusJSON(503, gin.H{"error": "IPv6 state plugin is unavailable"})
		}
	})
	v6.GET("", func(c *gin.Context) { c.JSON(200, h.ipv6State.Status()) })
	v6.PATCH("/policy", func(c *gin.Context) {
		var req struct {
			RequireValidState *bool `json:"require_valid_state"`
		}
		if statePoolError(c, c.ShouldBindJSON(&req)) {
			return
		}
		if req.RequireValidState == nil {
			statePoolError(c, errors.New("require_valid_state is required"))
			return
		}
		if !statePoolError(c, h.ipv6State.SetRequireValidState(c.Request.Context(), *req.RequireValidState)) {
			c.JSON(200, h.ipv6State.Status())
		}
	})
	v6.PUT("", func(c *gin.Context) {
		config := h.ipv6State.Status().Config
		if statePoolError(c, c.ShouldBindJSON(&config)) || statePoolError(c, h.ipv6State.Configure(c.Request.Context(), config)) {
			return
		}
		c.JSON(200, h.ipv6State.Status())
	})
	v6.POST("/export", func(c *gin.Context) {
		var req struct {
			AccountID int64  `json:"account_id"`
			Model     string `json:"model"`
		}
		if statePoolError(c, c.ShouldBindJSON(&req)) {
			return
		}
		pack, err := h.ipv6State.Export(req.AccountID, req.Model)
		if !statePoolError(c, err) {
			c.JSON(200, pack)
		}
	})
	v6.POST("/import", func(c *gin.Context) {
		var pack ipv6state.Portable
		if statePoolError(c, c.ShouldBindJSON(&pack)) || statePoolError(c, h.ipv6State.Import(c.Request.Context(), pack)) {
			return
		}
		c.JSON(200, h.ipv6State.Status())
	})
}
