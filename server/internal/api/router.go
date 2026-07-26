// Package router sets up the Gin HTTP router with all API endpoints.
package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"qingqiu-world-server/internal/api/handler"
	"qingqiu-world-server/internal/api/middleware"
	"qingqiu-world-server/internal/config"

	"github.com/gin-gonic/gin"
)

// SetupRouter creates and configures the Gin engine with all routes.
// Includes CORS middleware, static file serving for avatars, and all API endpoints.
func SetupRouter() *gin.Engine {
	r := gin.Default()

	r.Use(middleware.CORS())

	h := handler.NewHandler()

	r.GET("/", h.Root)
	r.GET("/api/version", h.GetVersion)

	avatarsDir := config.Get().GetAvatarsDir()
	os.MkdirAll(avatarsDir, 0755)
	r.GET("/avatars/:filename", func(c *gin.Context) {
		filename := c.Param("filename")
		if strings.Contains(filename, "..") {
			c.Status(http.StatusForbidden)
			return
		}
		filePath := filepath.Join(avatarsDir, filename)
		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			c.Status(http.StatusNotFound)
			return
		}
		c.Header("Cache-Control", "public, max-age=86400")
		c.File(filePath)
	})

	api := r.Group("/api")
	{
		llmConfigs := api.Group("/llm-configs")
		{
			llmConfigs.POST("", h.CreateLLMConfig)
			llmConfigs.GET("", h.ListLLMConfigs)
			llmConfigs.GET("/:id", h.GetLLMConfig)
			llmConfigs.PUT("/:id", h.UpdateLLMConfig)
			llmConfigs.DELETE("/:id", h.DeleteLLMConfig)
		}

		embeddingConfig := api.Group("/embedding-config")
		{
			embeddingConfig.GET("", h.GetEmbeddingConfig)
			embeddingConfig.PUT("", h.UpdateEmbeddingConfig)
		}

		userProfile := api.Group("/user-profile")
		{
			userProfile.GET("", h.GetUserProfile)
			userProfile.PUT("", h.CreateOrUpdateUserProfile)
		}

		persons := api.Group("/persons")
		{
			persons.GET("/me", h.GetCurrentPerson)
		}

		agents := api.Group("/agents")
		{
			agents.POST("", middleware.RequireEmbedding, h.CreateAgent)
			agents.GET("", h.ListAgents)
			agents.GET("/with-sessions", h.ListAgentConfigsWithSessions)
			agents.GET("/:id", h.GetAgent)
			agents.PUT("/:id", h.UpdateAgent)
			agents.DELETE("/:id", h.DeleteAgent)
		}

		sessions := api.Group("/sessions")
		{
			sessions.GET("", h.ListSessions)
			sessions.GET("/:id", h.GetSession)
			sessions.GET("/:id/activities", h.GetSessionActivities)
			sessions.GET("/:id/received/deliveries", h.GetReceivedDeliveries)
			sessions.GET("/:id/received/file", h.GetReceivedFile)
			sessions.PUT("/:id", h.UpdateSession)
			sessions.DELETE("/:id", h.DeleteSession)
		}

		messages := api.Group("/messages")
		{
			messages.POST("/:id", h.CreateMessage)
			messages.GET("/:id", h.ListMessages)
		}

		chat := api.Group("/chat")
		{
			chat.POST("/new", middleware.RequireEmbedding, h.CreateAndSend)
			chat.POST("/send/:session_id", middleware.RequireEmbedding, h.SendMessage)
			chat.GET("/stream/:session_id", h.StreamMessages)
			chat.GET("/agents/:session_id", h.GetSessionAgents)
		}

		searchConfig := api.Group("/search-config")
		{
			searchConfig.GET("", h.GetSearchConfig)
			searchConfig.PUT("", h.UpdateSearchConfig)
		}

		uploads := api.Group("/uploads")
		{
			uploads.POST("/avatar", h.UploadAvatar)
		}

		appearance := api.Group("/appearance")
		{
			appearance.GET("/settings", h.GetSettings)
			appearance.PATCH("/settings", h.UpdateSettings)
			// User-uploaded background images (dynamic data)
			appearance.GET("/backgrounds", h.ListBackgrounds)
			appearance.GET("/backgrounds/:filename", h.ServeBackground)
			appearance.POST("/backgrounds", h.UploadBackground)
			appearance.DELETE("/backgrounds/:filename", h.DeleteBackground)
		}

		systemLLMConfig := api.Group("/system-llm-config")
		{
			systemLLMConfig.GET("", h.GetSystemLLMConfigHandler)
			systemLLMConfig.PUT("", h.UpdateSystemLLMConfigHandler)
		}

		publicExperiences := api.Group("/public-experiences")
		{
			publicExperiences.GET("", h.ListPublicExperiences)
			publicExperiences.GET("/:id", h.GetPublicExperience)
			publicExperiences.DELETE("/:id", h.DeletePublicExperience)
			publicExperiences.POST("/ingest", middleware.RequireSystemLLM, h.IngestPublicExperience)
			publicExperiences.POST("/:id/redistill", middleware.RequireSystemLLM, h.RedistillPublicExperience)
		}

		uploadedSkills := api.Group("/uploaded-skills")
		{
			uploadedSkills.GET("/:id", h.GetUploadedSkill)
		}

		kbGroup := api.Group("/kb")
		{
			kbGroup.POST("", middleware.RequireEmbedding, h.CreateKnowledgeBase)
			kbGroup.GET("", h.ListKnowledgeBases)
			kbGroup.GET("/:id", h.GetKnowledgeBase)
			kbGroup.PUT("/:id", h.UpdateKnowledgeBase)
			kbGroup.DELETE("/:id", h.DeleteKnowledgeBase)
			kbGroup.GET("/:id/documents", h.ListDocuments)
			kbGroup.POST("/:id/documents", h.UploadDocument)
			kbGroup.GET("/:id/documents/:doc_id", h.GetDocument)
			kbGroup.DELETE("/:id/documents/:doc_id", h.DeleteDocument)
			kbGroup.POST("/:id/search", h.SearchKB)
			kbGroup.POST("/search", h.SearchMultiKB)
		}
	}

	return r
}
