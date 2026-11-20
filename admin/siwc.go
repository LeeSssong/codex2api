package admin

import (
	"net/http"
	"strconv"

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
	if account := h.store.FindByID(row.ID); account != nil {
		account.SetSIWCEnabled(*input.Enabled)
	}
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
