package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/vectorutils"
)

const (
	// RecallMemoryMaxPageSize bounds a single page of memory records.
	RecallMemoryMaxPageSize = 20
	// RecallMemoryMaxQueryRunes bounds a memory search query.
	RecallMemoryMaxQueryRunes = 200
	// cognitiveCandidateScanWindow bounds in-process vector ranking.
	cognitiveCandidateScanWindow = 2000
	// literalMatchSimilarity ranks a direct text hit above cosine matches.
	literalMatchSimilarity = 1.0
)

// CognitiveCandidate is a subject-owned concern with its current impression and
// understanding. Similarity is used internally for ordering; it is never an
// assessment of truth. Its three timestamps distinguish identity creation,
// current revision formation, and the latest completed review (which may create
// no revision).
type CognitiveCandidate struct {
	MemoryID                     int64     `json:"memory_id"`
	Description                  string    `json:"description"`
	Impression                   string    `json:"impression"`
	Understanding                string    `json:"understanding"`
	Revision                     int       `json:"revision"`
	ConcernCreatedAt             time.Time `json:"concern_created_at"`
	CurrentUnderstandingFormedAt time.Time `json:"current_understanding_formed_at"`
	LastReviewedAt               time.Time `json:"last_reviewed_at"`
	similarity                   float64
}

// CognitiveTimeField selects which completed-memory timestamp a recall filters.
// An omitted field uses the latest review time.
type CognitiveTimeField int

const (
	CognitiveTimeDefault CognitiveTimeField = iota
	CognitiveTimeCreated
	CognitiveTimeReviewed
)

// EmbedCognitiveText builds a rebuildable search representation. The caller
// may preserve a concern or understanding without a vector on failure.
func EmbedCognitiveText(ctx context.Context, value string) ([]float32, error) {
	if embeddingSvc == nil {
		return nil, fmt.Errorf("embedding service unavailable")
	}
	return embeddingSvc.EmbedSingle(ctx, value)
}

// FindCognitiveMemories returns a bounded candidate list. An embedding miss or
// absence is exposed through coverage, never interpreted as proof of nonexistence.
func FindCognitiveMemories(ctx context.Context, personID int64, query string, from, to time.Time, timeField CognitiveTimeField, limit, offset int, includeImpression bool) ([]CognitiveCandidate, bool, string, error) {
	if personID <= 0 {
		return nil, false, "", fmt.Errorf("cognitive memory owner must be positive")
	}
	if limit < 1 || limit > RecallMemoryMaxPageSize || offset < 0 {
		return nil, false, "", fmt.Errorf("cognitive memory page requires limit 1–%d and nonnegative offset", RecallMemoryMaxPageSize)
	}
	if timeField < CognitiveTimeDefault || timeField > CognitiveTimeReviewed {
		return nil, false, "", fmt.Errorf("unknown cognitive memory time_field %d", timeField)
	}
	if len([]rune(query)) > RecallMemoryMaxQueryRunes {
		return nil, false, "", fmt.Errorf("cognitive memory query exceeds %d characters", RecallMemoryMaxQueryRunes)
	}
	if !from.IsZero() && !to.IsZero() && !from.Before(to) {
		return nil, false, "", fmt.Errorf("cognitive memory from_time must precede to_time")
	}
	q := database.DB.WithContext(ctx).Where("person_id = ?", personID)
	column := "updated_at"
	if timeField == CognitiveTimeCreated {
		column = "created_at"
	}
	if !from.IsZero() {
		q = q.Where(column+" >= ?", from)
	}
	if !to.IsZero() {
		q = q.Where(column+" < ?", to)
	}
	// In-process vector ranking is bounded; direct ID reads and narrower time
	// intervals can still reach memories outside this candidate window.
	var records []model.CognitiveMemory
	if err := q.Order("updated_at DESC, id DESC").Limit(cognitiveCandidateScanWindow + 1).Find(&records).Error; err != nil {
		return nil, false, "", err
	}
	coverage := "Current subject-owned understandings; retrieve original records to check their claims."
	if len(records) > cognitiveCandidateScanWindow {
		records = records[:cognitiveCandidateScanWindow]
		coverage += fmt.Sprintf(" Only the %d most recently reviewed memories were searched.", cognitiveCandidateScanWindow)
	}
	if strings.TrimSpace(query) != "" {
		if embeddingSvc == nil {
			coverage += " Semantic index unavailable; literal description candidates only."
		} else {
			vector, err := embeddingSvc.EmbedSingle(ctx, query)
			if err != nil {
				applogger.Error("cognitive memory semantic query failed", "person_id", personID, "error", err)
				coverage += " Semantic query unavailable; literal description candidates only."
			} else {
				return rankedCognitiveCandidates(ctx, records, vector, query, limit, offset, coverage, includeImpression)
			}
		}
	}
	return rankedCognitiveCandidates(ctx, records, nil, query, limit, offset, coverage, includeImpression)
}

// rankedCognitiveCandidates orders visible identities without deciding whether
// two concerns are semantically the same or whether an understanding is true.
func rankedCognitiveCandidates(ctx context.Context, records []model.CognitiveMemory, vector []float32, query string, limit, offset int, coverage string, includeImpression bool) ([]CognitiveCandidate, bool, string, error) {
	results := make([]CognitiveCandidate, 0, len(records))
	terms := strings.Fields(strings.ToLower(query))
	missingVectors := false
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return nil, false, "", err
		}
		candidate := CognitiveCandidate{MemoryID: record.ID, Description: record.Description, ConcernCreatedAt: record.CreatedAt, LastReviewedAt: record.UpdatedAt}
		if len(vector) > 0 {
			stored := vectorutils.BlobToFloat32Slice(record.Embedding)
			if len(stored) == len(vector) && len(stored) > 0 {
				candidate.similarity = vectorutils.CosineSimilarity(vector, stored)
			} else {
				missingVectors = true
			}
			if includeImpression {
				current := vectorutils.BlobToFloat32Slice(record.ImpressionEmbedding)
				if len(current) == len(vector) && len(current) > 0 {
					candidate.similarity = max(candidate.similarity, vectorutils.CosineSimilarity(vector, current))
				} else {
					missingVectors = true
				}
			}
			if strings.Contains(strings.ToLower(record.Description), strings.ToLower(query)) {
				candidate.similarity = literalMatchSimilarity
			}
		} else if len(terms) > 0 {
			matched := false
			for _, term := range terms {
				if strings.Contains(strings.ToLower(record.Description), term) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		results = append(results, candidate)
	}
	if missingVectors {
		coverage += " Some memories lack a compatible vector."
	}
	if len(vector) > 0 {
		sort.SliceStable(results, func(i, j int) bool { return results[i].similarity > results[j].similarity })
	}
	if offset >= len(results) {
		return []CognitiveCandidate{}, false, coverage, nil
	}
	end := offset + limit
	hasMore := end < len(results)
	if end > len(results) {
		end = len(results)
	}
	selected := results[offset:end]
	ids := make([]int64, 0, len(selected))
	for _, item := range selected {
		ids = append(ids, item.MemoryID)
	}
	var versions []model.CognitiveMemoryVersion
	if err := database.DB.WithContext(ctx).Table("cognitive_memory_versions v").Select("v.*").Where("v.memory_id IN ? AND v.revision = (SELECT MAX(v2.revision) FROM cognitive_memory_versions v2 WHERE v2.memory_id = v.memory_id)", ids).Find(&versions).Error; err != nil {
		return nil, false, "", err
	}
	byID := make(map[int64]model.CognitiveMemoryVersion, len(versions))
	for _, version := range versions {
		byID[version.MemoryID] = version
	}
	for i := range selected {
		version, ok := byID[selected[i].MemoryID]
		if !ok {
			return nil, false, "", fmt.Errorf("cognitive memory %d has no version", selected[i].MemoryID)
		}
		selected[i].Impression, selected[i].Understanding, selected[i].Revision = version.Impression, version.Understanding, version.Revision
		selected[i].CurrentUnderstandingFormedAt = version.CreatedAt
	}
	return selected, hasMore, coverage, nil
}

// ReadCognitiveMemory checks ownership before returning one concern and its
// bounded revision history. The history remains a subjective record.
func ReadCognitiveMemory(ctx context.Context, personID, memoryID int64, limit, offset int) (CognitiveCandidate, []model.CognitiveMemoryVersion, bool, error) {
	if personID <= 0 || memoryID <= 0 {
		return CognitiveCandidate{}, nil, false, fmt.Errorf("cognitive memory read requires positive owner and memory IDs")
	}
	if limit < 1 || limit > RecallMemoryMaxPageSize || offset < 0 {
		return CognitiveCandidate{}, nil, false, fmt.Errorf("cognitive revision page requires limit 1–%d and nonnegative offset", RecallMemoryMaxPageSize)
	}
	var record model.CognitiveMemory
	if err := database.DB.WithContext(ctx).Where("id = ? AND person_id = ?", memoryID, personID).Take(&record).Error; err != nil {
		return CognitiveCandidate{}, nil, false, err
	}
	// The requested revision page may start after the latest version, but the
	// memory header must still identify when its current understanding formed.
	var current model.CognitiveMemoryVersion
	if err := database.DB.WithContext(ctx).Where("memory_id = ?", record.ID).Order("revision DESC").Take(&current).Error; err != nil {
		return CognitiveCandidate{}, nil, false, err
	}
	var versions []model.CognitiveMemoryVersion
	if err := database.DB.WithContext(ctx).Where("memory_id = ?", record.ID).Order("revision DESC").Limit(limit + 1).Offset(offset).Find(&versions).Error; err != nil {
		return CognitiveCandidate{}, nil, false, err
	}
	hasMore := len(versions) > limit
	if hasMore {
		versions = versions[:limit]
	}
	return CognitiveCandidate{
		MemoryID: record.ID, Description: record.Description,
		Understanding: current.Understanding, Impression: current.Impression, Revision: current.Revision,
		ConcernCreatedAt: record.CreatedAt, CurrentUnderstandingFormedAt: current.CreatedAt, LastReviewedAt: record.UpdatedAt,
	}, versions, hasMore, nil
}
