package admin

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"github.com/codex2api/auth"
	"github.com/codex2api/credentialops"
	"github.com/codex2api/database"
	"github.com/codex2api/internal/openaiidentity"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type credentialOpsConfigRequest struct {
	Name          string `json:"name"`
	Email         string `json:"email"`
	Mode          string `json:"mode"`
	Engine        string `json:"engine"`
	ProxySource   string `json:"proxy_source"`
	Password      string `json:"password"`
	TOTPSecret    string `json:"totp_secret"`
	OTPURL        string `json:"otp_url"`
	ClearPassword bool   `json:"clear_password"`
	ClearTOTP     bool   `json:"clear_totp"`
	ClearOTPURL   bool   `json:"clear_otp_url"`
}

func configView(cfg *database.CredentialOpsLoginConfigRow) gin.H {
	return gin.H{"account_id": cfg.AccountID, "email": cfg.LoginEmail, "mode": cfg.CredentialMode, "engine": cfg.Engine, "proxy_source": cfg.ProxySource, "password_configured": cfg.PasswordCiphertext != "", "totp_configured": cfg.TOTPCiphertext != "", "otp_url_configured": cfg.OTPURLCiphertext != "", "updated_at": cfg.UpdatedAt}
}
func (h *Handler) prepareCredentialConfig(c *gin.Context, id int64, in credentialOpsConfigRequest) (database.CredentialOpsLoginConfigRow, error) {
	old := credentialops.LoginConfig{}
	if id > 0 {
		row, err := h.db.GetAccountByID(c.Request.Context(), id)
		if err != nil || row == nil || row.Status == "deleted" {
			return database.CredentialOpsLoginConfigRow{}, errors.New("account not found")
		}
		if row.Platform != "" && row.Platform != "openai" {
			return database.CredentialOpsLoginConfigRow{}, errors.New("only OpenAI accounts support credential operations")
		}
		cfg, err := h.db.GetCredentialOpsLoginConfig(c.Request.Context(), id)
		if err != nil {
			return database.CredentialOpsLoginConfigRow{}, err
		}
		if cfg != nil {
			old = credentialops.LoginConfig{AccountID: id, Email: cfg.LoginEmail, Mode: cfg.CredentialMode, Engine: cfg.Engine, ProxySource: cfg.ProxySource, PasswordCiphertext: cfg.PasswordCiphertext, TOTPCiphertext: cfg.TOTPCiphertext, OTPURLCiphertext: cfg.OTPURLCiphertext}
		}
		if old.Email == "" {
			old.Email, _ = row.Credentials["email"].(string)
		}
	}
	cfg, err := credentialops.PrepareConfig(credentialops.LoginConfigInput{AccountID: id, Email: in.Email, Mode: in.Mode, Engine: in.Engine, ProxySource: in.ProxySource, Password: in.Password, TOTPSecret: in.TOTPSecret, OTPURL: in.OTPURL, ClearPassword: in.ClearPassword, ClearTOTP: in.ClearTOTP, ClearOTPURL: in.ClearOTPURL}, old)
	if err == nil && cfg.Engine == "session_studio" {
		u, e := url.Parse(os.Getenv("CODEX2API_CREDENTIAL_OPS_SESSION_STUDIO_ENDPOINT"))
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			err = errors.New("Session Studio endpoint is not configured")
		}
	}
	if len(in.Name) > 200 {
		err = errors.New("account name exceeds 200 characters")
	}
	return database.CredentialOpsLoginConfigRow{AccountID: id, Name: strings.TrimSpace(in.Name), LoginEmail: cfg.Email, CredentialMode: cfg.Mode, Engine: cfg.Engine, ProxySource: cfg.ProxySource, PasswordCiphertext: cfg.PasswordCiphertext, TOTPCiphertext: cfg.TOTPCiphertext, OTPURLCiphertext: cfg.OTPURLCiphertext}, err
}
func (h *Handler) StartCredentialOpsImport(c *gin.Context) { h.queueCredentialLogin(c, 0) }
func (h *Handler) StartCredentialOpsLogin(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	if id <= 0 {
		c.Status(400)
		return
	}
	h.queueCredentialLogin(c, id)
}
func (h *Handler) queueCredentialLogin(c *gin.Context, id int64) {
	var in credentialOpsConfigRequest
	if c.ShouldBindJSON(&in) != nil {
		c.JSON(400, gin.H{"error": "invalid login request"})
		return
	}
	cfg, err := h.prepareCredentialConfig(c, id, in)
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if (cfg.CredentialMode == "password_totp" && cfg.PasswordCiphertext == "") || (cfg.CredentialMode == "email_otp_url" && cfg.OTPURLCiphertext == "") {
		c.JSON(400, gin.H{"error": "configured login secret is required"})
		return
	}
	var generation int64
	if id > 0 {
		row, err := h.db.GetAccountByID(c.Request.Context(), id)
		if err != nil || row == nil {
			c.Status(404)
			return
		}
		generation = row.CredentialGeneration
	}
	job, err := h.db.CreateCredentialOpsTaskWithConfig(c.Request.Context(), id, generation, cfg)
	if err != nil {
		c.JSON(409, gin.H{"error": err.Error()})
		return
	}
	c.JSON(202, job)
}
func (h *Handler) SaveCredentialOpsConfig(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var in credentialOpsConfigRequest
	if id <= 0 || c.ShouldBindJSON(&in) != nil {
		c.Status(400)
		return
	}
	cfg, err := h.prepareCredentialConfig(c, id, in)
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if err = h.db.UpsertCredentialOpsLoginConfig(c.Request.Context(), cfg); err != nil {
		c.JSON(500, gin.H{"error": "config persistence failed"})
		return
	}
	c.JSON(200, configView(&cfg))
}
func (h *Handler) GetCredentialOpsConfig(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	cfg, err := h.db.GetCredentialOpsLoginConfig(c.Request.Context(), id)
	if err != nil {
		c.Status(500)
		return
	}
	if cfg == nil {
		c.Status(404)
		return
	}
	c.JSON(200, configView(cfg))
}
func (h *Handler) GetCredentialOpsLogin(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("job_id"), 10, 64)
	task, err := h.db.GetCredentialOpsTask(c.Request.Context(), id)
	if err != nil {
		c.Status(500)
		return
	}
	if task == nil {
		c.Status(404)
		return
	}
	c.JSON(200, task)
}
func (h *Handler) CancelCredentialOpsLogin(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("job_id"), 10, 64)
	if h.db.CancelCredentialOpsTask(c.Request.Context(), id) != nil {
		c.Status(500)
		return
	}
	c.Status(204)
}
func credentialOpsWorkerAuthorized(c *gin.Context) bool {
	want := os.Getenv("CODEX2API_CREDENTIAL_OPS_WORKER_TOKEN")
	got := c.GetHeader("X-Codex2API-Credential-Worker")
	return len(want) >= 32 && subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}
func (h *Handler) ClaimCredentialOpsWorker(c *gin.Context) {
	var req struct {
		WorkerID string `json:"worker_id"`
	}
	if c.ShouldBindJSON(&req) != nil || req.WorkerID == "" || len(req.WorkerID) > 128 {
		c.Status(400)
		return
	}
	task, err := h.db.ClaimCredentialOpsTask(c.Request.Context(), req.WorkerID, 2*time.Minute)
	if err != nil {
		c.Status(500)
		return
	}
	if task == nil {
		c.Status(204)
		return
	}
	cfg, err := h.db.CredentialOpsTaskConfig(c.Request.Context(), task.ID)
	if err != nil || cfg == nil {
		_ = h.db.CompleteCredentialOpsTask(c.Request.Context(), task.ID, req.WorkerID, "failed", "failed", "missing encrypted config")
		c.Status(204)
		return
	}
	_, dec := credentialops.EnvCrypto()
	pw, e1 := dec(cfg.PasswordCiphertext)
	totp, e2 := dec(cfg.TOTPCiphertext)
	otp, e3 := dec(cfg.OTPURLCiphertext)
	if e1 != nil || e2 != nil || e3 != nil || (cfg.CredentialMode == "password_totp" && pw == "") || (cfg.CredentialMode == "email_otp_url" && otp == "") {
		_ = h.db.CompleteCredentialOpsTask(c.Request.Context(), task.ID, req.WorkerID, "failed", "failed", "encrypted config unavailable")
		c.Status(204)
		return
	}
	proxyURL := ""
	if h.store != nil && (cfg.ProxySource == "global" || (cfg.ProxySource == "account" && task.AccountID == 0)) {
		proxyURL = h.store.GetProxyURL()
	}
	if task.AccountID > 0 && cfg.ProxySource == "account" {
		row, e := h.db.GetAccountByID(c.Request.Context(), task.AccountID)
		if e != nil || row == nil {
			c.Status(500)
			return
		}
		proxyURL = row.ProxyURL
		if proxyURL == "" && h.store != nil {
			proxyURL = h.store.GetProxyURL()
		}
	}
	login := gin.H{"email": cfg.LoginEmail, "mode": cfg.CredentialMode, "engine": cfg.Engine, "password": pw, "totp_secret": totp, "otp_url": otp, "proxy_url": proxyURL}
	if cfg.Engine == "session_studio" {
		login["relogin_endpoint"] = os.Getenv("CODEX2API_CREDENTIAL_OPS_SESSION_STUDIO_ENDPOINT")
		var headers map[string]string
		if value := os.Getenv("CODEX2API_CREDENTIAL_OPS_SESSION_STUDIO_HEADERS"); value != "" {
			if json.Unmarshal([]byte(value), &headers) != nil {
				_ = h.db.FailCredentialOpsTask(c.Request.Context(), task.ID, req.WorkerID, task.Attempt)
				c.Status(204)
				return
			}
		}
		login["relogin_headers"] = headers
	}
	c.JSON(200, gin.H{"task": task, "login": login})
}

type credentialWorkerRequest struct {
	WorkerID   string         `json:"worker_id"`
	Attempt    int            `json:"attempt"`
	Status     string         `json:"status"`
	Stage      string         `json:"stage"`
	Credential map[string]any `json:"credential"`
}

func (h *Handler) RenewCredentialOpsWorker(c *gin.Context) {
	var req credentialWorkerRequest
	id, _ := strconv.ParseInt(c.Param("job_id"), 10, 64)
	if c.ShouldBindJSON(&req) != nil || req.WorkerID == "" || req.Attempt <= 0 {
		c.Status(400)
		return
	}
	if req.Stage == "" {
		req.Stage = "protocol_login"
	}
	if len(req.Stage) > 64 {
		c.Status(400)
		return
	}
	if h.db.RenewCredentialOpsTask(c.Request.Context(), id, req.WorkerID, req.Attempt, req.Stage) != nil {
		c.Status(409)
		return
	}
	c.Status(204)
}
func (h *Handler) CompleteCredentialOpsWorker(c *gin.Context) {
	var req credentialWorkerRequest
	id, _ := strconv.ParseInt(c.Param("job_id"), 10, 64)
	if c.ShouldBindJSON(&req) != nil || req.WorkerID == "" || req.Attempt <= 0 {
		c.Status(400)
		return
	}
	task, err := h.db.GetCredentialOpsTask(c.Request.Context(), id)
	if err != nil || task == nil || task.Status != "running" || task.LeaseOwner != req.WorkerID || task.Attempt != req.Attempt || task.LeaseUntil == nil || !task.LeaseUntil.After(time.Now()) {
		c.Status(409)
		return
	}
	if req.Status == "failed" {
		if h.db.FailCredentialOpsTask(c.Request.Context(), id, req.WorkerID, req.Attempt) != nil {
			c.Status(409)
			return
		}
		c.Status(204)
		return
	}
	if req.Status != "succeeded" {
		c.Status(400)
		return
	}
	cfg, err := h.db.CredentialOpsTaskConfig(c.Request.Context(), id)
	if err != nil {
		c.Status(500)
		return
	}
	str := func(k string) string { v, _ := req.Credential[k].(string); return strings.TrimSpace(v) }
	email, workspace := openaiidentity.TokenIdentity(str("id_token"), str("access_token"))
	if email == "" || workspace == "" || !strings.EqualFold(email, cfg.LoginEmail) || str("refresh_token") == "" || str("access_token") == "" || str("id_token") == "" {
		_ = h.db.FailCredentialOpsTask(c.Request.Context(), id, req.WorkerID, req.Attempt)
		c.JSON(400, gin.H{"error": "incomplete or mismatched OAuth identity"})
		return
	}
	access := auth.ParseAccessToken(str("access_token"))
	if access == nil || access.ExpiresAt.IsZero() || !access.ExpiresAt.After(time.Now()) {
		_ = h.db.FailCredentialOpsTask(c.Request.Context(), id, req.WorkerID, req.Attempt)
		c.JSON(400, gin.H{"error": "invalid or expired access token"})
		return
	}
	accessEmail, accessWorkspace := openaiidentity.TokenIdentity("", str("access_token"))
	if accessEmail != "" && (!strings.EqualFold(accessEmail, email) || accessWorkspace != workspace) {
		_ = h.db.FailCredentialOpsTask(c.Request.Context(), id, req.WorkerID, req.Attempt)
		c.JSON(400, gin.H{"error": "OAuth token identities disagree"})
		return
	}
	seed := normalizeTokenCredentialSeed(tokenCredentialSeed{refreshToken: str("refresh_token"), accessToken: str("access_token"), idToken: str("id_token"), email: email, workspaceID: workspace, expiresAtRaw: str("expires_at")})
	if !seed.expiresAt.After(time.Now()) {
		_ = h.db.FailCredentialOpsTask(c.Request.Context(), id, req.WorkerID, req.Attempt)
		c.JSON(400, gin.H{"error": "expired OAuth token"})
		return
	}
	if !h.PluginEnabled(c.Request.Context(), "credential-ops") {
		c.Status(409)
		return
	}
	accountID, err := h.db.CommitCredentialOpsLogin(c.Request.Context(), id, req.WorkerID, req.Attempt, tokenCredentialMap(seed), h.newCodexAccountCredentials(seed))
	if err != nil {
		_ = h.db.FailCredentialOpsTask(c.Request.Context(), id, req.WorkerID, req.Attempt)
		c.JSON(409, gin.H{"error": err.Error()})
		return
	}
	if err = h.reloadTokenAccount(c.Request.Context(), accountID, "credential_ops"); err != nil {
		c.JSON(503, gin.H{"error": "credentials committed; runtime reload pending", "account_id": accountID})
		return
	}
	h.db.InsertAccountEventAsync(accountID, "updated", "credential_ops")
	c.JSON(200, gin.H{"status": "succeeded", "account_id": accountID})
}
func (h *Handler) credentialOpsGate(c *gin.Context) {
	if !h.PluginEnabled(c.Request.Context(), "credential-ops") {
		c.AbortWithStatusJSON(409, gin.H{"error": "credential operations plugin is disabled"})
		return
	}
	c.Next()
}
func (h *Handler) RegisterCredentialOpsRoutes(api *gin.RouterGroup) {
	api.GET("/accounts/:id/credential-ops/config", h.GetCredentialOpsConfig)
	api.PUT("/accounts/:id/credential-ops/config", h.credentialOpsGate, h.SaveCredentialOpsConfig)
	api.POST("/accounts/:id/credential-ops/login", h.credentialOpsGate, h.StartCredentialOpsLogin)
	api.POST("/credential-ops/import", h.credentialOpsGate, h.StartCredentialOpsImport)
	api.GET("/credential-ops/login/:job_id", h.GetCredentialOpsLogin)
	api.DELETE("/credential-ops/login/:job_id", h.CancelCredentialOpsLogin)
	h.registerCredentialMonitorRoutes(api)
}

// Register outside the admin group: the dedicated worker token is sufficient.
func (h *Handler) RegisterCredentialOpsWorkerRoutes(router *gin.Engine) {
	g := router.Group("/api/internal/credential-ops", func(c *gin.Context) {
		if !credentialOpsWorkerAuthorized(c) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		c.Next()
	}, h.credentialOpsGate)
	g.POST("/claim", h.ClaimCredentialOpsWorker)
	g.POST("/login/:job_id/renew", h.RenewCredentialOpsWorker)
	g.POST("/login/:job_id/complete", h.CompleteCredentialOpsWorker)
}
