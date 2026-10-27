package memory

import (
	"context"
	"fmt"
	"sort"
	"time"

	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/energy"
	"qingqiu-world-server/internal/service/vectorutils"
)

const (
	// cognitiveRouteMemoryWindow bounds how many memories are ranked in one
	// routing pass. The window is ordered by latest review, so the least
	// recently reviewed memory is dropped first when the bound is reached.
	cognitiveRouteMemoryWindow = 500
	// cognitiveRouteDailyWindow bounds the incremental dated accounts loaded
	// for one routing pass.
	cognitiveRouteDailyWindow = 1000
	// cognitiveRouteEvidenceCap bounds the triggering accounts shown to the
	// model in one update prompt.
	cognitiveRouteEvidenceCap = 3
	// sqliteAggregateTimestampLayout matches GORM's offset-bearing SQLite
	// datetime values after SQLite returns MAX(updated_at) as text.
	sqliteAggregateTimestampLayout = "2006-01-02 15:04:05.999999999-07:00"
)

// CognitiveReviewCandidate is one existing memory the system selected for a
// review. Similarity is the maximum cosine value across incremental dated
// accounts; it orders candidates and never judges identity or truth.
type CognitiveReviewCandidate struct {
	MemoryID       int64               `json:"memory_id"`
	Revision       int                 `json:"revision"`
	Description    string              `json:"description"`
	Impression     string              `json:"impression"`
	Understanding  string              `json:"understanding"`
	LastReviewedAt time.Time           `json:"last_reviewed_at"`
	Similarity     float64             `json:"-"`
	Evidence       []model.DailyMemory `json:"-"`
}

// cognitiveRouteMemory is one memory plus the time after which dated accounts
// count as incremental evidence for it.
type cognitiveRouteMemory struct {
	memory    model.CognitiveMemory
	watermark time.Time
}

// cognitiveRouteDaily is one dated account with its decoded summary vector.
type cognitiveRouteDaily struct {
	row    model.DailyMemory
	vector []float32
}

// SelectCognitiveUpdateCandidates ranks existing memories by the maximum
// cosine similarity between the memory description vector and the vectors of
// dated accounts created after that memory's review watermark. Only completed
// days are considered; today's accounts are still open. There is no keyword
// fallback: an embedding outage must surface as an error instead of a silent
// literal match.
func SelectCognitiveUpdateCandidates(ctx context.Context, personID int64, dayStart time.Time, limit int, minSimilarity float64) ([]CognitiveReviewCandidate, string, error) {
	if personID <= 0 {
		return nil, "", fmt.Errorf("cognitive route requires a positive owner")
	}
	if limit < 1 {
		return nil, "", fmt.Errorf("cognitive route requires a positive limit")
	}
	if minSimilarity < 0 || minSimilarity > 1 {
		return nil, "", fmt.Errorf("cognitive route minimum similarity must be within 0–1")
	}
	coverage := "Candidates are ordered by the strongest single dated account, not an average, and similarity never decides whether two concerns are identical."
	memories, coverage, err := loadCognitiveRouteMemories(ctx, personID, dayStart, coverage)
	if err != nil {
		return nil, coverage, err
	}
	if len(memories) == 0 {
		return []CognitiveReviewCandidate{}, coverage, nil
	}
	dailies, coverage, err := loadCognitiveRouteDailies(ctx, personID, memories, coverage)
	if err != nil {
		return nil, coverage, err
	}
	type ranked struct {
		memory   cognitiveRouteMemory
		best     float64
		evidence []model.DailyMemory
	}
	var results []ranked
	for _, entry := range memories {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, coverage, ctxErr
		}
		memoryVector := vectorutils.BlobToFloat32Slice(entry.memory.Embedding)
		if len(memoryVector) == 0 {
			// The write path rejects empty vectors, so a stored empty vector is
			// a legacy defect that background backfill must repair.
			applogger.Error("cognitive route skipped a memory without a description vector", "person_id", personID, "memory_id", entry.memory.ID)
			continue
		}
		type scored struct {
			row   model.DailyMemory
			score float64
		}
		matches := make([]scored, 0, len(dailies))
		for _, daily := range dailies {
			if !daily.row.CreatedAt.After(entry.watermark) {
				continue
			}
			if len(daily.vector) != len(memoryVector) {
				continue
			}
			score := vectorutils.CosineSimilarity(memoryVector, daily.vector)
			matches = append(matches, scored{row: daily.row, score: score})
		}
		if len(matches) == 0 {
			continue
		}
		sort.SliceStable(matches, func(i, j int) bool { return matches[i].score > matches[j].score })
		if matches[0].score < minSimilarity {
			continue
		}
		best := matches[0].score
		evidence := make([]model.DailyMemory, 0, cognitiveRouteEvidenceCap)
		for _, match := range matches {
			if len(evidence) == cognitiveRouteEvidenceCap {
				break
			}
			evidence = append(evidence, match.row)
		}
		results = append(results, ranked{memory: entry, best: best, evidence: evidence})
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].best > results[j].best })
	if len(results) > limit {
		results = results[:limit]
	}
	candidates := make([]CognitiveReviewCandidate, 0, len(results))
	ids := make([]int64, 0, len(results))
	for _, item := range results {
		ids = append(ids, item.memory.memory.ID)
	}
	versions, err := currentCognitiveVersions(ctx, ids)
	if err != nil {
		return nil, coverage, err
	}
	for _, item := range results {
		version, ok := versions[item.memory.memory.ID]
		if !ok {
			return nil, coverage, fmt.Errorf("cognitive memory %d has no version", item.memory.memory.ID)
		}
		candidates = append(candidates, CognitiveReviewCandidate{
			MemoryID:       item.memory.memory.ID,
			Revision:       version.Revision,
			Description:    item.memory.memory.Description,
			Impression:     version.Impression,
			Understanding:  version.Understanding,
			LastReviewedAt: item.memory.memory.UpdatedAt,
			Similarity:     item.best,
			Evidence:       item.evidence,
		})
	}
	return candidates, coverage, nil
}

// loadCognitiveRouteMemories loads memories outside today's review quota and
// computes each review watermark: the later of the memory's own last review
// and the last process that ended with a recorded result.
func loadCognitiveRouteMemories(ctx context.Context, personID int64, dayStart time.Time, coverage string) ([]cognitiveRouteMemory, string, error) {
	var attempted []model.CognitiveProcess
	if err := database.DB.WithContext(ctx).
		Select("memory_id").
		Where("person_id = ? AND status = ? AND updated_at >= ? AND memory_id > 0", personID, model.CognitiveProcessEnded, dayStart).
		Find(&attempted).Error; err != nil {
		return nil, coverage, err
	}
	attemptedToday := make(map[int64]bool, len(attempted))
	for _, process := range attempted {
		attemptedToday[process.MemoryID] = true
	}
	var records []model.CognitiveMemory
	if err := database.DB.WithContext(ctx).
		Where("person_id = ? AND updated_at < ? AND length(embedding) > 0", personID, dayStart).
		Order("updated_at DESC, id DESC").
		Limit(cognitiveRouteMemoryWindow + 1).
		Find(&records).Error; err != nil {
		return nil, coverage, err
	}
	if len(records) > cognitiveRouteMemoryWindow {
		records = records[:cognitiveRouteMemoryWindow]
		coverage += fmt.Sprintf(" Only the %d most recently reviewed memories were ranked.", cognitiveRouteMemoryWindow)
		applogger.Warn("cognitive route memory window reached", "person_id", personID, "window", cognitiveRouteMemoryWindow)
	}
	var watermarks []struct {
		MemoryID   int64
		MaxUpdated string
	}
	if err := database.DB.WithContext(ctx).
		Model(&model.CognitiveProcess{}).
		Select("memory_id, MAX(updated_at) AS max_updated").
		Where("person_id = ? AND memory_id > 0 AND status = ? AND outcome IN ?", personID, model.CognitiveProcessEnded,
			[]model.CognitiveProcessOutcome{model.CognitiveProcessOutcomeWritten, model.CognitiveProcessOutcomeConfirmed, model.CognitiveProcessOutcomeNoResult}).
		Group("memory_id").
		Scan(&watermarks).Error; err != nil {
		return nil, coverage, err
	}
	processWatermark := make(map[int64]time.Time, len(watermarks))
	for _, row := range watermarks {
		// SQLite returns an aggregate timestamp as text even when the source
		// column is datetime. Parse the offset-bearing format GORM persists.
		updatedAt, err := time.Parse(sqliteAggregateTimestampLayout, row.MaxUpdated)
		if err != nil {
			return nil, coverage, fmt.Errorf("invalid review time for cognitive memory %d: %w", row.MemoryID, err)
		}
		processWatermark[row.MemoryID] = updatedAt
	}
	result := make([]cognitiveRouteMemory, 0, len(records))
	for _, record := range records {
		if attemptedToday[record.ID] {
			continue
		}
		watermark := record.UpdatedAt
		if later := processWatermark[record.ID]; later.After(watermark) {
			watermark = later
		}
		result = append(result, cognitiveRouteMemory{memory: record, watermark: watermark})
	}
	return result, coverage, nil
}

// loadCognitiveRouteDailies loads completed-day accounts created after the
// earliest candidate watermark. Using creation time, not end time, keeps a
// dated account that is generated late from being excluded forever.
func loadCognitiveRouteDailies(ctx context.Context, personID int64, memories []cognitiveRouteMemory, coverage string) ([]cognitiveRouteDaily, string, error) {
	earliest := memories[0].watermark
	for _, entry := range memories[1:] {
		if entry.watermark.Before(earliest) {
			earliest = entry.watermark
		}
	}
	var rows []model.DailyMemory
	if err := database.DB.WithContext(ctx).
		Where("person_id = ? AND local_date < ? AND created_at > ?", personID, energy.Today(), earliest).
		Order("created_at DESC").
		Limit(cognitiveRouteDailyWindow + 1).
		Find(&rows).Error; err != nil {
		return nil, coverage, err
	}
	if len(rows) > cognitiveRouteDailyWindow {
		rows = rows[:cognitiveRouteDailyWindow]
		coverage += fmt.Sprintf(" Only the %d most recently generated dated accounts were ranked.", cognitiveRouteDailyWindow)
		applogger.Warn("cognitive route daily window reached", "person_id", personID, "window", cognitiveRouteDailyWindow)
	}
	result := make([]cognitiveRouteDaily, 0, len(rows))
	missing := 0
	var firstMissingID int64
	for _, row := range rows {
		vector := vectorutils.BlobToFloat32Slice(row.Embedding)
		if len(vector) == 0 {
			missing++
			if firstMissingID == 0 {
				firstMissingID = row.ID
			}
			continue
		}
		result = append(result, cognitiveRouteDaily{row: row, vector: vector})
	}
	if missing > 0 {
		coverage += " Some dated accounts lack a summary vector and were not ranked."
		applogger.Error("cognitive route skipped dated accounts without summary vectors", "person_id", personID, "count", missing, "first_memory_id", firstMissingID)
	}
	return result, coverage, nil
}

// currentCognitiveVersions reads the latest version for each selected memory.
func currentCognitiveVersions(ctx context.Context, ids []int64) (map[int64]model.CognitiveMemoryVersion, error) {
	versions := make(map[int64]model.CognitiveMemoryVersion, len(ids))
	if len(ids) == 0 {
		return versions, nil
	}
	var rows []model.CognitiveMemoryVersion
	if err := database.DB.WithContext(ctx).Table("cognitive_memory_versions v").Select("v.*").
		Where("v.memory_id IN ? AND v.revision = (SELECT MAX(v2.revision) FROM cognitive_memory_versions v2 WHERE v2.memory_id = v.memory_id)", ids).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		versions[row.MemoryID] = row
	}
	return versions, nil
}
