package admin

import (
	"context"
	"net/http"
	"strconv"
	"sync"

	"github.com/codex2api/credentialops"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
)

var credentialOpsManagers sync.Map // one manager per database pointer; no shared Sub2API service state
func (h *Handler) credentialOpsManager() *credentialops.Manager {
	if v, ok := credentialOpsManagers.Load(h.db); ok {
		return v.(*credentialops.Manager)
	}
	e, d := credentialops.EnvCrypto()
	m := credentialops.NewManager(credentialops.ManagerConfig{Encrypt: e, Decrypt: d})
	actual, _ := credentialOpsManagers.LoadOrStore(h.db, m)
	return actual.(*credentialops.Manager)
}

type credentialOpsConfigRequest struct {
	Email, Mode, Engine, ProxySource, Password, TOTPSecret, OTPURL string
	ClearPassword, ClearTOTP                                       bool
}

func (h *Handler) SaveCredentialOpsConfig(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var in credentialOpsConfigRequest
	if c.ShouldBindJSON(&in) != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid credential operations config"})
		return
	}
	if err := h.db.EnsureCredentialOpsSchema(c.Request.Context()); err != nil {
		c.JSON(500, gin.H{"error": "credential operations unavailable"})
		return
	}
	cfg, err := h.credentialOpsManager().SaveLoginConfig(c.Request.Context(), credentialops.LoginConfigInput{AccountID: id, Email: in.Email, Mode: in.Mode, Engine: in.Engine, ProxySource: in.ProxySource, Password: in.Password, TOTPSecret: in.TOTPSecret, OTPURL: in.OTPURL, ClearPassword: in.ClearPassword, ClearTOTP: in.ClearTOTP})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	_ = h.db.UpsertCredentialOpsLoginConfig(context.Background(), database.CredentialOpsLoginConfigRow{AccountID: id, LoginEmail: cfg.Email, CredentialMode: cfg.Mode, Engine: cfg.Engine, ProxySource: cfg.ProxySource, PasswordCiphertext: cfg.PasswordCiphertext, TOTPCiphertext: cfg.TOTPCiphertext, OTPURLCiphertext: cfg.OTPURLCiphertext})
	c.JSON(http.StatusOK, gin.H{"account_id": id, "login_email": cfg.Email, "credential_mode": cfg.Mode, "engine": cfg.Engine, "proxy_source": cfg.ProxySource, "password_configured": cfg.PasswordCiphertext != "", "totp_configured": cfg.TOTPCiphertext != "", "otp_url_configured": cfg.OTPURLCiphertext != ""})
}

func (h *Handler) GetCredentialOpsConfig(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	cfg, ok := h.credentialOpsManager().LoginConfig(id)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "credential config not found"})
		return
	}
	c.JSON(http.StatusOK, cfg)
}

func (h *Handler) StartCredentialOpsLogin(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var in credentialOpsConfigRequest
	if c.ShouldBindJSON(&in) != nil {
		c.JSON(400, gin.H{"error": "invalid login request"})
		return
	}
	job, err := h.credentialOpsManager().StartTwoFALogin(c.Request.Context(), id, credentialops.LoginConfigInput{AccountID: id, Email: in.Email, Mode: in.Mode, Engine: in.Engine, ProxySource: in.ProxySource, Password: in.Password, TOTPSecret: in.TOTPSecret, OTPURL: in.OTPURL})
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(202, job)
}
func (h *Handler) GetCredentialOpsLogin(c *gin.Context) {
	job, ok := h.credentialOpsManager().LoginJob(c.Param("job_id"))
	if !ok {
		c.JSON(404, gin.H{"error": "login job not found"})
		return
	}
	c.JSON(200, job)
}
func (h *Handler) CancelCredentialOpsLogin(c *gin.Context) {
	h.credentialOpsManager().CancelLogin(c.Param("job_id"))
	c.Status(204)
}

// RegisterCredentialOpsRoutes is called by the root route owner to avoid broad handler.go edits.
func (h *Handler) RegisterCredentialOpsRoutes(api *gin.RouterGroup) {
	api.GET("/accounts/:id/credential-ops/config", h.GetCredentialOpsConfig)
	api.PUT("/accounts/:id/credential-ops/config", h.SaveCredentialOpsConfig)
	api.POST("/accounts/:id/credential-ops/login", h.StartCredentialOpsLogin)
	api.GET("/credential-ops/login/:job_id", h.GetCredentialOpsLogin)
	api.DELETE("/credential-ops/login/:job_id", h.CancelCredentialOpsLogin)
}
