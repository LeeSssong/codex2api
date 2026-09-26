package admin

import (
	"github.com/codex2api/database"
	"github.com/codex2api/internal/imagestore"
	"github.com/codex2api/internal/signedasset"
	"github.com/codex2api/proxy"
	"github.com/codex2api/security"
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
	c.JSON(200, h.basispointsSettingsResponse(c, s))
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
	c.JSON(200, h.basispointsSettingsResponse(c, saved))
}

func (h *Handler) basispointsSettingsResponse(c *gin.Context, s database.BasispointsSettings) any {
	var usage *database.ImageRelayUsage
	if u, err := h.db.GetImageRelayUsage(c.Request.Context()); err == nil {
		usage = &u
	}
	status := "disabled"
	if s.Enabled && s.ImageRelayEnabled {
		status = "configuration_incomplete"
		if s.Validate() == nil && signedasset.PersistentSigningConfigured() {
			status = "configured_unverified"
		}
	}
	runtime := gin.H{
		"signing_key_configured": signedasset.PersistentSigningConfigured(), "backend": imagestore.CurrentConfig().Normalize().Backend,
		"max_image_bytes": 20 << 20, "max_request_bytes": 32 << 20, "max_images": 20, "max_pixels": 64000000,
		"ttl_seconds": int(database.ImageRelayLifetime.Seconds()), "max_slots": 32, "active_slots": security.ImageRelayActiveSlots(),
		"request_memory_limit_bytes": security.GetRequestMemorySnapshot().LimitBytes, "request_body_limit_bytes": security.MaxRequestBodySize,
		"source": s.ConfigSource, "validation_status": status,
	}
	return struct {
		database.BasispointsSettings
		Usage   *database.ImageRelayUsage `json:"image_relay_usage"`
		Runtime gin.H                     `json:"image_relay_runtime"`
	}{s, usage, runtime}
}
