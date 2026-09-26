package admin

import (
	"github.com/codex2api/database"
	"github.com/codex2api/proxy"
	"github.com/gin-gonic/gin"
)

// BasispointsSettingsPublisher is installed at startup to publish proxy snapshots.
var BasispointsSettingsPublisher func(database.BasispointsSettings)

func (h *Handler) GetBasispointsSettings(c *gin.Context) {
	s, e := h.db.GetBasispointsSettings(c.Request.Context())
	if e != nil {
		writeInternalError(c, e)
		return
	}
	c.JSON(200, s)
}
func (h *Handler) UpdateBasispointsSettings(c *gin.Context) {
	var s database.BasispointsSettings
	if e := c.ShouldBindJSON(&s); e != nil {
		writeError(c, 400, "Invalid Basispoints settings")
		return
	}
	if e := s.Validate(); e != nil {
		writeError(c, 400, e.Error())
		return
	}
	if e := h.db.SaveBasispointsSettings(c.Request.Context(), s); e != nil {
		writeInternalError(c, e)
		return
	}
	saved, e := h.db.GetBasispointsSettings(c.Request.Context())
	if e != nil {
		writeInternalError(c, e)
		return
	}
	h.store.SetCodexBasispointsEnabled(saved.Enabled)
	proxy.UpdateRuntimeSettings(func(r proxy.RuntimeSettings) proxy.RuntimeSettings {
		r.CodexBasispointsEnabled = saved.Enabled
		return r
	})
	if BasispointsSettingsPublisher != nil {
		BasispointsSettingsPublisher(saved)
	}
	c.JSON(200, saved)
}
