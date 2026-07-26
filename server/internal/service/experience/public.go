package experience

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/vectorutils"
)

// PublicSearchResult wraps a PublicExperience with its cosine similarity score.
type PublicSearchResult struct {
	Experience model.PublicExperience
	Score      float64 // Cosine similarity, 0..1
}

// resolveSectionRef resolves a SectionRef into the final text to store.
//
// Rules (in priority order):
//  1. If LineRange is valid and within rawContent bounds → extract those lines.
//     If Content is also non-empty → append it (SUPPLEMENT mode).
//  2. If only Content is non-empty → use Content (GENERATE fallback).
//  3. Otherwise → empty string.
func resolveSectionRef(ref SectionRef, rawContent string) string {
	if ref.LineRange != nil && ref.LineRange.Start > 0 && ref.LineRange.End >= ref.LineRange.Start {
		lines := strings.Split(rawContent, "\n")
		if ref.LineRange.End <= len(lines) {
			extracted := strings.Join(lines[ref.LineRange.Start-1:ref.LineRange.End], "\n")
			if ref.Content != "" {
				// SUPPLEMENT mode: original lines + LLM supplement.
				return extracted + "\n\n" + ref.Content
			}
			// PRESERVE mode: original lines verbatim.
			return extracted
		}
	}
	// GENERATE fallback or empty.
	return ref.Content
}

// finalizePublicExperience fills in the content fields of a pre-written
// PublicExperience, sets Status=Active, and upserts the embedding vector.
// Called by processIngestion when LLM distillation succeeds.
//
// fm contains the YAML frontmatter (name → title, description → description).
// These are used directly and NOT overridden by the LLM output.
// output contains the LLM's section refs for when_to_use, guidelines, etc.
// rawContent is the original SKILL.md content, used to resolve line_range refs.
func finalizePublicExperience(ctx context.Context, expID int64, fm *SkillFrontmatter, output ingestOutput, rawContent string) error {
	// Title and description come from YAML frontmatter.
	// Only fall back to LLM output if frontmatter parsing failed (nil pointer or empty fields).
	// If frontmatter has a value but the LLM returned something different,
	// log a warning — the frontmatter value always wins.
	var title, description string
	var fmName, fmDesc string
	if fm != nil {
		title = fm.Name
		description = fm.Description
		fmName = fm.Name
		fmDesc = fm.Description
	}
	if title == "" {
		title = output.Title
	} else if output.Title != "" && output.Title != title {
		applogger.Warn("finalizePublicExperience: LLM title differs from frontmatter name, using frontmatter value",
			"exp_id", expID,
			"frontmatter_name", fmName,
			"llm_title", output.Title,
		)
	}
	if description == "" {
		description = output.Description
	} else if output.Description != "" && output.Description != description {
		applogger.Warn("finalizePublicExperience: LLM description differs from frontmatter description, using frontmatter value",
			"exp_id", expID,
			"frontmatter_description", fmDesc,
			"llm_description", output.Description,
		)
	}

	// Resolve section content: line_range wins over content.
	whenToUse := resolveSectionRef(output.WhenToUse, rawContent)
	guidelines := resolveSectionRef(output.Guidelines, rawContent)
	pitfalls := resolveSectionRef(output.Pitfalls, rawContent)
	procedure := resolveSectionRef(output.Procedure, rawContent)

	updates := map[string]interface{}{
		"title":       title,
		"description": description,
		"when_to_use": whenToUse,
		"guidelines":  guidelines,
		"pitfalls":    pitfalls,
		"procedure":   procedure,
		"status":      model.PublicExperienceStatusActive,
	}
	if err := database.DB.Model(&model.PublicExperience{}).
		Where("id = ?", expID).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("update public_experience: %w", err)
	}

	// Embed description and upsert vector.
	emb, err := embeddingSvc.EmbedSingle(ctx, description)
	if err != nil {
		return fmt.Errorf("embed public experience description: %w", err)
	}
	embBlob := vectorutils.Float32SliceToBlob(emb)

	// Try update first; if no row exists (first distillation), create one.
	result := database.DB.Model(&model.PublicExperienceVector{}).
		Where("experience_id = ?", expID).
		Update("embedding", embBlob)
	if result.Error != nil {
		return fmt.Errorf("update public_experience_vector: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		vec := &model.PublicExperienceVector{
			ExperienceID: expID,
			Embedding:    embBlob,
		}
		if err := database.DB.Create(vec).Error; err != nil {
			return fmt.Errorf("insert public_experience_vector: %w", err)
		}
	}

	return nil
}

// markPublicExperienceError sets Status=Error on a public experience.
// Called by processIngestion when distillation fails. Error details are
// logged but not stored in the DB.
func markPublicExperienceError(expID int64) {
	if err := database.DB.Model(&model.PublicExperience{}).
		Where("id = ?", expID).
		Update("status", model.PublicExperienceStatusError).Error; err != nil {
		applogger.Error("Failed to mark public experience as error",
			"exp_id", expID,
			"error", err,
		)
	}
}

// deletePublicExperience removes a public experience and its vector.
// Called by processIngestion when LLM returns skip=true (nothing worth extracting).
func deletePublicExperience(expID int64) {
	if err := database.DB.Where("experience_id = ?", expID).
		Delete(&model.PublicExperienceVector{}).Error; err != nil {
		applogger.Error("Failed to delete public_experience_vector during skip cleanup",
			"exp_id", expID,
			"error", err,
		)
	}
	if err := database.DB.Delete(&model.PublicExperience{}, expID).Error; err != nil {
		applogger.Error("Failed to delete public_experience during skip cleanup",
			"exp_id", expID,
			"error", err,
		)
	}
}

// SearchPublicExperiences performs semantic retrieval against public experiences.
// query is an arbitrary search text (entity profile narratives, domain keywords, etc.).
// Returns results sorted by descending cosine similarity, filtered by minScore.
// Returns nil, nil when the embedding service is not configured.
func SearchPublicExperiences(ctx context.Context, query string, topN int, minScore float64) ([]PublicSearchResult, error) {
	if embeddingSvc == nil {
		return nil, nil
	}
	panicIfNotReady()

	queryVec, err := embeddingSvc.EmbedSingle(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embed search query: %w", err)
	}
	if len(queryVec) == 0 {
		applogger.Error("empty queryVec")
		return nil, nil
	}

	type expWithVec struct {
		exp model.PublicExperience
		vec []float32
	}
	var candidates []expWithVec

	var allVectors []model.PublicExperienceVector
	if err := database.DB.Find(&allVectors).Error; err != nil {
		return nil, fmt.Errorf("load public experience vectors: %w", err)
	}

	for _, v := range allVectors {
		var exp model.PublicExperience
		// Only return Active experiences — Generating/Error records are excluded.
		if err := database.DB.Where("id = ? AND status = ?", v.ExperienceID, model.PublicExperienceStatusActive).First(&exp).Error; err != nil {
			applogger.Error("failed to find public experience for vector during search", "experience_id", v.ExperienceID, "error", err)
			continue
		}
		candidates = append(candidates, expWithVec{
			exp: exp,
			vec: vectorutils.BlobToFloat32Slice(v.Embedding),
		})
	}

	if len(candidates) == 0 {
		return nil, nil
	}

	type scoreEntry struct {
		result PublicSearchResult
		score  float64
	}
	var entries []scoreEntry

	for _, c := range candidates {
		sim := vectorutils.CosineSimilarity(queryVec, c.vec)
		if sim >= minScore {
			entries = append(entries, scoreEntry{
				result: PublicSearchResult{Experience: c.exp, Score: sim},
				score:  sim,
			})
		}
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].score > entries[j].score })

	if len(entries) > topN {
		entries = entries[:topN]
	}

	results := make([]PublicSearchResult, len(entries))
	for i, s := range entries {
		results[i] = s.result
	}

	applogger.Info("Public experience search completed",
		"candidates", len(candidates),
		"results", len(results),
	)
	return results, nil
}
