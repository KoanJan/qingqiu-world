package middleware

import (
	"qingqiu-world-server/internal/api/response"
	"qingqiu-world-server/internal/dops"

	"github.com/gin-gonic/gin"
)

// RequireSystemLLM blocks requests when the system-level LLM config is not set up.
func RequireSystemLLM(c *gin.Context) {
	if !dops.IsSystemLLMConfigured() {
		response.BadRequest(c, "System LLM config is required but not configured")
		c.Abort()
		return
	}
	c.Next()
}
