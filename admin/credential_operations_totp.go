package admin

import (
	"fmt"
	"github.com/codex2api/credentialops"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
	"net/url"
	"strconv"
	"strings"
)

func (h *Handler) GetCredentialOpsTOTP(c *gin.Context) {
	_ = h.db.ReconcileCredentialOpsExpiredTOTP(c.Request.Context())
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	r, e := h.db.GetCredentialOpsTOTPRecovery(c.Request.Context(), id)
	if e != nil {
		c.Status(500)
		return
	}
	if r == nil {
		c.JSON(200, gin.H{"task_id": 0, "status": "idle", "candidate_available": false, "previous_available": false})
		return
	}
	c.JSON(200, gin.H{"task_id": r.TaskID, "status": r.State, "phase": r.State, "candidate_available": r.CandidateCiphertext != "", "previous_available": r.PreviousCiphertext != "", "updated_at": r.UpdatedAt})
}
func (h *Handler) StartCredentialOpsTOTP(c *gin.Context) { h.queueCredentialOpsTOTP(c, "rotate") }
func (h *Handler) VerifyCredentialOpsTOTP(c *gin.Context) {
	var in struct {
		Action string `json:"action"`
	}
	if c.ShouldBindJSON(&in) != nil || (in.Action != "verify_new" && in.Action != "verify_old") {
		c.Status(400)
		return
	}
	h.queueCredentialOpsTOTP(c, in.Action)
}
func (h *Handler) queueCredentialOpsTOTP(c *gin.Context, action string) {
	_ = h.db.ReconcileCredentialOpsExpiredTOTP(c.Request.Context())
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	cfg, e := h.db.GetCredentialOpsLoginConfig(c.Request.Context(), id)
	if e != nil || cfg == nil {
		c.Status(404)
		return
	}
	resolved, e := h.credentialOpsProxy(c.Request.Context(), id, *cfg)
	if e != nil {
		c.JSON(409, gin.H{"error": "configured egress unavailable"})
		return
	}
	task, e := h.db.CreateCredentialOpsTOTPTask(c.Request.Context(), id, action, database.CredentialOpsRetryRoute{ProxyURL: resolved, ConfigRevision: cfg.Revision})
	if e != nil {
		c.JSON(409, gin.H{"error": e.Error()})
		return
	}
	r, _ := h.db.GetCredentialOpsTOTPRecovery(c.Request.Context(), id)
	c.JSON(202, gin.H{"task_id": task.ID, "status": task.Status, "phase": task.Stage, "candidate_available": r != nil && r.CandidateCiphertext != "", "previous_available": r != nil && r.PreviousCiphertext != ""})
}
func (h *Handler) ExportCredentialOpsTOTP(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var in struct {
		Source          string `json:"source"`
		IncludePassword bool   `json:"include_password"`
	}
	if c.ShouldBindJSON(&in) != nil {
		c.Status(400)
		return
	}
	cfg, e := h.db.GetCredentialOpsLoginConfig(c.Request.Context(), id)
	if e != nil || cfg == nil {
		c.Status(404)
		return
	}
	cipher := cfg.TOTPCiphertext
	if in.Source == "candidate" || in.Source == "previous" {
		r, e := h.db.GetCredentialOpsTOTPRecovery(c.Request.Context(), id)
		if e != nil || r == nil {
			c.Status(404)
			return
		}
		cfg.LoginEmail = r.LoginEmail
		cfg.PasswordCiphertext = r.PasswordCiphertext
		cipher = r.CandidateCiphertext
		if in.Source == "previous" {
			cipher = r.PreviousCiphertext
		}
	} else if in.Source != "current" {
		c.Status(400)
		return
	}
	_, dec := credentialops.EnvCrypto()
	secret, e := dec(cipher)
	if e != nil || secret == "" {
		c.JSON(409, gin.H{"error": "recovery credential unavailable"})
		return
	}
	view := gin.H{"email": cfg.LoginEmail, "totp_secret": secret, "otpauth_uri": fmt.Sprintf("otpauth://totp/%s?secret=%s&issuer=OpenAI", url.PathEscape(cfg.LoginEmail), url.QueryEscape(secret))}
	if in.IncludePassword {
		password, e := dec(cfg.PasswordCiphertext)
		if e != nil {
			c.Status(409)
			return
		}
		view["password"] = password
	}
	c.JSON(200, view)
}
func (h *Handler) CredentialOpsWorkerTOTPPhase(c *gin.Context) {
	var in struct {
		WorkerID string `json:"worker_id"`
		Attempt  int    `json:"attempt"`
		Phase    string `json:"phase"`
		Secret   string `json:"secret"`
	}
	id, _ := strconv.ParseInt(c.Param("job_id"), 10, 64)
	if c.ShouldBindJSON(&in) != nil || in.WorkerID == "" || in.Attempt < 1 {
		c.Status(400)
		return
	}
	if e := h.db.CredentialOpsTOTPPhase(c.Request.Context(), id, in.WorkerID, in.Attempt, in.Phase, in.Secret); e != nil {
		c.Status(409)
		return
	}
	c.Status(204)
}
func (h *Handler) CredentialOpsWorkerTOTPFinish(c *gin.Context) {
	var in struct {
		WorkerID string `json:"worker_id"`
		Attempt  int    `json:"attempt"`
		Success  bool   `json:"success"`
	}
	id, _ := strconv.ParseInt(c.Param("job_id"), 10, 64)
	if c.ShouldBindJSON(&in) != nil || in.WorkerID == "" || in.Attempt < 1 {
		c.Status(400)
		return
	}
	if e := h.db.CredentialOpsTOTPFinish(c.Request.Context(), id, in.WorkerID, in.Attempt, in.Success); e != nil {
		c.Status(409)
		return
	}
	c.Status(204)
}
func (h *Handler) CredentialOpsWorkerTOTPRecover(c *gin.Context) {
	var in struct {
		Secret string `json:"secret"`
	}
	id, _ := strconv.ParseInt(c.Param("job_id"), 10, 64)
	if c.ShouldBindJSON(&in) != nil || in.Secret == "" {
		c.Status(400)
		return
	}
	if e := h.db.RecoverCredentialOpsTOTPCandidate(c.Request.Context(), id, strings.TrimSpace(in.Secret)); e != nil {
		c.Status(409)
		return
	}
	c.Status(204)
}
func (h *Handler) registerCredentialTOTPRoutes(api *gin.RouterGroup) {
	api.GET("/accounts/:id/credential-ops/totp", h.GetCredentialOpsTOTP)
	api.POST("/accounts/:id/credential-ops/totp", h.StartCredentialOpsTOTP)
	api.POST("/accounts/:id/credential-ops/totp/verify", h.VerifyCredentialOpsTOTP)
	api.POST("/accounts/:id/credential-ops/totp/export", h.ExportCredentialOpsTOTP)
}
