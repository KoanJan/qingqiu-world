package dops

import (
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
)

// CreateAgentEventBuffer persists a runtime event until the agent can process it.
func CreateAgentEventBuffer(buffer *model.AgentEventBuffer) error {
	return database.DB.Create(buffer).Error
}

// ListAgentEventBuffers returns a person's buffered events in replay order.
func ListAgentEventBuffers(personID int64) ([]model.AgentEventBuffer, error) {
	var buffers []model.AgentEventBuffer
	err := database.DB.Where("person_id = ?", personID).
		Order("event_id ASC, id ASC").
		Find(&buffers).Error
	return buffers, err
}

// DeleteAgentEventBuffer removes a processed or invalid buffered event.
func DeleteAgentEventBuffer(id int64) error {
	return database.DB.Delete(&model.AgentEventBuffer{}, id).Error
}

// SetAgentSleepSince records when an agent entered its energy-depleted state.
func SetAgentSleepSince(personID int64, sleepSince string) error {
	return database.DB.Model(&model.AgentState{}).
		Where("person_id = ?", personID).
		Update("sleep_since", sleepSince).Error
}

// ClearAgentSleepSince clears an agent's energy-depleted state marker.
func ClearAgentSleepSince(personID int64) error {
	return SetAgentSleepSince(personID, "")
}

// GetAgentSleepSince returns when an agent entered its energy-depleted state.
func GetAgentSleepSince(personID int64) (string, error) {
	var state model.AgentState
	if err := database.DB.Select("sleep_since").Where("person_id = ?", personID).First(&state).Error; err != nil {
		return "", err
	}
	return state.SleepSince, nil
}

// SetAgentSleepSinceIfEmpty records the sleep time only when no marker exists.
func SetAgentSleepSinceIfEmpty(personID int64, sleepSince time.Time) error {
	return database.DB.Model(&model.AgentState{}).
		Where("person_id = ? AND sleep_since = ?", personID, "").
		Update("sleep_since", sleepSince.Format(time.RFC3339)).Error
}
