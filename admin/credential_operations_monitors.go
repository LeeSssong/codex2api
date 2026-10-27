package admin

import (
	"context"
	"errors"
	"fmt"
	"github.com/codex2api/credentialops"
	"strconv"
	"sync"
	"time"

	"github.com/codex2api/database"
	"github.com/codex2api/proxy"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func (h *Handler) ListCredentialOps(c *gin.Context) {
	_ = h.db.ReconcileCredentialOpsExpiredTOTP(c.Request.Context())
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
	runtime, _ := h.db.CredentialOpsRuntime(c.Request.Context())
	rows := []gin.H{}
	for _, a := range accounts {
		cfg, _ := h.db.GetCredentialOpsLoginConfig(c.Request.Context(), a.AccountID)
		var view any
		effectiveEngine := runtime.Engine
		egress := "account"
		if cfg != nil {
			view = configView(cfg)
			if cfg.Engine != "inherit" {
				effectiveEngine = cfg.Engine
			}
			egress = cfg.ProxySource
			if cfg.ProxySource == "proxy" {
				egress = fmt.Sprintf("proxy #%d", cfg.ProxyID)
			}
		}

		native, _ := h.db.GetAccountByID(c.Request.Context(), a.AccountID)
		email := ""
		if native != nil {
			email = native.GetCredential("email")
		}
		if cfg == nil {
			a.BlockedReason = "login configuration is missing"
		} else if cfg.CredentialMode == "password_totp" && cfg.PasswordCiphertext == "" {
			a.BlockedReason = "login password is missing"
		} else if cfg.CredentialMode == "email_otp_url" && cfg.OTPURLCiphertext == "" {
			a.BlockedReason = "email OTP URL is missing"
		}
		if recovery, _ := h.db.GetCredentialOpsTOTPRecovery(c.Request.Context(), a.AccountID); recovery != nil && recovery.State != "adopted" && recovery.State != "old_verified" {
			a.BlockedReason = "2FA recovery requires confirmation"
		}
		if effectiveEngine == "session_studio" && cfg != nil && cfg.ProxySource != "direct" {
			a.BlockedReason = "Session Studio requires an explicit direct login route"
		}
		rows = append(rows, gin.H{"account_id": a.AccountID, "name": a.Name, "email": email, "enabled": a.Enabled, "auto_relogin": a.AutoRelogin, "probe_state": a.State, "probe_detail": a.Detail, "fail_streak": a.FailStreak, "next_probe_at": a.NextProbeAt, "cooldown_until": a.CooldownUntil, "last_probe_at": a.LastProbeAt, "last_reauth_at": a.LastReauthAt, "blocked_reason": a.BlockedReason, "effective_engine": effectiveEngine, "effective_egress": egress, "config": view})
	}
	encrypted, source := credentialops.EncryptionStatus()
	c.JSON(200, gin.H{"accounts": rows, "tasks": tasks, "rules": rules, "login_configured": ready, "enabled": runtime.Enabled, "runtime_settings": runtime, "worker": h.credentialWorkerStatus(), "encryption": gin.H{"configured": encrypted, "source": source}, "session_studio_configured": runtime.SessionStudioEndpoint != ""})
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
	if err != nil || !database.CredentialOperationsEligible(row) {
		c.Status(404)
		return
	}
	var m database.CredentialOpsMonitorRow
	if c.ShouldBindJSON(&m) != nil {
		c.Status(400)
		return
	}
	rules, _ := h.db.CredentialOpsGlobalRules(c.Request.Context())
	m.IntervalSeconds = rules.IntervalSeconds
	m.FailureThreshold = rules.FailureThreshold
	m.CooldownSeconds = rules.CooldownSeconds
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
	if !h.credentialOperationsEnabled(ctx) {
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
			resolved, usable := h.store.ResolveUsableProxyForAccount(account)
			if !usable {
				_, _ = h.db.CompleteCredentialOpsMonitor(ctx, m, "transient")
				return
			}
			probeCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
			var err error
			if h.probeUsage != nil {
				err = h.probeUsage(probeCtx, account)
			} else {
				_, err = proxy.FetchCodexModelsManifest(probeCtx, account, resolved, "", "", nil)
			}
			cancel()
			state = credentialops.ClassifyProbe(account.GetAccessToken() != "", err)

		}
	}
	if !h.credentialOperationsEnabled(ctx) {
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
	resolved, e := h.credentialOpsProxy(ctx, m.AccountID, *cfg)
	if e != nil {
		return
	}
	cfg.ResolvedProxyURL = resolved
	cfg.RouteResolved = true
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
		nextExcel := time.Time{}
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !h.credentialOperationsEnabled(ctx) {
					continue
				}
				if time.Now().After(nextExcel) {
					_ = h.db.ReconcileCredentialOpsExpiredTOTP(ctx)
					h.queueMissingCredentialExcel(ctx)
					nextExcel = time.Now().Add(time.Minute)
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

func (h *Handler) queueMissingCredentialExcel(ctx context.Context) {
	if h.store == nil {
		return
	}
	ids, e := h.db.ListCredentialOpsMissingExcelAccounts(ctx, proxy.CurrentRuntimeSettings().CodexBasispointsEnabled)
	if e != nil {
		return
	}
	for _, id := range ids {
		cfg, e := h.db.GetCredentialOpsLoginConfig(ctx, id)
		if e != nil || cfg == nil {
			continue
		}
		row, e := h.db.GetAccountByID(ctx, id)
		if e != nil || row == nil || !database.CredentialOperationsExcelEligible(row) {
			continue
		}
		resolved, e := h.credentialOpsProxy(ctx, id, *cfg)
		if e != nil {
			continue
		}
		cfg.Engine = "local_worker"
		cfg.OAuthProfile = "excel"
		cfg.ResolvedProxyURL = resolved
		cfg.RouteResolved = true
		_, _ = h.db.CreateCredentialOpsTaskWithConfig(ctx, id, row.CredentialGeneration, *cfg)
	}
}
