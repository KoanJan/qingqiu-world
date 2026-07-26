package dops

import (
	"fmt"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
)

// GetAgentConfigWithPerson retrieves an agent config with its associated Person.
func GetAgentConfigWithPerson(agentConfigID int64) (*model.AgentConfig, *model.Person, error) {
	ac, err := Get[model.AgentConfig](agentConfigID)
	if err != nil {
		return nil, nil, err
	}
	person, err := GetPerson(ac.PersonID)
	if err != nil {
		return nil, nil, err
	}
	return ac, person, nil
}

// GetAgentConfigByPersonID retrieves an agent config by PersonID.
func GetAgentConfigByPersonID(personID int64) (*model.AgentConfig, error) {
	var ac model.AgentConfig
	if err := database.DB.Where("person_id = ?", personID).First(&ac).Error; err != nil {
		return nil, fmt.Errorf("agent config with person_id %d: %w", personID, err)
	}
	return &ac, nil
}

// GetAgentConfigName returns the name of an agent config via its Person record.
func GetAgentConfigName(agentConfigID int64) string {
	ac, err := Get[model.AgentConfig](agentConfigID)
	if err != nil {
		return ""
	}
	person, err := GetPerson(ac.PersonID)
	if err != nil {
		return ""
	}
	return person.Name
}

// ListAgentConfigsByLLMConfigID return AgentConfig list who using the LLMConfig
func ListAgentConfigsByLLMConfigID(llmConfigID int64) ([]model.AgentConfig, error) {
	var referencingAgents []model.AgentConfig
	if err := database.DB.Where("llm_config_id = ?", llmConfigID).Find(&referencingAgents).Error; err != nil {
		return nil, err
	}
	return referencingAgents, nil
}

// GetAgentConfigIDBySessionID returns the id of AgentConfig of the AIPerson participated in the session
func GetFirstAgentConfigIDBySessionID(sessionID int64) int64 {
	var agentConfigID int64
	database.DB.Raw(`SELECT ac.id FROM participant_sessions ps
		JOIN persons p ON p.id = ps.participant_id AND p.type = 1
		JOIN agent_configs ac ON ac.person_id = p.id
		WHERE ps.session_id = ?
		LIMIT 1`, sessionID).Scan(&agentConfigID)
	return agentConfigID
}
