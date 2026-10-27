package admin

import (
	"github.com/codex2api/credentialops"
	"github.com/gin-gonic/gin"
)

func (h *Handler) registerCredentialMihomoRoutes(group *gin.RouterGroup) {
	group.GET("/credential-ops/mihomo", func(c *gin.Context) { c.JSON(200, credentialops.MihomoStatus()) })
	group.PUT("/credential-ops/mihomo", func(c *gin.Context) {
		var input credentialops.MihomoInput
		if c.ShouldBindJSON(&input) != nil {
			c.Status(400)
			return
		}
		if err := credentialops.SaveMihomoConfig(c.Request.Context(), input); err != nil {
			c.JSON(409, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, credentialops.MihomoStatus())
	})
	group.POST("/credential-ops/mihomo/install", func(c *gin.Context) {
		if err := credentialops.InstallMihomo(c.Request.Context()); err != nil {
			c.JSON(409, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, credentialops.MihomoStatus())
	})
}
