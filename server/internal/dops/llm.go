package dops

import (
	"fmt"
	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
)

// GetLLMConfig retrieves an LLM config by ID.
func GetLLMConfig(llmConfigID int64) (*model.LLMConfig, error) {
	var config model.LLMConfig
	if err := database.DB.First(&config, llmConfigID).Error; err != nil {
		return nil, fmt.Errorf("llm_config %d: %w", llmConfigID, err)
	}
	return &config, nil
}

// GetSystemLLMConfig returns the system-level LLM config (id=1 row).
// Returns nil if no system LLM config has been configured.
func GetSystemLLMConfig() *model.LLMConfig {
	var sysCfg model.SystemLLMConfig
	if err := database.DB.Where("id = ?", 1).First(&sysCfg).Error; err != nil {
		applogger.Error("System LLM config not configured, system-level LLM operations unavailable")
		return nil
	}

	cfg, err := GetLLMConfig(sysCfg.LLMConfigID)
	if err != nil {
		applogger.Error("System LLM config references invalid llm_config_id",
			"llm_config_id", sysCfg.LLMConfigID,
			"error", err,
		)
		return nil
	}
	return cfg
}

// IsSystemLLMConfigured returns true if a system-level LLM config has been set
// and its referenced LLM config is still valid.
func IsSystemLLMConfigured() bool {
	return GetSystemLLMConfig() != nil
}

// UpdateSystemLLMConfig upserts the system-level LLM config (single row, id=1).
// llmConfigID is the ID of an existing LLM config in the llm_configs table.
func UpdateSystemLLMConfig(llmConfigID int64) error {
	var sysCfg model.SystemLLMConfig
	err := database.DB.Where("id = ?", 1).First(&sysCfg).Error
	if err != nil {
		applogger.Error("system LLM config not found, creating new record", "error", err)
		sysCfg = model.SystemLLMConfig{
			LLMConfigID: llmConfigID,
		}
		if createErr := database.DB.Create(&sysCfg).Error; createErr != nil {
			return createErr
		}
	} else {
		sysCfg.LLMConfigID = llmConfigID
		if updateErr := database.DB.Save(&sysCfg).Error; updateErr != nil {
			return updateErr
		}
	}
	applogger.Info("System LLM config updated", "llm_config_id", llmConfigID)
	return nil
}

// CreateLLMConfig creates an llm config. If it is the first LLM config in the
// table, it is automatically promoted to the system LLM so that system-level
// LLM features work out of the box without manual bootstrap.
func CreateLLMConfig(entity *model.LLMConfig) error {
	if err := database.DB.Select(
		"Name", "ModelID", "BaseURL", "APIKey", "Description",
	).Create(entity).Error; err != nil {
		return err
	}

	var count int64
	if err := database.DB.Model(&model.LLMConfig{}).Count(&count).Error; err != nil {
		// The create itself already succeeded; a count failure must not make
		// the client believe the config was not saved.
		applogger.Error("count llm configs after create failed", "error", err)
		return nil
	}
	if count != 1 {
		return nil
	}
	if err := UpdateSystemLLMConfig(entity.ID); err != nil {
		// The create succeeded; a failed auto promotion is recoverable via the
		// manual "set as system LLM" action, so log instead of failing.
		applogger.Error("auto-set first llm config as system llm failed",
			"llm_config_id", entity.ID, "error", err)
		return nil
	}
	applogger.Info("First LLM config auto-set as system LLM", "llm_config_id", entity.ID)
	return nil
}
