package admin

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/codex2api/database"
	"github.com/codex2api/egressipv6"
	"github.com/gin-gonic/gin"
)

func (h *Handler) AssignAccountIPv6Egress(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(c, http.StatusBadRequest, "无效的账号 ID")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var req struct {
		IP         string  `json:"ip"`
		ExpectedIP *string `json:"expected_ip"`
	}
	if err = c.ShouldBindJSON(&req); err != nil || req.ExpectedIP == nil {
		writeError(c, http.StatusBadRequest, "需要 ip 和 expected_ip（未分配时为空字符串）")
		return
	}
	manager := egressipv6.Current()
	if manager == nil {
		writeError(c, http.StatusServiceUnavailable, "IPv6 出口池不可用")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	binding, err := manager.AssignAccount(ctx, id, req.IP, *req.ExpectedIP)
	switch {
	case err == nil:
		c.JSON(http.StatusOK, gin.H{"ip": binding.IP})
	case errors.Is(err, sql.ErrNoRows):
		writeError(c, http.StatusNotFound, "账号不存在")
	case errors.Is(err, database.ErrIPv6InvalidSource):
		writeError(c, http.StatusBadRequest, "请选择本机 IPv6 池内的规范地址")
	case errors.Is(err, database.ErrIPv6NotEnrolled):
		writeError(c, http.StatusConflict, "请先保存账号的 IPv6 出口选择并清空代理")
	case errors.Is(err, database.ErrIPv6SelectionConflict):
		writeError(c, http.StatusConflict, "绑定已变更，或目标地址仍被占用或冷却，请刷新后重试")
	case errors.Is(err, database.ErrIPv6Unavailable):
		writeError(c, http.StatusServiceUnavailable, "IPv6 出口池已关闭或不可用")
	default:
		writeInternalError(c, err)
	}
}
