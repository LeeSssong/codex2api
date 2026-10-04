package admin

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/codex2api/smartops"
	"github.com/gin-gonic/gin"
)

const maxSmartOpsConcurrencyProgressIDs = 200

type smartOpsConcurrencyProgressResponse struct {
	Enabled  bool                                `json:"enabled"`
	Paused   bool                                `json:"paused"`
	Reason   string                              `json:"reason"`
	Progress map[int64]smartops.ConcurrencyState `json:"progress"`
}

func parseSmartOpsConcurrencyProgressIDs(raw string) ([]int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	if len(parts) > maxSmartOpsConcurrencyProgressIDs {
		return nil, fmt.Errorf("ids must contain at most %d entries", maxSmartOpsConcurrencyProgressIDs)
	}
	ids := make([]int64, 0, len(parts))
	seen := make(map[int64]struct{}, len(parts))
	for _, part := range parts {
		id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("ids must contain positive integer IDs")
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, fmt.Errorf("ids must be distinct")
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

func (h *Handler) smartOpsConcurrencyProgress(c *gin.Context) {
	ids, err := parseSmartOpsConcurrencyProgressIDs(c.Query("ids"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	cfg, err := h.db.LoadOAuthAutoConfig(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	enabled := h.PluginEnabled(c.Request.Context(), smartops.PluginAutoConfig) && cfg.UpgradeEnabled
	response := smartOpsConcurrencyProgressResponse{Enabled: enabled, Progress: map[int64]smartops.ConcurrencyState{}}
	if !enabled {
		c.JSON(http.StatusOK, response)
		return
	}
	if h.store != nil {
		response.Paused, response.Reason = h.store.SmartOpsConcurrencyStatus()
	}
	if len(ids) == 0 {
		c.JSON(http.StatusOK, response)
		return
	}
	response.Progress, err = h.db.LoadSmartOpsConcurrencyProgress(c.Request.Context(), ids, cfg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, response)
}
