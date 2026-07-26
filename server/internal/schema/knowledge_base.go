package schema

import (
	"time"

	"qingqiu-world-server/internal/model"
)

// KnowledgeBaseCreate represents the input for creating a knowledge base.
type KnowledgeBaseCreate struct {
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
}

// KnowledgeBaseUpdate contains the mutable fields for updating a knowledge base.
type KnowledgeBaseUpdate struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

// KnowledgeBaseResponse represents the API response for a knowledge base.
type KnowledgeBaseResponse struct {
	ID            int64                        `json:"id"`
	Name          string                       `json:"name"`
	Description   string                       `json:"description"`
	IndexType     model.KnowledgeBaseIndexType `json:"index_type"`
	IndexFilePath string                       `json:"index_file_path"`
	DocumentCount int                          `json:"document_count"`
	VectorCount   int                          `json:"vector_count"`
	DeletedCount  int                          `json:"deleted_count"`
	CreatedAt     time.Time                    `json:"created_at"`
	UpdatedAt     time.Time                    `json:"updated_at"`
}

// NewKnowledgeBaseResponse converts a model.KnowledgeBase to a KnowledgeBaseResponse.
func NewKnowledgeBaseResponse(m *model.KnowledgeBase) *KnowledgeBaseResponse {
	return &KnowledgeBaseResponse{
		ID:            m.ID,
		Name:          m.Name,
		Description:   m.Description,
		IndexType:     m.IndexType,
		IndexFilePath: m.IndexFilePath,
		DocumentCount: m.DocumentCount,
		VectorCount:   m.VectorCount,
		DeletedCount:  m.DeletedCount,
		CreatedAt:     m.CreatedAt,
		UpdatedAt:     m.UpdatedAt,
	}
}

// NewKnowledgeBaseResponseList converts a list of model.KnowledgeBase to KnowledgeBaseResponse list.
func NewKnowledgeBaseResponseList(entities []model.KnowledgeBase) []*KnowledgeBaseResponse {
	result := make([]*KnowledgeBaseResponse, 0, len(entities))
	for i := range entities {
		result = append(result, NewKnowledgeBaseResponse(&entities[i]))
	}
	return result
}

// BuildUpdates builds a map of non-nil update fields from KnowledgeBaseUpdate.
func (req *KnowledgeBaseUpdate) BuildUpdates() map[string]interface{} {
	updates := make(map[string]interface{})
	if req.Name != nil {
		updates["name"] = *req.Name
	}
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	return updates
}
