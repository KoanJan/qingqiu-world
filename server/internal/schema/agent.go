package schema

import (
	"time"

	"qingqiu-world-server/internal/model"
)

// AgentCreate represents the input for creating an agent config.
type AgentCreate struct {
	Name              string `json:"name" binding:"required"`
	Description       string `json:"description"`
	CharacterSettings string `json:"character_settings"`
	LLMConfigID       int64  `json:"llm_config_id" binding:"required"`
	Avatar            string `json:"avatar"`
}

// AgentUpdate allows updating mutable agent config fields and person-level fields.
type AgentUpdate struct {
	Bio               *string `json:"bio"`
	CharacterSettings *string `json:"character_settings"`
	LLMConfigID       *int64  `json:"llm_config_id"`
	Avatar            *string `json:"avatar"`
}

// AgentResponse represents the API response for an agent.
// From the frontend's perspective, the agent is the Person entity.
// ID is the person ID; the handler layer distributes to Person and AgentConfig internally.
type AgentResponse struct {
	ID                int64     `json:"id"`
	Name              string    `json:"name"`
	Bio               string    `json:"bio"`
	CharacterSettings string    `json:"character_settings"`
	LLMConfigID       int64     `json:"llm_config_id"`
	Avatar            string    `json:"avatar"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// NewAgentResponse converts a model.AgentConfig and model.Person to an AgentResponse.
// KB bindings are no longer part of the agent payload: since 0.1.9 KB access
// is managed per knowledge base via the kb_access endpoints.
func NewAgentResponse(m *model.AgentConfig, person *model.Person) *AgentResponse {
	name := ""
	bio := ""
	avatar := ""
	if person != nil {
		name = person.Name
		bio = person.Bio
		avatar = person.Avatar
	}
	return &AgentResponse{
		ID:                person.ID,
		Name:              name,
		Bio:               bio,
		CharacterSettings: m.CharacterSettings,
		LLMConfigID:       m.LLMConfigID,
		Avatar:            avatar,
		CreatedAt:         m.CreatedAt,
		UpdatedAt:         m.UpdatedAt,
	}
}

// NewAgentResponseList converts a list of model.AgentConfig to AgentResponse list.
func NewAgentResponseList(configs []model.AgentConfig, persons map[int64]*model.Person) []*AgentResponse {
	result := make([]*AgentResponse, 0, len(configs))
	for i := range configs {
		result = append(result, NewAgentResponse(&configs[i], persons[configs[i].PersonID]))
	}
	return result
}

// BuildUpdates builds a map of non-nil update fields from AgentConfigUpdate.
func (req *AgentUpdate) BuildUpdates() map[string]interface{} {
	updates := make(map[string]interface{})
	if req.CharacterSettings != nil {
		updates["character_settings"] = *req.CharacterSettings
	}
	if req.LLMConfigID != nil {
		updates["llm_config_id"] = *req.LLMConfigID
	}
	if req.Avatar != nil {
		updates["avatar"] = *req.Avatar
	}
	return updates
}
