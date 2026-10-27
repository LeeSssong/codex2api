package admin

import (
	"encoding/json"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"strconv"
)

func (h *Handler) GetAccountStatsPricing(c *gin.Context) {
	id, e := strconv.ParseInt(c.Param("id"), 10, 64)
	if e != nil {
		writeError(c, http.StatusBadRequest, "无效的账号 ID")
		return
	}
	account := h.store.FindByID(id)
	if account == nil {
		writeError(c, http.StatusNotFound, "账号不存在")
		return
	}
	c.JSON(http.StatusOK, account.AccountStatsPricing())
}

// PUT replaces this account's statistical pricing only; customer fees are independent.
func (h *Handler) UpdateAccountStatsPricing(c *gin.Context) {
	id, e := strconv.ParseInt(c.Param("id"), 10, 64)
	if e != nil {
		writeError(c, http.StatusBadRequest, "无效的账号 ID")
		return
	}
	if h.store.FindByID(id) == nil {
		writeError(c, http.StatusNotFound, "账号不存在")
		return
	}
	var cfg database.AccountStatsPricingConfig
	decoder := json.NewDecoder(io.LimitReader(c.Request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if e := decoder.Decode(&cfg); e != nil {
		writeError(c, http.StatusBadRequest, "请求格式错误: "+e.Error())
		return
	}
	if decoder.Decode(new(any)) != io.EOF {
		writeError(c, http.StatusBadRequest, "请求必须是单个 JSON 对象")
		return
	}
	if e := database.ValidateAccountStatsPricing(cfg); e != nil {
		writeError(c, http.StatusBadRequest, e.Error())
		return
	}
	if e := h.store.SetAccountStatsPricing(c.Request.Context(), id, cfg); e != nil {
		writeError(c, http.StatusInternalServerError, e.Error())
		return
	}
	c.JSON(http.StatusOK, cfg)
}
