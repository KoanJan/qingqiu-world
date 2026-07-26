package schema

import (
	"time"

	"qingqiu-world-server/internal/model"
)

// PublicExperienceIngest is the request body for POST /api/public-experiences/ingest.
type PublicExperienceIngest struct {
	FileName   string `json:"file_name" binding:"required"`
	RawContent string `json:"raw_content" binding:"required"`
}

// PublicExperienceSearch is the request body for POST /api/public-experiences/search.
type PublicExperienceSearch struct {
	Query    string  `json:"query" binding:"required"`
	TopN     int     `json:"top_n"`
	MinScore float64 `json:"min_score"`
}

// PublicExperienceResponse is the API response for a single public experience.
type PublicExperienceResponse struct {
	ID                int64     `json:"id"`
	Title             string    `json:"title"`
	Description       string    `json:"description"`
	WhenToUse         string    `json:"when_to_use"`
	Guidelines        string    `json:"guidelines"`
	Pitfalls          string    `json:"pitfalls"`
	Procedure         string    `json:"procedure"`
	SourceType        int       `json:"source_type"`
	SourceID          int64     `json:"source_id"`
	SourceFingerprint string    `json:"source_fingerprint"`
	Status            int       `json:"status"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// NewPublicExperienceResponse creates a response from a model.
func NewPublicExperienceResponse(m *model.PublicExperience) *PublicExperienceResponse {
	return &PublicExperienceResponse{
		ID:                m.ID,
		Title:             m.Title,
		Description:       m.Description,
		WhenToUse:         m.WhenToUse,
		Guidelines:        m.Guidelines,
		Pitfalls:          m.Pitfalls,
		Procedure:         m.Procedure,
		SourceType:        m.SourceType,
		SourceID:          m.SourceID,
		SourceFingerprint: m.SourceFingerprint,
		Status:            m.Status,
		CreatedAt:         m.CreatedAt,
		UpdatedAt:         m.UpdatedAt,
	}
}

// NewPublicExperienceResponseList converts a slice of models to a slice of responses.
func NewPublicExperienceResponseList(entities []model.PublicExperience) []*PublicExperienceResponse {
	result := make([]*PublicExperienceResponse, 0, len(entities))
	for i := range entities {
		result = append(result, NewPublicExperienceResponse(&entities[i]))
	}
	return result
}

// PublicExperienceSearchResult is a single search result with a similarity score.
type PublicExperienceSearchResult struct {
	*PublicExperienceResponse
	Score float64 `json:"score"`
}

// UploadedSkillResponse is the API response for an uploaded skill.
type UploadedSkillResponse struct {
	ID         int64     `json:"id"`
	FileName   string    `json:"file_name"`
	Title      string    `json:"title"`
	RawContent string    `json:"raw_content"`
	CreatedAt  time.Time `json:"created_at"`
}

// NewUploadedSkillResponse creates a response from an UploadedSkill model.
func NewUploadedSkillResponse(m *model.UploadedSkill) *UploadedSkillResponse {
	return &UploadedSkillResponse{
		ID:         m.ID,
		FileName:   m.FileName,
		Title:      m.Title,
		RawContent: m.RawContent,
		CreatedAt:  m.CreatedAt,
	}
}
