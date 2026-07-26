package dops

import (
	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
)

// GetSearchConfig retrieves the search configuration. Creates a default if not found.
func GetSearchConfig() *model.SearchConfig {
	var config model.SearchConfig
	if err := database.DB.Where("id = ?", 1).First(&config).Error; err != nil {
		applogger.Error("SearchConfig not found, creating default")
		config = model.SearchConfig{
			Provider:    "tavily",
			APIKey:      "",
			Description: "",
			IsActive:    false,
		}
		if err := database.DB.Create(&config).Error; err != nil {
			applogger.Error("failed to create default search config", "error", err)
		}
	}
	return &config
}

// UpdateSearchConfig updates the search configuration with non-nil fields.
func UpdateSearchConfig(provider, apiKey, description *string, isActive *bool) *model.SearchConfig {
	config := GetSearchConfig()

	updates := make(map[string]interface{})
	if provider != nil {
		updates["provider"] = *provider
	}
	if apiKey != nil {
		updates["api_key"] = *apiKey
	}
	if description != nil {
		updates["description"] = *description
	}
	if isActive != nil {
		updates["is_active"] = *isActive
	}

	if len(updates) > 0 {
		if err := database.DB.Model(config).Updates(updates).Error; err != nil {
			applogger.Error("failed to update search config", "error", err)
		}
		if err := database.DB.First(config, 1).Error; err != nil {
			applogger.Error("failed to refresh search config after update", "error", err)
		}
	}

	applogger.Info("SearchConfig updated",
		"provider", config.Provider,
		"is_active", config.IsActive,
		"has_api_key", config.APIKey != "",
	)
	return config
}
