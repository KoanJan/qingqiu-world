package schema

import (
	"time"

	"qingqiu-world-server/internal/model"

	"gorm.io/gorm"
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
	ID        int64     `json:"id"`
	Title     string    `json:"title"`
	AgentID   int64     `json:"agent_id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NewSessionResponse converts a model.Session to a SessionResponse.
// agentID is resolved from participant_sessions by the caller.
func NewSessionResponse(m *model.Session, agentID int64) *SessionResponse {
	return &SessionResponse{
		ID:        m.ID,
		Title:     m.Title,
		AgentID:   agentID,
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}
}

// NewSessionResponseList converts a list of model.Session to SessionResponse list.
// Resolves the first AI person_id for each session from participant_sessions.
func NewSessionResponseList(db *gorm.DB, entities []model.Session) []*SessionResponse {
	if len(entities) == 0 {
		return nil
	}
	sids := make([]int64, len(entities))
	for i := range entities {
		sids[i] = entities[i].ID
	}

	// Resolve session → first AI person ID from participant_sessions.
	type row struct {
		SessionID int64
		PersonID  int64
	}
	var rows []row
	db.Raw(`SELECT ps.session_id, ps.participant_id AS person_id
		FROM participant_sessions ps
		JOIN persons p ON p.id = ps.participant_id AND p.type = 1
		WHERE ps.session_id IN ?
		GROUP BY ps.session_id`, sids).Scan(&rows)

	personMap := make(map[int64]int64, len(rows))
	for _, r := range rows {
		if _, ok := personMap[r.SessionID]; !ok {
			personMap[r.SessionID] = r.PersonID
		}
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
