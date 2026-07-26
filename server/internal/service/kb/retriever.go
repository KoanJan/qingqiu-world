package kb

import (
	"context"
	"fmt"
	"sync"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/schema"
	"qingqiu-world-server/internal/service/llm"
)

// getEmbeddingService creates an EmbeddingService from the global config.
func getEmbeddingService() (*llm.EmbeddingService, error) {
	config := dops.GetEmbeddingConfig()
	if config == nil {
		return nil, fmt.Errorf("no global embedding config")
	}
	return llm.NewEmbeddingService(config.BaseURL, config.APIKey, config.ModelID, embeddingDim), nil
}

// getEmbeddingServiceForKB creates an EmbeddingService instance for a knowledge base.
// Deprecated: use getEmbeddingService() instead.
func getEmbeddingServiceForKB(kbID int64) (*llm.EmbeddingService, error) {
	return getEmbeddingService()
}

// searchKB searches a single knowledge base for relevant chunks.
func searchKB(ctx context.Context, kbID int64, query string, topK int) ([]schema.SearchResult, error) {
	embService, err := getEmbeddingService()
	if err != nil {
		return nil, err
	}

	queryVec, err := embService.EmbedSingle(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to embed query: %w", err)
	}

	mgr, err := getOrCreateIndexManager(kbID)
	if err != nil {
		return nil, fmt.Errorf("failed to get index manager: %w", err)
	}

	candidates, err := mgr.Search(queryVec, topK)
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}

	var deletedChunkIDs []int64
	if err := database.DB.Model(&model.DocumentChunk{}).
		Where("knowledge_base_id = ? AND deleted = 1", kbID).
		Pluck("id", &deletedChunkIDs).Error; err != nil {
		applogger.Error("search: failed to load deleted chunk IDs, results may include deleted chunks", "kb_id", kbID, "error", err)
	}

	tracker := newDeletedVectorTracker()
	tracker.LoadDeletedChunkIDs(deletedChunkIDs)
	candidates = tracker.FilterCandidates(candidates)

	return candidatesToResults(candidates, kbID), nil
}

// searchMultiKB searches multiple knowledge bases concurrently.
// Generates one query embedding per KB (since KBs may use different models),
// then searches each KB in parallel.
func searchMultiKB(ctx context.Context, kbIDs []int64, query string, topK int) ([]schema.SearchResult, error) {
	if topK <= 0 {
		topK = DefaultSearchTopK
	}

	embService, err := getEmbeddingService()
	if err != nil {
		return nil, err
	}

	type kbResult struct {
		results []schema.SearchResult
		err     error
		kbID    int64
	}

	ch := make(chan kbResult, len(kbIDs))
	var wg sync.WaitGroup

	for _, kbID := range kbIDs {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()

			queryVec, err := embService.EmbedSingle(ctx, query)
			if err != nil {
				ch <- kbResult{err: err, kbID: id}
				return
			}

			mgr, err := getOrCreateIndexManager(id)
			if err != nil {
				ch <- kbResult{err: err, kbID: id}
				return
			}

			candidates, err := mgr.Search(queryVec, topK)
			if err != nil {
				ch <- kbResult{err: err, kbID: id}
				return
			}

			var deletedChunkIDs []int64
			if err := database.DB.Model(&model.DocumentChunk{}).
				Where("knowledge_base_id = ? AND deleted = 1", id).
				Pluck("id", &deletedChunkIDs).Error; err != nil {
				applogger.Error("multiSearch: failed to load deleted chunk IDs", "kb_id", id, "error", err)
			}

			tracker := newDeletedVectorTracker()
			tracker.LoadDeletedChunkIDs(deletedChunkIDs)
			candidates = tracker.FilterCandidates(candidates)

			ch <- kbResult{results: candidatesToResults(candidates, id), kbID: id}
		}(kbID)
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	allResults := make([]schema.SearchResult, 0)
	for res := range ch {
		if res.err != nil {
			continue
		}
		allResults = append(allResults, res.results...)
	}

	return allResults, nil
}

func candidatesToResults(candidates []searchCandidate, kbID int64) []schema.SearchResult {
	if len(candidates) == 0 {
		return make([]schema.SearchResult, 0)
	}

	results := make([]schema.SearchResult, 0, len(candidates))
	for _, c := range candidates {
		var chunk model.DocumentChunk
		if err := database.DB.First(&chunk, int64(c.ChunkID)).Error; err != nil {
			applogger.Error("failed to find document chunk in retriever", "chunk_id", c.ChunkID, "error", err)
			continue
		}
		if chunk.Deleted == 1 {
			continue
		}

		var doc model.Document
		if err := database.DB.Select("id, title").First(&doc, chunk.DocumentID).Error; err != nil {
			applogger.Error("failed to find document in retriever", "document_id", chunk.DocumentID, "error", err)
			continue
		}

		results = append(results, schema.SearchResult{
			ChunkID:         int64(c.ChunkID),
			DocumentID:      chunk.DocumentID,
			DocumentTitle:   doc.Title,
			Content:         chunk.Content,
			Score:           c.Score,
			KnowledgeBaseID: kbID,
		})
	}

	return results
}
