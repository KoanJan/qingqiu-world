package schema

import (
	"time"

	"qingqiu-world-server/internal/model"
)

// MessageCreate represents the input for creating a message.
type MessageCreate struct {
	Content string `json:"content" binding:"required"`
}

// MessageResponse represents the API response for a message.
type MessageResponse struct {
	ID        int64     `json:"id"`
	SessionID int64     `json:"session_id"`
	PersonID  int64     `json:"person_id"`
	Content   string    `json:"content"`
	Status    int       `json:"status"`
	DraftID   *int64    `json:"draft_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NewMessageResponse converts a model.Message to a MessageResponse.
func NewMessageResponse(m *model.Message) *MessageResponse {
	return &MessageResponse{
		ID:        m.ID,
		SessionID: m.SessionID,
		PersonID:  m.PersonID,
		Content:   m.Content,
		DraftID:   m.DraftID,
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}
}

// NewMessageResponseList converts a list of model.Message to MessageResponse list.
func NewMessageResponseList(entities []model.Message) []*MessageResponse {
	result := make([]*MessageResponse, 0, len(entities))
	for i := range entities {
		result = append(result, NewMessageResponse(&entities[i]))
	}
	return result
}
