package admin

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/codex2api/smartops"
	"github.com/gin-gonic/gin"
)

// SmartOpsConfigResponse is the stable admin transport shape. Root routing may
// embed these handlers without changing the existing Handler contract.
type SmartOpsConfigResponse struct {
	OAuth    smartops.OAuthAutoConfig `json:"oauth_auto_config"`
	Priority smartops.PriorityConfig  `json:"priority_scheduling"`
	Plugins  map[string]bool          `json:"plugins"`
}

// RegisterSmartOpsRoutes mounts the complete smart-ops API under an existing
// authenticated admin group. The caller supplies the native plugin gate and
// probe/history adapters when constructing Runtime.
func RegisterSmartOpsRoutes(r *gin.RouterGroup, runtime *smartops.Runtime) {
	r.GET("/smart-ops", func(c *gin.Context) {
		oauth, priority, err := runtime.Config(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, SmartOpsConfigResponse{OAuth: oauth, Priority: priority, Plugins: map[string]bool{"auto-config": true, "priority-scheduling": true, "pelican-tests": true}})
	})
	r.PUT("/smart-ops/oauth-auto-config", func(c *gin.Context) {
		cfg, err := DecodeOAuthAutoConfig(c.Request)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err = runtime.SetOAuth(c.Request.Context(), cfg); err != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}
		c.Status(http.StatusNoContent)
	})
	r.PUT("/smart-ops/priority-scheduling", func(c *gin.Context) {
		cfg, err := DecodePriorityConfig(c.Request)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err = runtime.SetPriority(c.Request.Context(), cfg); err != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}
		c.Status(http.StatusNoContent)
	})
	r.GET("/smart-ops/pelican-tests", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"jobs": runtime.Jobs(c.Request.Context())}) })
	r.POST("/smart-ops/pelican-tests", func(c *gin.Context) {
		var job smartops.PelicanJob
		if err := c.ShouldBindJSON(&job); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		rec, err := runtime.CreateJob(c.Request.Context(), job)
		if err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusAccepted, rec)
	})
	r.POST("/smart-ops/pelican-tests/:id/cancel", func(c *gin.Context) {
		var id int64
		if _, err := fmt.Sscan(c.Param("id"), &id); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
			return
		}
		if err := runtime.CancelJob(c.Request.Context(), id); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		c.Status(http.StatusNoContent)
	})
}

func WriteSmartOpsConfig(w http.ResponseWriter, response SmartOpsConfigResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func DecodeOAuthAutoConfig(r *http.Request) (smartops.OAuthAutoConfig, error) {
	var c smartops.OAuthAutoConfig
	err := json.NewDecoder(r.Body).Decode(&c)
	if err == nil {
		err = smartops.ValidateOAuthAutoConfig(c)
	}
	return c, err
}
func DecodePriorityConfig(r *http.Request) (smartops.PriorityConfig, error) {
	var c smartops.PriorityConfig
	err := json.NewDecoder(r.Body).Decode(&c)
	if err == nil {
		err = smartops.ValidatePriorityConfig(c)
	}
	return c, err
}
