// Package llm provides LLM client wrappers for OpenAI-compatible chat completion APIs.
//
// This file contains model capability detection and caching logic.
// Capabilities (e.g., json_schema support) are cached in memory with
// persistence to the database, avoiding repeated trial-and-error for
// unsupported features.
package llm

import (
	"strings"
	"sync"

	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
)

// capabilityCache stores model json_schema support status in memory for fast lookup.
// Key format: "baseURL|modelID", value: int (0=unsupported, 1=supported).
// Loaded from DB on startup, updated on write (memory-first, DB async).
var capabilityCache sync.Map

// LoadCapabilityCache reads the entire model_capabilities table into in-memory cache.
// Must be called after database.Init() and database.AutoMigrate() during startup.
// Subsequent calls to lookupCapability will hit the in-memory cache instead of the DB.
func LoadCapabilityCache() {
	var caps []model.ModelCapability
	if err := database.DB.Find(&caps).Error; err != nil {
		applogger.Error("failed to load capability cache from database", "error", err)
		return
	}
	for _, c := range caps {
		key := capabilityCacheKey(c.BaseURL, c.ModelID)
		capabilityCache.Store(key, c.SupportsJSONSchema)
	}
	applogger.Info("capability cache loaded from database", "count", len(caps))
}

// capabilityCacheKey builds the cache key from baseURL and modelID.
func capabilityCacheKey(baseURL, modelID string) string {
	return baseURL + "|" + modelID
}

// lookupCapability checks the in-memory capability cache for json_schema support status.
// Returns (nil, false) if this model has never been tested.
func (cm *ChatModel) lookupCapability() (*model.ModelCapability, bool) {
	key := capabilityCacheKey(cm.baseURL, cm.modelID)
	if val, ok := capabilityCache.Load(key); ok {
		supports := val.(int)
		applogger.Debug("capability cache hit", "base_url", cm.baseURL, "model_id", cm.modelID, "supports_json_schema", supports)
		return &model.ModelCapability{
			BaseURL:            cm.baseURL,
			ModelID:            cm.modelID,
			SupportsJSONSchema: supports,
		}, true
	}
	applogger.Debug("capability cache miss", "base_url", cm.baseURL, "model_id", cm.modelID)
	return nil, false
}

// saveCapability persists the json_schema support status for this model.
// Writes to in-memory cache first, then persists to DB asynchronously.
// DB write failure is logged but not propagated, as it does not affect the running service
// and the worst case is each model retries json_schema once on next restart.
func (cm *ChatModel) saveCapability(supportsJSONSchema int) {
	key := capabilityCacheKey(cm.baseURL, cm.modelID)
	capabilityCache.Store(key, supportsJSONSchema)

	go func() {
		cap := model.ModelCapability{
			BaseURL:            cm.baseURL,
			ModelID:            cm.modelID,
			SupportsJSONSchema: supportsJSONSchema,
		}
		result := database.DB.Where("base_url = ? AND model_id = ?", cm.baseURL, cm.modelID).Assign(map[string]interface{}{
			"supports_json_schema": supportsJSONSchema,
		}).FirstOrCreate(&cap)
		if result.Error != nil {
			applogger.Error("failed to persist capability cache to database", "base_url", cm.baseURL, "model_id", cm.modelID, "error", result.Error)
		}
	}()
}

// isResponseFormatError checks if a chat completion error is caused by the model
// not supporting the response_format feature. Uses keyword matching on the error string,
// which is conservative: if the error contains "response_format", it is treated as a
// compatibility issue and cached as unsupported. False positives are acceptable because
// the fallback (function_call) is functionally equivalent.
func isResponseFormatError(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "response_format")
}
