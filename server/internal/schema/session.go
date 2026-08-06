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

// SessionParticipantResponse represents a single AI participant in a session.
// Used by the frontend to render a multi-avatar grid for A2A sessions
// and future group chats.
type SessionParticipantResponse struct {
	PersonID int64  `json:"person_id"`
	Name     string `json:"name"`
	Avatar   string `json:"avatar"`
}

// SessionResponse represents the API response for a session.
// AgentID is the person ID of the first AI participant,
// resolved from participant_sessions.
//
// Participants (0.1.3) lists ALL AI participants in the session, so the
// frontend can render a multi-avatar grid (up to 9, 九宫格) for A2A
// sessions and future group chats. For 1v1 human-AI sessions this has
// exactly one entry.
//
// IsParticipant (0.1.3) indicates whether the current human user is a
// participant in this session. When false, the session is read-only for the
// user — they can view the chat history but cannot send messages (the
// frontend hides the input component). This reflects a relationship fact
// (the user is not in that conversation), not a permission restriction.
type SessionResponse struct {
	ID            int64                        `json:"id"`
	Title         string                       `json:"title"`
	AgentID       int64                        `json:"agent_id"`
	AgentName     string                       `json:"agent_name"`   // Resolved from persons table
	AgentAvatar   string                       `json:"agent_avatar"` // Resolved from persons table
	Participants  []SessionParticipantResponse `json:"participants"`
	IsParticipant bool                         `json:"is_participant"`
	CreatedAt     time.Time                    `json:"created_at"`
	UpdatedAt     time.Time                    `json:"updated_at"`
}

// NewSessionResponse converts a model.Session to a SessionResponse.
// aiMembers is the full list of AI participants, resolved by the caller.
// The first entry (if any) populates AgentID/AgentName/AgentAvatar for
// backward compatibility; all entries populate Participants.
// isParticipant indicates whether the current user is in this session.
func NewSessionResponse(m *model.Session, aiMembers []dops.SessionMember, isParticipant bool) *SessionResponse {
	resp := &SessionResponse{
		ID:            m.ID,
		Title:         m.Title,
		IsParticipant: isParticipant,
		CreatedAt:     m.CreatedAt,
		UpdatedAt:     m.UpdatedAt,
	}
	for _, mem := range aiMembers {
		resp.Participants = append(resp.Participants, SessionParticipantResponse{
			PersonID: mem.PersonID,
			Name:     mem.Name,
			Avatar:   mem.Avatar,
		})
	}
	// First participant (if any) populates the legacy single-agent fields.
	if len(aiMembers) > 0 {
		resp.AgentID = aiMembers[0].PersonID
		resp.AgentName = aiMembers[0].Name
		resp.AgentAvatar = aiMembers[0].Avatar
	}
	return resp
}

// NewSessionResponseList converts a list of model.Session to SessionResponse list.
// membersMap maps sessionID → []SessionMember (all AI participants), pre-resolved
// by dops.GetAIPersonsInSessions.
// participationMap maps sessionID → true for sessions the current user participates in.
func NewSessionResponseList(entities []model.Session, membersMap map[int64][]dops.SessionMember, participationMap map[int64]bool) []*SessionResponse {
	if len(entities) == 0 {
		return nil
	}
	result := make([]*SessionResponse, 0, len(entities))
	for i := range entities {
		isParticipant := participationMap[entities[i].ID]
		result = append(result, NewSessionResponse(&entities[i], membersMap[entities[i].ID], isParticipant))
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
