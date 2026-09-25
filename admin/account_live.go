package admin

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

type accountLiveItem struct {
	StateModels      []accountStateModel `json:"state_models,omitempty"`
	ActiveRequests   int64               `json:"active_requests"`
	OccupiedRequests int64               `json:"occupied_requests"`
}

// GetAccountLiveState returns request-local runtime counters for the visible
// account page. It intentionally reads only in-memory atomics, so frequent UI
// polling does not touch the database or rebuild the paged account snapshot.
func (h *Handler) GetAccountLiveState(c *gin.Context) {
	ids, err := parseAccountListIDs(c.Query("ids"))
	if err != nil {
		writeError(c, http.StatusBadRequest, "ids 参数无效")
		return
	}
	if len(ids) > accountListPageMax {
		writeError(c, http.StatusBadRequest, "ids 最多允许 500 个")
		return
	}

	live := make(map[int64]accountLiveItem, len(ids))
	state := h.stateSnapshot()
	for _, id := range ids {
		account := h.store.FindByID(id)
		if account == nil {
			continue
		}
		live[id] = accountLiveItem{
			StateModels:      state.Accounts[id],
			ActiveRequests:   account.GetActiveRequests(),
			OccupiedRequests: account.GetOccupiedRequests(),
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"state_summary":               state.Summary,
		"server_time":                 time.Now().Unix(),
		"accounts":                    live,
		"session_slot_buffer_enabled": h.store.SessionSlotBufferEnabled(),
	})
}
