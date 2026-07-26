package experience

import (
	"context"
	"fmt"
	"sort"

	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/vectorutils"
)

// SearchResult wraps an AgentExperience with its cosine similarity score.
type SearchResult struct {
	Experience model.AgentExperience
	Score      float64 // Cosine similarity, 0..1
}

// SearchExperiences performs semantic retrieval against an agent's private
// experiences. Returns results sorted by descending cosine similarity,
// filtered by minScore.
// Returns nil, nil when the embedding service is not configured.
func SearchExperiences(ctx context.Context, personID int64, taskDescription string, topN int, minScore float64) ([]SearchResult, error) {
	if embeddingSvc == nil {
		return nil, nil
	}
	panicIfNotReady()

	queryVec, err := embeddingSvc.EmbedSingle(ctx, taskDescription)
	if err != nil {
		return nil, fmt.Errorf("embed task description: %w", err)
	}
	if len(queryVec) == 0 {
		return nil, nil
	}

	// Pre-filter by agent_config_id at the SQL level to avoid loading the entire
	// vectors table. First load this agent's experience IDs, then load only
	// the corresponding vectors.
	var experiences []model.AgentExperience
	if err := database.DB.Where("person_id = ?", personID).Find(&experiences).Error; err != nil {
		return nil, fmt.Errorf("load agent experiences: %w", err)
	}
	if len(experiences) == 0 {
		return nil, nil
	}

	expIDs := make([]int64, len(experiences))
	expMap := make(map[int64]model.AgentExperience, len(experiences))
	for i, e := range experiences {
		expIDs[i] = e.ID
		expMap[e.ID] = e
	}

	var vectors []model.AgentExperienceVector
	if err := database.DB.Where("experience_id IN ?", expIDs).Find(&vectors).Error; err != nil {
		return nil, fmt.Errorf("load experience vectors: %w", err)
	}

	type scoreEntry struct {
		result SearchResult
		score  float64
	}
	var entries []scoreEntry

	for _, v := range vectors {
		exp, ok := expMap[v.ExperienceID]
		if !ok {
			// Vector exists but its parent experience row is missing —
			// data integrity violation. Error-level because this should
			// never happen under normal operation (cascade should keep
			// vectors in sync with their parent rows).
			applogger.Error("Experience retrieval: vector has no matching experience row (data integrity violation)",
				"experience_id", v.ExperienceID,
				"person_id", personID,
			)
			continue
		}
		sim := vectorutils.CosineSimilarity(queryVec, vectorutils.BlobToFloat32Slice(v.Embedding))
		if sim >= minScore {
			entries = append(entries, scoreEntry{
				result: SearchResult{Experience: exp, Score: sim},
				score:  sim,
			})
		}
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].score > entries[j].score })

	if len(entries) > topN {
		entries = entries[:topN]
	}

	results := make([]SearchResult, len(entries))
	for i, s := range entries {
		results[i] = s.result
	}

	applogger.Info("Experience retrieval completed",
		"person_id", personID,
		"candidates", len(vectors),
		"results", len(results),
	)
	return results, nil
}
