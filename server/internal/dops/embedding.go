package dops

import (
	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
)

// GetEmbeddingConfig retrieves the global embedding configuration (first row).
// Returns nil if no embedding config exists at all.
func GetEmbeddingConfig() *model.EmbeddingConfig {
	var config model.EmbeddingConfig
	if err := database.DB.Order("id ASC").First(&config).Error; err != nil {
		applogger.Error("No embedding config found, embedding-dependent features unavailable")
		return nil
	}
	return &config
}

// IsEmbeddingConfigured returns true if an embedding config exists and has
// a non-empty API key.
func IsEmbeddingConfigured() bool {
	cfg := GetEmbeddingConfig()
	return cfg != nil && cfg.APIKey != ""
}

// UpdateEmbeddingConfig updates the global embedding configuration.
// If no config exists, creates one; otherwise updates the first row.
func UpdateEmbeddingConfig(req model.EmbeddingConfig) *model.EmbeddingConfig {
	config := GetEmbeddingConfig()
	if config == nil {
		if err := database.DB.Create(&req).Error; err != nil {
			applogger.Error("Failed to create embedding config", "error", err)
			return nil
		}
		config = &req
	} else {
		if err := database.DB.Model(config).Updates(req).Error; err != nil {
			applogger.Error("Failed to update embedding config", "error", err)
			return nil
		}
		if err := database.DB.First(config, config.ID).Error; err != nil {
			applogger.Error("failed to refresh embedding config after update", "id", config.ID, "error", err)
		}
	}

	applogger.Info("Embedding config updated",
		"name", config.Name,
		"model", config.ModelID,
	)
	return config
}
