package dops

import (
	"errors"
	"fmt"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"

	"gorm.io/gorm"
)

// UpsertTTSProviderDefinition synchronizes a registered provider's public
// configuration contract without allowing user-managed protocol definitions.
func UpsertTTSProviderDefinition(definition *model.TTSProviderDefinition) error {
	var existing model.TTSProviderDefinition
	err := database.DB.Where("provider = ?", definition.Provider).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return database.DB.Create(definition).Error
	}
	if err != nil {
		return err
	}
	return database.DB.Model(&existing).Updates(map[string]interface{}{
		"name":                          definition.Name,
		"connection_config_json_schema": definition.ConnectionConfigJSONSchema,
		"terms_url":                     definition.TermsURL,
		"description":                   definition.Description,
	}).Error
}

// DeleteUnregisteredTTSProviderDefinitions removes derived provider metadata
// whose implementation is no longer part of the renderer registry.
func DeleteUnregisteredTTSProviderDefinitions(providers []model.TTSProvider) error {
	query := database.DB.Model(&model.TTSProviderDefinition{})
	if len(providers) == 0 {
		return query.Where("1 = 1").Delete(&model.TTSProviderDefinition{}).Error
	}
	return query.Where("provider NOT IN ?", providers).Delete(&model.TTSProviderDefinition{}).Error
}

// ListTTSProviderDefinitions lists all registered provider configuration contracts.
func ListTTSProviderDefinitions() ([]model.TTSProviderDefinition, error) {
	var definitions []model.TTSProviderDefinition
	if err := database.DB.Order("provider ASC").Find(&definitions).Error; err != nil {
		return nil, err
	}
	return definitions, nil
}

// GetTTSProviderDefinition retrieves the public contract for one provider.
func GetTTSProviderDefinition(provider model.TTSProvider) (*model.TTSProviderDefinition, error) {
	var definition model.TTSProviderDefinition
	if err := database.DB.Where("provider = ?", provider).First(&definition).Error; err != nil {
		return nil, fmt.Errorf("tts provider %d: %w", provider, err)
	}
	return &definition, nil
}

// GetTTSRenderer retrieves one renderer connection configuration.
func GetTTSRenderer(id int64) (*model.TTSRenderer, error) {
	var renderer model.TTSRenderer
	if err := database.DB.First(&renderer, id).Error; err != nil {
		return nil, fmt.Errorf("tts renderer %d: %w", id, err)
	}
	return &renderer, nil
}

// ListTTSRenderers lists configured renderer connections.
func ListTTSRenderers() ([]model.TTSRenderer, error) {
	var renderers []model.TTSRenderer
	if err := database.DB.Order("id ASC").Find(&renderers).Error; err != nil {
		return nil, err
	}
	return renderers, nil
}

// DeleteTTSRendererIfUnbound checks the application-level reference rule and
// deletes in one transaction. A positive returned count means deletion was
// deliberately skipped because current Agent voices still use the renderer.
func DeleteTTSRendererIfUnbound(rendererID int64) (int64, error) {
	var count int64
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		var renderer model.TTSRenderer
		if err := tx.First(&renderer, rendererID).Error; err != nil {
			return err
		}
		bound, err := countCurrentAgentVoicesByRenderer(tx, rendererID)
		if err != nil {
			return err
		}
		count = bound
		if count > 0 {
			return nil
		}
		return tx.Delete(&renderer).Error
	})
	return count, err
}

// countCurrentAgentVoicesByRenderer applies the current-version rule using the
// greatest immutable Voice ID for each Agent.
func countCurrentAgentVoicesByRenderer(db *gorm.DB, rendererID int64) (int64, error) {
	latestIDs := db.Model(&model.AgentVoice{}).Select("MAX(id)").Group("person_id")
	var count int64
	err := db.Model(&model.AgentVoice{}).
		Where("id IN (?) AND tts_renderer_id = ?", latestIDs, rendererID).
		Count(&count).Error
	return count, err
}

// GetAgentVoiceByPersonID retrieves the newest immutable voice version for an agent.
func GetAgentVoiceByPersonID(personID int64) (*model.AgentVoice, error) {
	var voice model.AgentVoice
	if err := database.DB.Where("person_id = ?", personID).Order("id DESC").First(&voice).Error; err != nil {
		return nil, fmt.Errorf("agent voice for person %d: %w", personID, err)
	}
	return &voice, nil
}

// GetSpeechRenderHistoryByMessageID retrieves one message delivery record.
func GetSpeechRenderHistoryByMessageID(messageID int64) (*model.SpeechRenderHistory, error) {
	var history model.SpeechRenderHistory
	if err := database.DB.Where("message_id = ?", messageID).First(&history).Error; err != nil {
		return nil, fmt.Errorf("speech render history for message %d: %w", messageID, err)
	}
	return &history, nil
}
