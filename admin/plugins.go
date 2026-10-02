package admin

import (
	"context"
	"errors"
	"net/http"

	"github.com/codex2api/database"
	"github.com/codex2api/plugins"
	"github.com/gin-gonic/gin"
)

// PluginEnabled is the shared backend gate for mutations and background jobs.
func (h *Handler) PluginEnabled(ctx context.Context, id string) bool {
	return h.pluginRegistry != nil && h.pluginRegistry.Enabled(ctx, id)
}

func (h *Handler) ListPlugins(c *gin.Context) {
	if h.pluginRegistry == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "plugin registry unavailable"})
		return
	}
	settings, err := h.pluginRegistry.List(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read plugin settings"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"plugins": settings})
}

func (h *Handler) UpdatePlugin(c *gin.Context) {
	if h.pluginRegistry == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "plugin registry unavailable"})
		return
	}
	id := c.Param("id")
	current, err := h.pluginRegistry.Get(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, plugins.ErrUnknownPlugin) {
			c.JSON(http.StatusNotFound, gin.H{"error": "unknown plugin"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read plugin setting"})
		return
	}
	var req struct {
		Enabled *bool           `json:"enabled"`
		Flags   map[string]bool `json:"flags"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid plugin setting"})
		return
	}
	if req.Enabled != nil {
		current.Enabled = *req.Enabled
	}
	if req.Flags != nil {
		current.Flags = req.Flags
	}
	if err := h.pluginRegistry.Set(c.Request.Context(), current); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save plugin setting"})
		return
	}
	// The ported account quality and token guard workers use the legacy module
	// fence as their durable cancellation boundary. Turning either capability
	// off increments that fence and cancels queued/running guarded work.
	if !current.Enabled && (id == "account-ops" || id == "quality-ops" || id == "token-guard") {
		if err := database.NewAccountOpsSettings(h.db).Set(c.Request.Context(), "module_enabled", "false"); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fence plugin work"})
			return
		}
		h.refreshAccountOpsModule(c.Request.Context())
	}
	c.JSON(http.StatusOK, current)
}
