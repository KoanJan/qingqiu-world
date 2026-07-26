package middleware

import (
	"qingqiu-world-server/internal/api/response"
	"qingqiu-world-server/internal/dops"

	"github.com/gin-gonic/gin"
)

// RequireEmbedding blocks requests when the embedding config is not set up.
func RequireEmbedding(c *gin.Context) {
	if !dops.IsEmbeddingConfigured() {
		response.BadRequest(c, "Embedding config is required but not configured")
		c.Abort()
		return
	}
	c.Next()
}
