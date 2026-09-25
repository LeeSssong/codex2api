package admin

import (
	"context"
	"errors"
	"github.com/codex2api/database"
	"github.com/codex2api/tokenguard"
	"github.com/gin-gonic/gin"
	"net/http"
	"strconv"
)

// The caller supplies the existing /api/admin group with adminAuthMiddleware.
func (h *Handler) RegisterTokenGuardRoutes(admin *gin.RouterGroup) {
	g := admin.Group("/account-ops/token-guard")
	g.GET("/status", h.GetTokenGuardStatus)
	g.GET("/config", h.GetTokenGuardConfig)
	g.PUT("/config", h.SaveTokenGuardConfig)
	g.POST("/run", h.RunTokenGuard)
	g.POST("/accounts/:id/relogin", h.ReloginTokenGuardAccount)
	g.POST("/jobs/:id/cancel", h.CancelTokenGuardJob)
	g.GET("/events", h.GetTokenGuardEvents)
}
func (h *Handler) StartTokenGuard(ctx context.Context) {
	if h.db == nil {
		return
	}
	if h.tokenGuard == nil {
		h.tokenGuard = tokenguard.NewService(h.db, h.store, nil)
	}
	h.tokenGuard.Start(ctx)
}
func (h *Handler) StopTokenGuard() {
	if h.tokenGuard != nil {
		h.tokenGuard.Stop()
	}
}
func (h *Handler) guardService(c *gin.Context) *tokenguard.Service {
	if h.tokenGuard == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "凭证守护服务不可用"})
		return nil
	}
	return h.tokenGuard
}
func guardHTTPError(c *gin.Context, err error, invalid bool) {
	status := http.StatusInternalServerError
	message := "凭证守护请求失败"
	if errors.Is(err, database.ErrTokenGuardBusy) || errors.Is(err, database.ErrTokenGuardStale) || errors.Is(err, database.ErrTokenGuardDisabled) {
		status = http.StatusConflict
		message = err.Error()
	} else if invalid {
		status = http.StatusBadRequest
		message = "配置、账号范围或明确身份映射无效"
	}
	c.JSON(status, gin.H{"error": message})
}
func (h *Handler) GetTokenGuardStatus(c *gin.Context) {
	s := h.guardService(c)
	if s == nil {
		return
	}
	status, err := s.Status(c.Request.Context())
	if err != nil {
		guardHTTPError(c, err, false)
		return
	}
	c.JSON(http.StatusOK, status)
}
func (h *Handler) GetTokenGuardConfig(c *gin.Context) {
	s := h.guardService(c)
	if s == nil {
		return
	}
	cfg, _, err := s.Config(c.Request.Context())
	if err != nil {
		guardHTTPError(c, err, false)
		return
	}
	c.JSON(http.StatusOK, tokenguard.PublicConfig(cfg))
}
func (h *Handler) SaveTokenGuardConfig(c *gin.Context) {
	s := h.guardService(c)
	if s == nil {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	var req tokenguard.Config
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的凭证守护配置"})
		return
	}
	cfg, err := s.SaveConfig(c.Request.Context(), req)
	if err != nil {
		guardHTTPError(c, err, true)
		return
	}
	c.JSON(http.StatusOK, cfg)
}
func (h *Handler) RunTokenGuard(c *gin.Context) {
	s := h.guardService(c)
	if s == nil {
		return
	}
	j, err := s.Run(c.Request.Context(), 0)
	if err != nil {
		guardHTTPError(c, err, true)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"job_id": j.ID, "state": j.State})
}
func (h *Handler) ReloginTokenGuardAccount(c *gin.Context) {
	s := h.guardService(c)
	if s == nil {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的账号ID"})
		return
	}
	j, err := s.Run(c.Request.Context(), id)
	if err != nil {
		guardHTTPError(c, err, true)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"job_id": j.ID, "state": j.State})
}
func (h *Handler) CancelTokenGuardJob(c *gin.Context) {
	s := h.guardService(c)
	if s == nil {
		return
	}
	id := c.Param("id")
	if len(id) > 64 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的任务ID"})
		return
	}
	cancelled, err := s.Cancel(c.Request.Context(), id)
	if err != nil {
		guardHTTPError(c, err, false)
		return
	}
	c.JSON(http.StatusOK, gin.H{"cancelled": cancelled})
}
func (h *Handler) GetTokenGuardEvents(c *gin.Context) {
	s := h.guardService(c)
	if s == nil {
		return
	}
	before, _ := strconv.ParseInt(c.Query("before_id"), 10, 64)
	limit, _ := strconv.Atoi(c.Query("limit"))
	items, cursor, err := s.Events(c.Request.Context(), before, limit)
	if err != nil {
		guardHTTPError(c, err, false)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "next_cursor": cursor})
}
