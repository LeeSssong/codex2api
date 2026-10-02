package admin

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/codex2api/proxy"
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
		c.JSON(http.StatusOK, SmartOpsConfigResponse{OAuth: oauth, Priority: priority, Plugins: map[string]bool{"auto-config": runtime.Enabled(c.Request.Context(), smartops.PluginAutoConfig), "priority-scheduling": runtime.Enabled(c.Request.Context(), smartops.PluginPriorityScheduling), "pelican-tests": runtime.Enabled(c.Request.Context(), smartops.PluginPelicanTests)}})
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
	r.GET("/smart-ops/pelican-tests", func(c *gin.Context) {
		jobs, err := runtime.Jobs(c.Request.Context())
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"jobs": jobs})
	})
	r.GET("/smart-ops/pelican-tests/:id", func(c *gin.Context) {
		var id int64
		if _, e := fmt.Sscan(c.Param("id"), &id); e != nil {
			c.Status(400)
			return
		}
		job, e := runtime.GetJob(c.Request.Context(), id)
		if e != nil {
			c.JSON(404, gin.H{"error": e.Error()})
			return
		}
		c.JSON(200, job)
	})
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
	r.GET("/smart-ops/pelican-plans", func(c *gin.Context) {
		plans, e := runtime.Plans(c.Request.Context())
		if e != nil {
			c.JSON(500, gin.H{"error": e.Error()})
			return
		}
		c.JSON(200, gin.H{"plans": plans})
	})
	for _, method := range []string{"POST", "PUT"} {
		r.Handle(method, "/smart-ops/pelican-plans", func(c *gin.Context) {
			var p smartops.PelicanPlan
			if e := c.ShouldBindJSON(&p); e != nil {
				c.JSON(400, gin.H{"error": e.Error()})
				return
			}
			p, e := runtime.SavePlan(c.Request.Context(), p)
			if e != nil {
				c.JSON(400, gin.H{"error": e.Error()})
				return
			}
			c.JSON(200, p)
		})
	}
	r.DELETE("/smart-ops/pelican-plans/:id", func(c *gin.Context) {
		var id int64
		if _, e := fmt.Sscan(c.Param("id"), &id); e != nil {
			c.Status(400)
			return
		}
		if e := runtime.DeletePlan(c.Request.Context(), id); e != nil {
			c.JSON(400, gin.H{"error": e.Error()})
			return
		}
		c.Status(204)
	})
	r.POST("/smart-ops/pelican-plans/:id/run", func(c *gin.Context) {
		var id int64
		if _, e := fmt.Sscan(c.Param("id"), &id); e != nil {
			c.Status(400)
			return
		}
		j, e := runtime.RunPlan(c.Request.Context(), id)
		if e != nil {
			c.JSON(400, gin.H{"error": e.Error()})
			return
		}
		c.JSON(202, j)
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
	if err == nil && !c.BPS.AllModels {
		for _, model := range c.BPS.Models {
			if !proxy.BasispointsModelSupported(model) {
				return c, fmt.Errorf("Basispoints model %q is outside the native supported catalog", model)
			}
		}
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
