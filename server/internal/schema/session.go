package schema

import (
	"time"

	"qingqiu-world-server/internal/dops"
	"qingqiu-world-server/internal/model"
)

// SessionBase contains the common fields for session creation.
// AgentID is the person ID of the initial AI participant.
// Backend validates the person type and creates a participant_sessions record.
type SessionBase struct {
	Title   *string `json:"title"`
	AgentID int64   `json:"agent_id" binding:"required"`
}

// SessionCreate is an alias of SessionBase for creating sessions.
type SessionCreate SessionBase

// SessionUpdate contains the mutable fields for updating a session.
type SessionUpdate struct {
	Title *string `json:"title"`
}

// SessionResponse represents the API response for a session.
// AgentID is the person ID of the first AI participant,
// resolved from participant_sessions.
type SessionResponse struct {
	ID          int64     `json:"id"`
	Title       string    `json:"title"`
	AgentID     int64     `json:"agent_id"`
	AgentName   string    `json:"agent_name"`   // Resolved from persons table
	AgentAvatar string    `json:"agent_avatar"` // Resolved from persons table
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// NewSessionResponse converts a model.Session to a SessionResponse.
// agent is resolved from participant_sessions by the caller.
func NewSessionResponse(m *model.Session, aiMember *dops.SessionMember) *SessionResponse {
	return &SessionResponse{
		ID:          m.ID,
		Title:       m.Title,
		AgentID:     aiMember.PersonID,
		AgentName:   aiMember.Name,
		AgentAvatar: aiMember.Avatar,
		CreatedAt:   m.CreatedAt,
		UpdatedAt:   m.UpdatedAt,
	}
}

// NewSessionResponseList converts a list of model.Session to SessionResponse list.
// personMap maps sessionID → AI participant info, pre-resolved by dops.GetAIPersonsInSessions.
func NewSessionResponseList(entities []model.Session, personMap map[int64]*dops.SessionMember) []*SessionResponse {
	if len(entities) == 0 {
		return nil
	}
	result := make([]*SessionResponse, 0, len(entities))
	for i := range entities {
		result = append(result, NewSessionResponse(&entities[i], personMap[entities[i].ID]))
	}
	return result
}

// BuildUpdates builds a map of non-nil update fields from SessionUpdate.
func (req *SessionUpdate) BuildUpdates() map[string]interface{} {
	updates := make(map[string]interface{})
	if req.Title != nil {
		updates["title"] = *req.Title
	}
	return updates
}
