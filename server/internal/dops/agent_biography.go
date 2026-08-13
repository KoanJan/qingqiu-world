package dops

import (
	"fmt"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
)

// CreateAgentBiography inserts an agent's origin record and returns it.
// Each agent has exactly one origin record; person_id is unique.
func CreateAgentBiography(personID int64, content string) (*model.AgentBiography, error) {
	biography := &model.AgentBiography{
		PersonID: personID,
		Content:  content,
	}
	if err := database.DB.Create(biography).Error; err != nil {
		return nil, fmt.Errorf("create agent biography: %w", err)
	}
	return biography, nil
}
