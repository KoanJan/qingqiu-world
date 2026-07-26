package experience

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/llm"
	"qingqiu-world-server/internal/service/vectorutils"
)

// Package-level state for the experience system singleton.
var (
	embeddingSvc *llm.EmbeddingService
	initOnce     sync.Once
	ready        atomic.Bool
)

// Init sets the embedding service reference for the experience system.
// Must be called once during application startup, before any experience operations.
// Idempotent: only the first call has effect.
// embeddingSvc may be nil if no embedding service is configured.
func Init(es *llm.EmbeddingService) {
	initOnce.Do(func() {
		embeddingSvc = es
		ready.Store(true)
		applogger.Info("Experience system initialized")
	})
}

func panicIfNotReady() {
	if !ready.Load() {
		panic("Experience system not initialized")
	}
}

// createExperience inserts a private experience and computes its embedding
// from the description. Called internally by CheckReflection and CheckLearning.
//
// sourceID identifies the origin resource, interpreted by source:
//   - source=1 (Reflection): sourceID = session_id
//   - source=2 (Learn):       sourceID = public_experience_id
func createExperience(ctx context.Context, personID int64, source int, sourceID int64, title, description, whenToUse, guidelines, pitfalls, procedure string) (*model.AgentExperience, error) {
	emb, err := embeddingSvc.EmbedSingle(ctx, description)
	if err != nil {
		return nil, fmt.Errorf("embed experience description: %w", err)
	}

	exp := &model.AgentExperience{
		PersonID:    personID,
		Title:       title,
		Description: description,
		WhenToUse:   whenToUse,
		Guidelines:  guidelines,
		Pitfalls:    pitfalls,
		Procedure:   procedure,
		Source:      source,
		SourceID:    sourceID,
	}

	if err := database.DB.Create(exp).Error; err != nil {
		return nil, fmt.Errorf("insert agent_experience: %w", err)
	}

	vec := &model.AgentExperienceVector{
		ExperienceID: exp.ID,
		Embedding:    vectorutils.Float32SliceToBlob(emb),
	}
	if err := database.DB.Create(vec).Error; err != nil {
		applogger.Error("Failed to insert agent_experience_vector, orphaned experience row",
			"experience_id", exp.ID,
			"error", err,
		)
		return nil, fmt.Errorf("insert agent_experience_vector: %w", err)
	}

	applogger.Info("AgentExperience created",
		"id", exp.ID,
		"person_id", personID,
		"source", source,
		"source_id", sourceID,
	)
	return exp, nil
}

// updateExperience overwrites an existing experience's content fields and
// re-embeds the description if it changed.
//
// source_id is intentionally NOT updated: it is a stable resource reference
// (session_id for reflection, public_experience_id for learn) that does not
// change when the content is refined. This replaces the previous
// source_fingerprint handling which needed source-type-specific branching.
//
// Called by the reflection step when the LLM determines that a newly distilled
// experience refines an existing one (update_exp_id > 0) rather than being a
// new lesson.
func updateExperience(ctx context.Context, expID, personID int64, title, description, whenToUse, guidelines, pitfalls, procedure string) error {
	var exp model.AgentExperience
	if err := database.DB.Where("id = ? AND person_id = ?", expID, personID).First(&exp).Error; err != nil {
		return fmt.Errorf("experience not found: %w", err)
	}

	updates := map[string]interface{}{
		"title":       title,
		"description": description,
		"when_to_use": whenToUse,
		"guidelines":  guidelines,
		"pitfalls":    pitfalls,
		"procedure":   procedure,
	}

	if err := database.DB.Model(&exp).Updates(updates).Error; err != nil {
		return fmt.Errorf("update agent_experience: %w", err)
	}

	// Re-embed only when the description actually changed.
	if description != exp.Description {
		emb, err := embeddingSvc.EmbedSingle(ctx, description)
		if err != nil {
			return fmt.Errorf("re-embed experience description: %w", err)
		}
		if err := database.DB.Model(&model.AgentExperienceVector{}).
			Where("experience_id = ?", expID).
			Update("embedding", vectorutils.Float32SliceToBlob(emb)).Error; err != nil {
			return fmt.Errorf("update agent_experience_vector: %w", err)
		}
	}

	applogger.Info("AgentExperience updated",
		"id", expID,
		"person_id", personID,
		"source", exp.Source,
		"description_changed", description != exp.Description,
	)
	return nil
}
