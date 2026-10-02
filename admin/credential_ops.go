package admin

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
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

// StartCredentialOpsImport creates or reuses a native OAuth account before queuing
// the real login runner. Identity dedup uses the same account table as routing.
func (h *Handler) StartCredentialOpsImport(c *gin.Context) {
	var in credentialOpsConfigRequest
	if c.ShouldBindJSON(&in) != nil || in.Email == "" {
		c.JSON(400, gin.H{"error": "email is required"})
		return
	}
	ctx := c.Request.Context()
	email := strings.ToLower(strings.TrimSpace(in.Email))
	id, err := h.db.FindActiveAccountByOAuthIdentity(ctx, email, "")
	if err != nil {
		c.JSON(500, gin.H{"error": "identity lookup failed"})
		return
	}
	if id == 0 {
		id, err = h.db.InsertAccountWithCredentials(ctx, email, map[string]any{"upstream_type": "openai", "email": email}, "")
		if err != nil {
			c.JSON(500, gin.H{"error": "native account create failed"})
			return
		}
	}
	c.Params = append(c.Params, gin.Param{Key: "id", Value: strconv.FormatInt(id, 10)})
	h.StartCredentialOpsLogin(c)
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
	row, err := h.db.GetAccountByID(c.Request.Context(), id)
	if err != nil || row == nil {
		c.JSON(404, gin.H{"error": "account not found"})
		return
	}
	_, err = h.credentialOpsManager().SaveLoginConfig(c.Request.Context(), credentialops.LoginConfigInput{AccountID: id, Email: in.Email, Mode: in.Mode, Engine: in.Engine, ProxySource: in.ProxySource, Password: in.Password, TOTPSecret: in.TOTPSecret, OTPURL: in.OTPURL})
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if err := h.db.EnsureCredentialOpsSchema(c.Request.Context()); err != nil {
		c.JSON(500, gin.H{"error": "credential operations unavailable"})
		return
	}
	job, err := h.db.CreateCredentialOpsTask(c.Request.Context(), id, row.CredentialGeneration)
	if err != nil {
		c.JSON(500, gin.H{"error": "could not queue login"})
		return
	}
	c.JSON(202, job)
}
func (h *Handler) GetCredentialOpsLogin(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("job_id"), 10, 64)
	job, err := h.db.GetCredentialOpsTask(c.Request.Context(), id)
	if err != nil || job == nil {
		c.JSON(404, gin.H{"error": "login job not found"})
		return
	}
	c.JSON(200, job)
}
func (h *Handler) CancelCredentialOpsLogin(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("job_id"), 10, 64)
	_ = h.db.CancelCredentialOpsTask(c.Request.Context(), id)
	c.Status(204)
}

func credentialOpsWorkerAuthorized(c *gin.Context) bool {
	token := os.Getenv("CODEX2API_CREDENTIAL_OPS_WORKER_TOKEN")
	return len(token) >= 32 && c.GetHeader("X-Codex2API-Credential-Worker") == token
}
func (h *Handler) ClaimCredentialOpsWorker(c *gin.Context) {
	if !credentialOpsWorkerAuthorized(c) {
		c.Status(403)
		return
	}
	var req struct {
		WorkerID string `json:"worker_id"`
	}
	if c.ShouldBindJSON(&req) != nil || req.WorkerID == "" {
		c.Status(400)
		return
	}
	task, err := h.db.ClaimCredentialOpsTask(c.Request.Context(), req.WorkerID, 2*60*1000000000)
	if err != nil {
		c.Status(500)
		return
	}
	if task == nil {
		c.Status(204)
		return
	}
	cfg, err := h.db.GetCredentialOpsLoginConfig(c.Request.Context(), task.AccountID)
	if err != nil || cfg == nil {
		_ = h.db.CompleteCredentialOpsTask(c.Request.Context(), task.ID, req.WorkerID, "failed", "failed", "missing config")
		c.Status(204)
		return
	}
	_, dec := credentialops.EnvCrypto()
	pw, e1 := dec(cfg.PasswordCiphertext)
	totp, e2 := dec(cfg.TOTPCiphertext)
	otp, e3 := dec(cfg.OTPURLCiphertext)
	if e1 != nil || e2 != nil || e3 != nil {
		_ = h.db.CompleteCredentialOpsTask(c.Request.Context(), task.ID, req.WorkerID, "failed", "failed", "encrypted config unavailable")
		c.Status(204)
		return
	}
	c.JSON(200, gin.H{"task": task, "login": gin.H{"email": cfg.LoginEmail, "mode": cfg.CredentialMode, "password": pw, "totp_secret": totp, "otp_url": otp}})
}
func (h *Handler) CompleteCredentialOpsWorker(c *gin.Context) {
	if !credentialOpsWorkerAuthorized(c) {
		c.Status(403)
		return
	}
	var req struct {
		WorkerID   string         `json:"worker_id"`
		Status     string         `json:"status"`
		Stage      string         `json:"stage"`
		Credential map[string]any `json:"credential"`
	}
	id, _ := strconv.ParseInt(c.Param("job_id"), 10, 64)
	if c.ShouldBindJSON(&req) != nil {
		c.Status(400)
		return
	}
	task, err := h.db.GetCredentialOpsTask(c.Request.Context(), id)
	if err != nil || task == nil || task.LeaseOwner != req.WorkerID {
		c.Status(409)
		return
	}
	if req.Status == "succeeded" {
		_, ok, err := h.db.UpdateAccountCredentialsCAS(c.Request.Context(), task.AccountID, task.ExpectedGeneration, req.Credential)
		if err != nil || !ok {
			_ = h.db.CompleteCredentialOpsTask(c.Request.Context(), id, req.WorkerID, "failed", "failed", "stale credential callback")
			c.Status(409)
			return
		}
	}
	_ = h.db.CompleteCredentialOpsTask(c.Request.Context(), id, req.WorkerID, req.Stage, req.Status, "")
	c.Status(204)
}

// RegisterCredentialOpsRoutes is called by the root route owner to avoid broad handler.go edits.
func (h *Handler) RegisterCredentialOpsRoutes(api *gin.RouterGroup) {
	api.GET("/accounts/:id/credential-ops/config", h.GetCredentialOpsConfig)
	api.PUT("/accounts/:id/credential-ops/config", h.SaveCredentialOpsConfig)
	api.POST("/accounts/:id/credential-ops/login", h.StartCredentialOpsLogin)
	api.POST("/credential-ops/import", h.StartCredentialOpsImport)
	api.GET("/credential-ops/login/:job_id", h.GetCredentialOpsLogin)
	api.DELETE("/credential-ops/login/:job_id", h.CancelCredentialOpsLogin)
	api.POST("/internal/credential-ops/claim", h.ClaimCredentialOpsWorker)
	api.POST("/internal/credential-ops/login/:job_id/complete", h.CompleteCredentialOpsWorker)
}
