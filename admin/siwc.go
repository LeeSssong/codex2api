package admin

import (
	"net/http"
	"net/mail"
	"strconv"
	"strings"

	"github.com/codex2api/credentialops/siwc"
	"github.com/codex2api/database"
	"github.com/codex2api/proxy"
	"github.com/gin-gonic/gin"
)

func (h *Handler) siwcAccount(c *gin.Context) (*database.AccountRow, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(c, 400, "invalid account")
		return nil, false
	}
	row, err := h.db.GetAccountByID(c.Request.Context(), id)
	if err != nil || row == nil {
		writeError(c, 404, "account unavailable")
		return nil, false
	}
	if !database.CredentialOperationsEligible(row) {
		writeError(c, 400, "SIWC requires an OpenAI OAuth account")
		return nil, false
	}
	return row, true
}

func (h *Handler) GetSIWCState(c *gin.Context) {
	row, ok := h.siwcAccount(c)
	if !ok {
		return
	}
	stored, err := h.db.GetCredentialOpsProfileGrant(c.Request.Context(), row.ID, "siwc")
	if err != nil {
		writeError(c, 503, "SIWC credential storage unavailable")
		return
	}
	state := gin.H{"bound": stored != nil, "enabled": row.GetCredentialBool("siwc_enabled"), "native_available": row.GetCredential("refresh_token") != "", "status": "not_bound"}
	if stored != nil {
		state["status"] = "ready"
		grant, err := siwc.DecodeGrant(stored.Credentials)
		if err != nil || stored.Invalidated {
			state["status"] = "reauthorization_required"
		} else {
			state["expires_at"] = grant.ExpiresAt
			state["email"] = grant.Email
		}
		if stored.RefreshPending {
			state["status"] = "refresh_pending"
		}
	}
	c.JSON(http.StatusOK, state)
}

func (h *Handler) BeginSIWC(c *gin.Context) {
	row, ok := h.siwcAccount(c)
	if !ok {
		return
	}
	result, err := proxy.BeginSIWCAuthorization(c.Request.Context(), row.ID)
	if err != nil {
		writeError(c, 409, "Cannot begin SIWC authorization; verify account identity and egress")
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) CompleteSIWC(c *gin.Context) {
	row, ok := h.siwcAccount(c)
	if !ok {
		return
	}
	var input struct {
		SessionID string `json:"session_id"`
		Callback  string `json:"callback"`
	}
	if c.ShouldBindJSON(&input) != nil || input.SessionID == "" || input.Callback == "" {
		writeError(c, 400, "session and callback required")
		return
	}
	if err := proxy.CompleteSIWCAuthorization(c.Request.Context(), row.ID, input.SessionID, input.Callback); err != nil {
		writeError(c, 409, err.Error())
		return
	}
	h.GetSIWCState(c)
}

func (h *Handler) SelectSIWC(c *gin.Context) {
	row, ok := h.siwcAccount(c)
	if !ok {
		return
	}
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if c.ShouldBindJSON(&input) != nil || input.Enabled == nil {
		writeError(c, 400, "enabled is required")
		return
	}
	if *input.Enabled {
		stored, err := h.db.GetCredentialOpsProfileGrant(c.Request.Context(), row.ID, "siwc")
		if err != nil || stored == nil || stored.Invalidated || stored.RefreshPending {
			writeError(c, 409, "authorize SIWC first")
			return
		}
		if _, err = siwc.DecodeGrant(stored.Credentials); err != nil {
			writeError(c, 409, "SIWC credentials unavailable")
			return
		}
	}
	if err := h.db.UpdateCredentials(c.Request.Context(), row.ID, map[string]any{"siwc_enabled": *input.Enabled}); err != nil {
		writeError(c, 503, "cannot save protocol selection")
		return
	}
	h.store.ApplySIWCSelection(row.ID, *input.Enabled)
	h.GetSIWCState(c)
}

func (h *Handler) RevokeSIWC(c *gin.Context) {
	row, ok := h.siwcAccount(c)
	if !ok {
		return
	}
	stored, err := h.db.GetCredentialOpsProfileGrant(c.Request.Context(), row.ID, "siwc")
	if err != nil {
		writeError(c, 503, "SIWC credential storage unavailable")
		return
	}
	if stored != nil {
		changed, err := h.db.InvalidateCredentialOpsProfileGrantCAS(c.Request.Context(), row.ID, "siwc", stored.Generation)
		if err != nil || !changed {
			writeError(c, 409, "SIWC authorization changed; reload before revoking")
			return
		}
	}
	h.GetSIWCState(c)
}

// CreateSIWCAccount creates an inactive identity anchor; only a verified grant
// can be selected, and no native token is invented or copied from SIWC.
func (h *Handler) CreateSIWCAccount(c *gin.Context) {
	var input struct {
		Email      string `json:"email"`
		Name       string `json:"name"`
		ProxyID    int64  `json:"proxy_id"`
		Password   string `json:"password"`
		TOTPSecret string `json:"totp_secret"`
	}
	if c.ShouldBindJSON(&input) != nil {
		writeError(c, 400, "invalid SIWC account")
		return
	}
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	address, err := mail.ParseAddress(input.Email)
	if err != nil || address.Address != input.Email || len(input.Name) > 200 {
		writeError(c, 400, "valid email and name required")
		return
	}
	if input.Password != "" {
		if _, err := h.prepareCredentialConfig(c, 0, credentialOpsConfigRequest{Email: input.Email, Mode: "password_totp", Engine: "local_worker", ProxySource: "direct", Password: input.Password, TOTPSecret: input.TOTPSecret}); err != nil {
			writeError(c, 400, "invalid login credentials or encryption unavailable")
			return
		}
	}
	proxyURL := ""
	if input.ProxyID > 0 {
		selected, err := h.db.GetProxy(c.Request.Context(), input.ProxyID)
		if err != nil || selected == nil || !selected.Enabled {
			writeError(c, 400, "selected proxy unavailable")
			return
		}
		proxyURL = selected.URL
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = input.Email
	}
	id, err := h.db.InsertPendingAccountWithCredentials(c.Request.Context(), name, map[string]any{"email": input.Email, "siwc_account": true, "ipv6_egress_enabled": false}, proxyURL, "SIWC authorization pending", nil)
	if err != nil {
		writeError(c, 503, "cannot create SIWC account")
		return
	}
	if input.Password != "" {
		cfg, err := h.prepareCredentialConfig(c, id, credentialOpsConfigRequest{Email: input.Email, Mode: "password_totp", Engine: "local_worker", ProxySource: "account", Password: input.Password, TOTPSecret: input.TOTPSecret})
		if err == nil {
			err = h.db.UpsertCredentialOpsLoginConfig(c.Request.Context(), cfg)
		}
		if err != nil {
			c.JSON(409, gin.H{"id": id, "error": "account created; save login credentials before 2FA authorization"})
			return
		}
	}
	if err := h.store.LoadAccountByID(c.Request.Context(), id); err != nil {
		writeError(c, 503, "cannot load SIWC account")
		return
	}
	c.JSON(201, gin.H{"id": id})
}
