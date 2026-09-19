package admin

import (
	"context"
	"net/http"
	"time"

	"github.com/codex2api/database"
	"github.com/codex2api/security"
	"github.com/gin-gonic/gin"
)

func (h *Handler) GetTurnStateReuseSettings(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	settings, err := h.db.GetTurnStateReuseSettings(ctx)
	if err != nil {
		writeInternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, settings)
}

func (h *Handler) UpdateTurnStateReuseSettings(c *gin.Context) {
	var req database.TurnStateReuseSettings
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "请求格式错误")
		return
	}
	for _, raw := range req.HarvestProxyURLs {
		if err := security.ValidateProxyURL(raw); err != nil {
			writeError(c, http.StatusBadRequest, err.Error())
			return
		}
	}
	for _, id := range []*int64{req.MissTargetGroupID, req.RecoveredTargetGroupID} {
		if id == nil {
			continue
		}
		missing, err := h.db.VerifyAccountGroupIDs(c.Request.Context(), []int64{*id})
		if err != nil {
			writeInternalError(c, err)
			return
		}
		if len(missing) > 0 {
			writeError(c, http.StatusBadRequest, "目标分组不存在")
			return
		}
	}
	if err := h.db.UpdateTurnStateReuseSettings(c.Request.Context(), req); err != nil {
		writeError(c, http.StatusBadRequest, err.Error())
		return
	}
	h.GetTurnStateReuseSettings(c)
}
