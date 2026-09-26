package admin

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
)

func codexViewsFromRecords(records *database.CodexRouteRecords, model string, account *auth.Account, now time.Time) []auth.CodexPathSnapshot {
	views := []auth.CodexPathSnapshot{}
	for _, path := range []string{database.CodexPathNative, database.CodexPathBasispoints} {
		models := []string{model}
		if model == "" {
			seen := map[string]bool{"": true}
			if records != nil {
				for _, f := range records.Facts {
					if f.Upstream == path && !seen[f.Model] {
						models = append(models, f.Model)
						seen[f.Model] = true
					}
				}
			}
			if account != nil {
				for _, v := range account.CodexPathViews("", now) {
					if v.Upstream == path && !seen[v.Model] {
						models = append(models, v.Model)
						seen[v.Model] = true
					}
				}
			}
			sort.Strings(models)
		}
		for _, m := range models {
			v := auth.CodexPathSnapshot{Upstream: path, Model: m, Allowed: true, Capability: database.CapabilityUnknown, Health: "ready"}
			if account != nil {
				v = account.CodexPathSnapshot(path, m, now)
			}
			if records != nil {
				for _, c := range records.Paths {
					if c.Upstream == path {
						v.Allowed = c.Allowed
					}
				}
				var exact, global *database.CodexCapability
				for i := range records.Facts {
					f := &records.Facts[i]
					if f.Upstream != path {
						continue
					}
					if f.Model == m {
						exact = f
					}
					if f.Model == "" {
						global = f
					}
				}
				f := exact
				if global != nil && (f == nil || global.Capability == database.CapabilityUnsupported) {
					f = global
				}
				if f != nil {
					v.Capability, v.Source, v.Reason, v.ObservedAt = f.Capability, f.Source, f.Reason, f.ObservedAt
				}
			}
			views = append(views, v)
		}
	}
	return views
}

func (h *Handler) codexCapabilityPredicate(ctx context.Context, filter, model string) (func(int64) bool, error) {
	filter = strings.TrimSpace(filter)
	model = strings.ToLower(strings.TrimSpace(model))
	if filter == "" || filter == "all" {
		return func(int64) bool { return true }, nil
	}
	switch filter {
	case "bps_supported", "bps_unsupported", "bps_unknown", "dual_supported", "codex_only_supported", "bps_only_supported", "cooldown", "admin_disabled":
	default:
		return nil, fmt.Errorf("invalid Codex capability filter")
	}
	// A model is mandatory: evidence about one model cannot label the entire account.
	if model == "" {
		return nil, fmt.Errorf("capability_model is required for capability filtering")
	}
	records, err := h.db.ListCodexRouteRecords(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	return func(id int64) bool {
		a := h.store.FindByID(id)
		if a != nil && (a.IsRelayStyle() || a.IsCodexAgentIdentity()) {
			return false
		}
		v := codexViewsFromRecords(records[id], model, a, now)
		native, bps := v[0], v[1]
		switch filter {
		case "bps_supported":
			return bps.Capability == database.CapabilitySupported
		case "bps_unsupported":
			return bps.Capability == database.CapabilityUnsupported
		case "bps_unknown":
			return bps.Capability == database.CapabilityUnknown
		case "dual_supported":
			return bps.Capability == database.CapabilitySupported && native.Capability == database.CapabilitySupported
		case "codex_only_supported":
			return native.Capability == database.CapabilitySupported && bps.Capability == database.CapabilityUnsupported
		case "bps_only_supported":
			return bps.Capability == database.CapabilitySupported && native.Capability == database.CapabilityUnsupported
		case "cooldown":
			return native.Health == "cooldown" || bps.Health == "cooldown" || native.Health == "recovering" || bps.Health == "recovering"
		case "admin_disabled":
			return !native.Allowed || !bps.Allowed
		}
		return false
	}, nil
}

func (h *Handler) UpdateCodexRoutes(c *gin.Context) {
	var req struct {
		IDs      []int64                            `json:"ids"`
		Upstream string                             `json:"upstream"`
		Allowed  *bool                              `json:"allowed"`
		Reset    bool                               `json:"reset_observations"`
		Policy   *database.BasispointsAccountPolicy `json:"basispoints_policy"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, 400, "Invalid route configuration")
		return
	}
	ids := positiveUniqueAdminIDs(req.IDs)
	if len(ids) == 0 || len(ids) > 100 || !database.ValidCodexPath(req.Upstream) || req.Allowed == nil && !req.Reset && req.Policy == nil {
		writeError(c, 400, "Select 1–100 Codex OAuth accounts, an upstream and an action")
		return
	}
	if req.Policy != nil {
		if req.Upstream != database.CodexPathBasispoints || req.Policy.Validate() != nil {
			writeError(c, 400, "Invalid Basispoints account policy")
			return
		}
	}
	ctx := c.Request.Context()
	// Validate the complete batch before changing anything.
	for _, id := range ids {
		row, err := h.db.GetAccountByID(ctx, id)
		if err != nil || row == nil || row.GetCredential("upstream_type") != "" || row.GetCredential("auth_mode") == auth.CodexAuthModeAgentIdentity {
			writeError(c, 400, "Route management requires existing Codex OAuth accounts")
			return
		}
	}
	for _, id := range ids {
		var err error
		if req.Upstream == database.CodexPathBasispoints && (req.Allowed != nil || req.Policy != nil) {
			err = h.db.UpdateBasispointsAccountRoute(ctx, id, req.Allowed, req.Policy)
		} else if req.Allowed != nil {
			err = h.db.SetCodexPathAllowed(ctx, id, req.Upstream, *req.Allowed)
		}
		if err == nil && req.Reset {
			err = h.db.ResetCodexCapabilities(ctx, id, req.Upstream, time.Now())
		}
		if err != nil {
			writeInternalError(c, err)
			return
		}
		if a := h.store.FindByID(id); a != nil {
			if err := a.ReloadCodexRoutes(ctx); err != nil {
				writeInternalError(c, err)
				return
			}
			if req.Reset {
				a.ClearCodexPathHealth(req.Upstream)
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"updated": len(ids)})
}

func (h *Handler) GetCodexRoutes(c *gin.Context) {
	// The ordinary account endpoint already validates IDs and hides credentials.
	id, err := parseCodexRouteID(c.Param("id"))
	if err != nil {
		writeError(c, 400, err.Error())
		return
	}
	row, err := h.db.GetAccountByID(c.Request.Context(), id)
	if err != nil || row == nil {
		writeError(c, 404, "Account not found")
		return
	}
	configs, facts, err := h.db.GetCodexRoutes(c.Request.Context(), id)
	if err != nil {
		writeInternalError(c, err)
		return
	}
	policy, err := h.db.GetBasispointsAccountPolicy(c.Request.Context(), id)
	if err != nil {
		writeInternalError(c, err)
		return
	}
	model := strings.ToLower(strings.TrimSpace(c.Query("model")))
	probes, err := h.codexProbeResults(c.Request.Context(), id, model)
	if err != nil {
		writeInternalError(c, err)
		return
	}
	c.JSON(200, gin.H{"paths": codexViewsFromRecords(&database.CodexRouteRecords{Paths: configs, Facts: facts}, model, h.store.FindByID(id), time.Now()), "probes": probes, "basispoints_policy": policy})
}

func parseCodexRouteID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid account ID")
	}
	return id, nil
}

func codexCapabilityRowEligible(row *database.AccountRow, filter string) bool {
	if filter == "" || filter == "all" {
		return true
	}
	return row != nil && row.GetCredential("upstream_type") == "" && row.GetCredential("auth_mode") != auth.CodexAuthModeAgentIdentity
}
