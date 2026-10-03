package admin

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/codex2api/database"
	"github.com/codex2api/proxy"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"strings"
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
	rules, err := h.db.CredentialOpsGlobalRules(c.Request.Context())
	if err != nil {
		c.Status(500)
		return
	}
	ready := map[int64]bool{}
	for _, a := range accounts {
		cfg, e := h.db.GetCredentialOpsLoginConfig(c.Request.Context(), a.AccountID)
		if e != nil {
			c.Status(500)
			return
		}
		ready[a.AccountID] = cfg != nil && ((cfg.CredentialMode == "password_totp" && cfg.PasswordCiphertext != "") || (cfg.CredentialMode == "email_otp_url" && cfg.OTPURLCiphertext != ""))
	}
	c.JSON(200, gin.H{"accounts": accounts, "tasks": tasks, "rules": rules, "login_configured": ready, "enabled": h.PluginEnabled(c.Request.Context(), "credential-ops"), "session_studio_configured": os.Getenv("CODEX2API_CREDENTIAL_OPS_SESSION_STUDIO_ENDPOINT") != ""})
}
func (h *Handler) SaveCredentialGlobalRules(c *gin.Context) {
	var rules database.CredentialOpsGlobalRules
	if c.ShouldBindJSON(&rules) != nil {
		c.Status(400)
		return
	}
	if err := h.db.SaveCredentialOpsGlobalRules(c.Request.Context(), rules); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, rules)
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
			var err error
			if h.probeUsage != nil {
				err = h.probeUsage(probeCtx, account)
			} else {
				_, err = proxy.FetchCodexModelsManifest(probeCtx, account, h.store.ResolveProxyForAccount(account), "", "")
			}
			cancel()
			if err == nil {
				state = "ok"
			}
			if account.GetAccessToken() == "" {
				state = "auth"
			} else if err != nil {
				message := strings.ToLower(err.Error())
				for _, marker := range []string{"codex models upstream status 401:", "codex models upstream status 403:", "unauthorized", "invalid token", "invalid_token", "token expired", "invalid_grant", "requires re-login"} {
					if strings.Contains(message, marker) {
						state = "auth"
						break
					}
				}
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
	api.PUT("/credential-ops/rules", h.credentialOpsGate, h.SaveCredentialGlobalRules)
	api.PUT("/accounts/:id/credential-ops/monitor", h.credentialOpsGate, h.SaveCredentialMonitor)
	api.POST("/accounts/:id/credential-ops/probe", h.credentialOpsGate, h.ProbeCredentialMonitor)
}
