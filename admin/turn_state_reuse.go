package admin

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/codex2api/database"
	"github.com/codex2api/proxy"
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

func (h *Handler) GetTurnStateReuseStatus(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	if h.store == nil {
		c.JSON(http.StatusOK, gin.H{"accounts": []proxy.TurnStateReuseAccountStatus{}})
		return
	}
	statuses := proxy.TurnStateReuseStatuses(ctx, h.store.Accounts())
	byAccount := make(map[int64]turnStateHarvestAccountState)
	h.turnStateHarvestMu.RLock()
	for key, state := range h.turnStateHarvestStates {
		if state == nil {
			continue
		}
		var accountID int64
		if _, err := fmt.Sscanf(key, "%d:", &accountID); err == nil {
			byAccount[accountID] = *state
		}
	}
	h.turnStateHarvestMu.RUnlock()
	for i := range statuses {
		if state, ok := byAccount[statuses[i].AccountID]; ok {
			statuses[i].LastHTTPStatus = state.LastHTTPStatus
			statuses[i].LastError = state.LastError
			statuses[i].LastRoute = state.LastRoute
			if state.Status == "paused_auth" || state.Status == "paused_429" {
				statuses[i].Status = state.Status
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"accounts": statuses})
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
	proxy.InvalidateTurnStateReuseRuntime()
	h.GetTurnStateReuseSettings(c)
}
