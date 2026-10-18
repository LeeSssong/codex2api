package admin

import (
	"errors"
	"github.com/codex2api/database"
	"github.com/codex2api/egressipv6"
	"github.com/codex2api/fixedegress"
	"github.com/codex2api/proxy"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/netip"
	"regexp"
	"strconv"
)

var fixedID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func (h *Handler) fixedError(c *gin.Context, status int, msg string) {
	c.JSON(status, gin.H{"error": msg})
}
func (h *Handler) fixedBind(c *gin.Context, v any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 256*1024)
	if c.ShouldBindJSON(v) != nil {
		h.fixedError(c, 400, "请求格式无效")
		return false
	}
	return true
}
func (h *Handler) fixedConflict() bool { return proxy.IsResinEnabled() || egressipv6.Enabled() }
func (h *Handler) ListFixedEgress(c *gin.Context) {
	ctx := c.Request.Context()
	cfg, e := h.db.GetFixedEgressConfig(ctx)
	if e != nil {
		h.fixedError(c, 500, "读取固定出口配置失败")
		return
	}
	pools, e := h.db.ListFixedEgressPools(ctx)
	if e != nil {
		h.fixedError(c, 500, "读取出口池失败")
		return
	}
	providers, e := h.db.ListFixedEgressProviders(ctx)
	if e != nil {
		h.fixedError(c, 500, "读取供应商失败，请检查加密密钥")
		return
	}
	resources, e := h.db.ListFixedEgress(ctx)
	if e != nil {
		h.fixedError(c, 500, "读取代理资源失败，请检查加密密钥")
		return
	}
	bindings, e := h.db.ListFixedEgressBindings(ctx)
	if e != nil {
		h.fixedError(c, 500, "读取绑定失败")
		return
	}
	enrollments, e := h.db.ListFixedEgressEnrollments(ctx)
	if e != nil {
		h.fixedError(c, 500, "读取账号接入失败")
		return
	}
	tasks, e := h.db.ListFixedEgressTasks(ctx)
	if e != nil {
		h.fixedError(c, 500, "读取供应商操作失败")
		return
	}
	events, e := h.db.ListFixedEgressEvents(ctx)
	if e != nil {
		h.fixedError(c, 500, "读取出口事件失败")
		return
	}
	ps := []gin.H{}
	for _, p := range providers {
		ps = append(ps, gin.H{"id": p.ID, "name": p.Name, "base_url": p.BaseURL, "protocol": p.Protocol, "auth_format": p.AuthFormat, "country": p.Country, "city": p.City, "whitelist_ips": p.WhitelistIPs, "port_start": p.PortStart, "port_end": p.PortEnd, "capacity": p.Capacity, "refill_interval_seconds": p.RefillIntervalSeconds, "enabled": p.Enabled, "has_key": p.APIKey != ""})
	}
	rs := []gin.H{}
	for _, r := range resources {
		rs = append(rs, gin.H{"id": r.ID, "pool_id": r.PoolID, "provider_id": r.ProviderID, "provider_port": r.ProviderPort, "endpoint": fixedegress.MaskEndpoint(r.ProxyURL), "observed_exit_ip": r.ObservedExitIP, "state": r.State, "owned": r.Owned, "provider_task_id": r.ProviderTaskID, "changed_at": r.ChangedAt, "cooldown_until": r.CooldownUntil})
	}
	accounts := []gin.H{}
	for _, a := range h.store.Accounts() {
		if a.IsRelayStyle() {
			continue
		}
		a.Mu().RLock()
		name := a.Email
		a.Mu().RUnlock()
		accounts = append(accounts, gin.H{"id": a.ID(), "name": name})
	}
	c.JSON(200, gin.H{"config": cfg, "pools": pools, "providers": ps, "resources": rs, "bindings": bindings, "enrollments": enrollments, "events": events, "tasks": tasks, "accounts": accounts, "resin_enabled": proxy.IsResinEnabled(), "ipv6_enabled": egressipv6.Enabled()})
}
func (h *Handler) UpdateFixedEgressConfig(c *gin.Context) {
	var cfg database.FixedEgressConfig
	if !h.fixedBind(c, &cfg) {
		return
	}
	if cfg.Enabled && h.fixedConflict() {
		h.fixedError(c, 409, "请先关闭 Resin 和主机 IPv6 出口")
		return
	}
	if e := h.db.SetFixedEgressConfigRevision(c.Request.Context(), cfg.Enabled, cfg.Revision); e != nil {
		h.fixedError(c, 409, "配置已变化，请刷新重试")
		return
	}
	h.ListFixedEgress(c)
}
func (h *Handler) UpdateFixedEgressPool(c *gin.Context) {
	var p database.FixedEgressPool
	if !h.fixedBind(c, &p) {
		return
	}
	p.ID = c.Param("id")
	if !fixedID.MatchString(p.ID) || len(p.Name) > 128 || (p.Strategy != "strict" && p.Strategy != "stable") || p.CooldownSeconds < 30 || p.CooldownSeconds > 86400 || p.MaxAttempts < 1 || p.MaxAttempts > 5 {
		h.fixedError(c, 400, "出口池参数无效")
		return
	}
	if e := h.db.SaveFixedEgressPool(c.Request.Context(), p); e != nil {
		h.fixedError(c, 500, "保存出口池失败")
		return
	}
	h.ListFixedEgress(c)
}
func (h *Handler) SaveFixedEgressProvider(c *gin.Context) {
	var in struct {
		database.FixedEgressProvider
		APIKey string `json:"api_key"`
	}
	if !h.fixedBind(c, &in) {
		return
	}
	p := in.FixedEgressProvider
	p.ID = c.Param("id")
	p.APIKey = in.APIKey
	if p.BaseURL == "" {
		p.BaseURL = "https://ipapi.cliproxy.com"
	}
	if p.APIKey == "" {
		old, e := h.db.GetFixedEgressProvider(c.Request.Context(), p.ID)
		if e == nil {
			p.APIKey = old.APIKey
		}
	}
	_, e := fixedegress.NewCliProxyClient(p.BaseURL)
	if !fixedID.MatchString(p.ID) || len(p.Name) > 128 || e != nil || p.APIKey == "" || len(p.APIKey) > 4096 || (p.Protocol != "http" && p.Protocol != "https" && p.Protocol != "socks5") || (p.AuthFormat != "1" && p.AuthFormat != "2" && p.AuthFormat != "3") || p.PortStart < 1 || p.PortEnd > 65535 || p.PortEnd < p.PortStart || p.Capacity < 1 || p.Capacity > 1000 || p.RefillIntervalSeconds < 30 || p.RefillIntervalSeconds > 86400 {
		h.fixedError(c, 400, "供应商参数无效，首次保存需填写 API Key")
		return
	}
	for _, v := range p.WhitelistIPs {
		ip, e := netip.ParseAddr(v)
		if e != nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
			h.fixedError(c, 400, "白名单必须为公网 IP")
			return
		}
	}
	if e = h.db.SaveFixedEgressProvider(c.Request.Context(), p); e != nil {
		h.fixedError(c, 500, "供应商保存失败，请检查固定出口加密密钥")
		return
	}
	h.ListFixedEgress(c)
}
func (h *Handler) ImportFixedEgress(c *gin.Context) {
	var in struct {
		PoolID   string `json:"pool_id"`
		Protocol string `json:"protocol"`
		Format   int    `json:"format"`
		Lines    string `json:"lines"`
	}
	if !h.fixedBind(c, &in) {
		return
	}
	if len(in.Lines) > 128*1024 || !fixedID.MatchString(in.PoolID) {
		h.fixedError(c, 400, "导入参数无效")
		return
	}
	if e := h.fixedEgress.Import(c.Request.Context(), in.PoolID, in.Protocol, in.Format, in.Lines); e != nil {
		h.fixedError(c, 400, "导入失败，请检查格式、协议和加密配置")
		return
	}
	h.ListFixedEgress(c)
}
func (h *Handler) TestFixedEgressResource(c *gin.Context) {
	if e := h.fixedEgress.TestResource(c.Request.Context(), c.Param("id")); e != nil {
		h.fixedError(c, 409, e.Error())
		return
	}
	h.ListFixedEgress(c)
}
func (h *Handler) RetireFixedEgressResource(c *gin.Context) {
	if e := h.db.RetireFixedEgressResource(c.Request.Context(), c.Param("id"), false); e != nil {
		h.fixedError(c, 409, "资源仍有绑定，无法停用")
		return
	}
	h.ListFixedEgress(c)
}
func (h *Handler) fixedProviderAction(c *gin.Context, start bool) {
	var in struct {
		PoolID string `json:"pool_id"`
	}
	if !h.fixedBind(c, &in) {
		return
	}
	var e error
	if start {
		e = h.fixedEgress.StartProvider(c.Request.Context(), c.Param("id"), in.PoolID)
	} else {
		e = h.fixedEgress.SyncProvider(c.Request.Context(), c.Param("id"), in.PoolID)
	}
	if e != nil {
		h.fixedError(c, 409, "供应商操作未完成；请同步在线列表确认，勿重复启动。")
		return
	}
	h.ListFixedEgress(c)
}
func (h *Handler) SyncFixedEgressProvider(c *gin.Context)  { h.fixedProviderAction(c, false) }
func (h *Handler) StartFixedEgressProvider(c *gin.Context) { h.fixedProviderAction(c, true) }
func (h *Handler) StopFixedEgressResource(c *gin.Context) {
	// Refuse remote release while any native account work might retain the route.
	for _, a := range h.store.Accounts() {
		if a.ActiveRequests.Load() > 0 {
			h.fixedError(c, 409, "仍有在途请求，请排空后停止远端资源")
			return
		}
	}
	if e := h.fixedEgress.StopResource(c.Request.Context(), c.Param("id")); e != nil {
		h.fixedError(c, 409, "停止未确认，仅允许停止本程序创建且无绑定的资源，请同步确认")
		return
	}
	h.ListFixedEgress(c)
}
func (h *Handler) EnrollFixedEgressAccount(c *gin.Context) {
	id, e := strconv.ParseInt(c.Param("id"), 10, 64)
	if e != nil || id <= 0 {
		h.fixedError(c, 400, "账号 ID 无效")
		return
	}
	native := false
	for _, a := range h.store.Accounts() {
		if a.ID() == id && !a.IsRelayStyle() {
			native = true
			break
		}
	}
	if !native || h.fixedConflict() {
		h.fixedError(c, 409, "仅支持原生 Codex 账号，请关闭冲突出口")
		return
	}
	var in struct {
		PoolID string `json:"pool_id"`
	}
	if !h.fixedBind(c, &in) {
		return
	}
	if _, e = h.fixedEgress.Enroll(c.Request.Context(), id, in.PoolID); e != nil {
		h.fixedError(c, 409, "接入未完成，请启用功能及出口池并准备可用资源；已接入账号不会直连")
		return
	}
	h.ListFixedEgress(c)
}
func (h *Handler) UnenrollFixedEgressAccount(c *gin.Context) {
	id, e := strconv.ParseInt(c.Param("id"), 10, 64)
	if e != nil || id <= 0 {
		h.fixedError(c, 400, "账号 ID 无效")
		return
	}
	if e = h.db.UnenrollFixedEgress(c.Request.Context(), id); e != nil {
		h.fixedError(c, 500, "退出固定出口池失败")
		return
	}
	h.ListFixedEgress(c)
}
func (h *Handler) RotateFixedEgressAccount(c *gin.Context) {
	id, e := strconv.ParseInt(c.Param("id"), 10, 64)
	if e != nil || id <= 0 {
		h.fixedError(c, 400, "账号 ID 无效")
		return
	}
	var in struct {
		Generation int64  `json:"generation"`
		ResourceID string `json:"resource_id"`
	}
	if !h.fixedBind(c, &in) {
		return
	}
	if e = h.fixedEgress.Rotate(c.Request.Context(), id, in.Generation, in.ResourceID); e != nil {
		status := 409
		if !errors.Is(e, database.ErrFixedEgressStale) {
			status = 400
		}
		h.fixedError(c, status, "绑定已变化或备用资源不可用，请刷新重试")
		return
	}
	h.ListFixedEgress(c)
}

func (h *Handler) registerFixedEgressRoutes(api *gin.RouterGroup) {
	api.GET("/fixed-egress", h.ListFixedEgress)
	api.PUT("/fixed-egress/config", h.UpdateFixedEgressConfig)
	api.PUT("/fixed-egress/pools/:id", h.UpdateFixedEgressPool)
	api.PUT("/fixed-egress/providers/:id", h.SaveFixedEgressProvider)
	api.POST("/fixed-egress/import", h.ImportFixedEgress)
	api.POST("/fixed-egress/resources/:id/test", h.TestFixedEgressResource)
	api.POST("/fixed-egress/resources/:id/retire", h.RetireFixedEgressResource)
	api.POST("/fixed-egress/resources/:id/stop", h.StopFixedEgressResource)
	api.POST("/fixed-egress/providers/:id/sync", h.SyncFixedEgressProvider)
	api.POST("/fixed-egress/providers/:id/start", h.StartFixedEgressProvider)
	api.POST("/fixed-egress/accounts/:id/enroll", h.EnrollFixedEgressAccount)
	api.POST("/fixed-egress/accounts/:id/unenroll", h.UnenrollFixedEgressAccount)
	api.POST("/fixed-egress/accounts/:id/rotate", h.RotateFixedEgressAccount)
}
