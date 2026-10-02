package admin

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func (h *Handler) ListCredentialOps(c *gin.Context) {
	accounts, err := h.db.ListCredentialOpsMonitors(c.Request.Context())
	if err != nil {
		c.Status(500)
		return
	}
	tasks, err := h.db.ListCredentialOpsTasks(c.Request.Context())
	if err != nil {
		c.Status(500)
		return
	}
	c.JSON(200, gin.H{"accounts": accounts, "tasks": tasks, "enabled": h.PluginEnabled(c.Request.Context(), "credential-ops"), "session_studio_configured": os.Getenv("CODEX2API_CREDENTIAL_OPS_SESSION_STUDIO_ENDPOINT") != ""})
}
func (h *Handler) SaveCredentialMonitor(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	row, err := h.db.GetAccountByID(c.Request.Context(), id)
	if err != nil || row == nil || row.Status == "deleted" || row.Platform != "openai" || row.Type != "oauth" {
		c.Status(404)
		return
	}
	var m database.CredentialOpsMonitorRow
	if c.ShouldBindJSON(&m) != nil {
		c.Status(400)
		return
	}
	m.AccountID = id
	if err = h.db.SaveCredentialOpsMonitor(c.Request.Context(), m); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.Status(204)
}
func (h *Handler) ProbeCredentialMonitor(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	m, err := h.db.ClaimCredentialOpsMonitor(c.Request.Context(), id, uuid.NewString())
	if err != nil {
		if errors.Is(err, database.ErrCredentialOpsDisabled) {
			c.Status(409)
			return
		}
		c.Status(500)
		return
	}
	if m == nil {
		c.JSON(409, gin.H{"error": "monitor not configured or probe in progress"})
		return
	}
	h.runCredentialProbe(c.Request.Context(), *m)
	c.Status(204)
}
func (h *Handler) runCredentialProbe(ctx context.Context, m database.CredentialOpsMonitorRow) {
	if !h.PluginEnabled(ctx, "credential-ops") {
		return
	}
	state := "transient"
	if h.store != nil {
		account := h.store.FindByID(m.AccountID)
		if account == nil {
			if err := h.store.LoadAccountByID(ctx, m.AccountID); err == nil {
				account = h.store.FindByID(m.AccountID)
			}
		}
		if account != nil {
			probeCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
			err := h.usageProbeFunc()(probeCtx, account)
			cancel()
			if err == nil {
				state = "ok"
			}
			row, e := h.db.GetAccountByID(ctx, m.AccountID)
			if e == nil && row != nil && row.CooldownReason == "unauthorized" {
				state = "auth"
			}
		}
	}
	if !h.PluginEnabled(ctx, "credential-ops") {
		return
	}
	relogin, err := h.db.CompleteCredentialOpsMonitor(ctx, m, state)
	if err != nil || !relogin {
		return
	}
	cfg, err := h.db.GetCredentialOpsLoginConfig(ctx, m.AccountID)
	if err != nil || cfg == nil || (cfg.CredentialMode == "password_totp" && cfg.PasswordCiphertext == "") || (cfg.CredentialMode == "email_otp_url" && cfg.OTPURLCiphertext == "") {
		return
	}
	row, err := h.db.GetAccountByID(ctx, m.AccountID)
	if err != nil || row == nil {
		return
	}
	_, _ = h.db.CreateCredentialOpsAutoTaskWithConfig(ctx, m.AccountID, row.CredentialGeneration, *cfg, m.LeaseOwner)
}
func (h *Handler) StartCredentialOpsScheduler(ctx context.Context) {
	state := &credentialSchedulerState{done: make(chan struct{})}
	if _, loaded := credentialSchedulers.LoadOrStore(h, state); loaded {
		return
	}
	go func() {
		defer close(state.done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !h.PluginEnabled(ctx, "credential-ops") {
					continue
				}
				m, err := h.db.ClaimCredentialOpsMonitor(ctx, 0, uuid.NewString())
				if err == nil && m != nil {
					h.runCredentialProbe(ctx, *m)
				}
			}
		}
	}()
}

type credentialSchedulerState struct{ done chan struct{} }

var credentialSchedulers sync.Map

func (h *Handler) WaitCredentialOpsScheduler(ctx context.Context) error {
	state, ok := credentialSchedulers.Load(h)
	if !ok {
		return nil
	}
	select {
	case <-state.(*credentialSchedulerState).done:
		credentialSchedulers.Delete(h)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (h *Handler) registerCredentialMonitorRoutes(api *gin.RouterGroup) {
	api.GET("/credential-ops", h.ListCredentialOps)
	api.PUT("/accounts/:id/credential-ops/monitor", h.credentialOpsGate, h.SaveCredentialMonitor)
	api.POST("/accounts/:id/credential-ops/probe", h.credentialOpsGate, h.ProbeCredentialMonitor)
}
