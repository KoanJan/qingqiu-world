package kb

import (
	"context"
	"fmt"
	"sort"
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

const (
	// candidateFetchFactor enlarges the vector-index fetch when a document
	// filter is active: candidates are filtered afterwards, so fetching only
	// topK could starve the result set of the target document.
	candidateFetchFactor = 4
	// maxFilteredFetchK caps the enlarged fetch to bound latency on large KBs.
	maxFilteredFetchK = 200
)

// searchOneKB searches a single knowledge base for relevant chunks.
// When docIDs is non-empty, results are restricted to chunks of these
// documents (metadata filter); extra candidates are fetched before truncating
// to topK so the filter does not starve the result set.
func searchOneKB(ctx context.Context, kbID int64, query string, topK int, docIDs []int64) ([]schema.SearchResult, error) {
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

	fetchK := topK
	if len(docIDs) > 0 {
		fetchK = topK * candidateFetchFactor
		if fetchK > maxFilteredFetchK {
			fetchK = maxFilteredFetchK
		}
	}

	candidates, err := mgr.Search(queryVec, fetchK)
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

	if len(docIDs) > 0 {
		candidates = filterCandidatesByDocuments(candidates, docIDs)
		if len(candidates) > topK {
			candidates = candidates[:topK]
		}
	}

	return candidatesToResults(candidates, kbID), nil
}

// searchKB searches a single knowledge base without any document filter.
func searchKB(ctx context.Context, kbID int64, query string, topK int) ([]schema.SearchResult, error) {
	return searchOneKB(ctx, kbID, query, topK, nil)
}

// SearchKBFiltered searches a single knowledge base, optionally restricted to
// documents whose title matches documentTitle (metadata filter). Matching is
// exact first, then substring. An empty documentTitle disables the filter.
func SearchKBFiltered(ctx context.Context, kbID int64, query string, topK int, documentTitle string) ([]schema.SearchResult, error) {
	if documentTitle == "" {
		return searchOneKB(ctx, kbID, query, topK, nil)
	}
	docIDs, err := resolveDocumentIDs(kbID, documentTitle)
	if err != nil {
		return nil, fmt.Errorf("resolve document filter %q in KB %d: %w", documentTitle, kbID, err)
	}
	if len(docIDs) == 0 {
		// No document matches the filter in this KB: nothing can be returned.
		return make([]schema.SearchResult, 0), nil
	}
	return searchOneKB(ctx, kbID, query, topK, docIDs)
}

// SearchMultiKBFiltered searches multiple knowledge bases concurrently with an
// optional per-KB document title filter (see SearchKBFiltered).
func SearchMultiKBFiltered(ctx context.Context, kbIDs []int64, query string, topK int, documentTitle string) ([]schema.SearchResult, error) {
	return searchMultiKB(ctx, kbIDs, query, topK, documentTitle)
}

// resolveDocumentIDs returns the IDs of non-deleted documents in the KB whose
// title matches documentTitle: exact match first, substring fallback.
func resolveDocumentIDs(kbID int64, documentTitle string) ([]int64, error) {
	var ids []int64
	if err := database.DB.Model(&model.Document{}).
		Where("knowledge_base_id = ? AND status != ? AND title = ?", kbID, model.DocumentStatusDeleted, documentTitle).
		Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		if err := database.DB.Model(&model.Document{}).
			Where("knowledge_base_id = ? AND status != ? AND title LIKE ?", kbID, model.DocumentStatusDeleted, "%"+documentTitle+"%").
			Pluck("id", &ids).Error; err != nil {
			return nil, err
		}
	}
	return ids, nil
}

// filterCandidatesByDocuments keeps only candidates whose chunk belongs to one
// of the given documents. Chunk membership is resolved with a single query.
// On query failure it logs and returns the unfiltered candidates instead of
// silently returning empty results.
func filterCandidatesByDocuments(candidates []searchCandidate, docIDs []int64) []searchCandidate {
	if len(candidates) == 0 || len(docIDs) == 0 {
		return nil
	}
	var chunkIDs []int64
	if err := database.DB.Model(&model.DocumentChunk{}).
		Where("document_id IN ?", docIDs).
		Pluck("id", &chunkIDs).Error; err != nil {
		applogger.Error("failed to load chunk IDs for document filter, keeping unfiltered candidates", "doc_ids", docIDs, "error", err)
		return candidates
	}
	set := make(map[int64]struct{}, len(chunkIDs))
	for _, id := range chunkIDs {
		set[id] = struct{}{}
	}
	filtered := make([]searchCandidate, 0, len(candidates))
	for _, c := range candidates {
		if _, ok := set[int64(c.ChunkID)]; ok {
			filtered = append(filtered, c)
		}
	}
	return filtered
}

// searchMultiKB searches multiple knowledge bases concurrently.
// Each KB is searched independently (embedding included) and failures are
// logged and skipped so one broken KB cannot block the others.
// When documentTitle is non-empty, each KB's results are restricted to the
// documents whose titles match it (see SearchKBFiltered).
func searchMultiKB(ctx context.Context, kbIDs []int64, query string, topK int, documentTitle string) ([]schema.SearchResult, error) {
	if topK <= 0 {
		topK = DefaultSearchTopK
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

			var docIDs []int64
			if documentTitle != "" {
				ids, err := resolveDocumentIDs(id, documentTitle)
				if err != nil {
					applogger.Error("multiSearch: failed to resolve document filter", "kb_id", id, "document", documentTitle, "error", err)
					ch <- kbResult{err: err, kbID: id}
					return
				}
				if len(ids) == 0 {
					// No matching document in this KB: contribute nothing.
					ch <- kbResult{kbID: id}
					return
				}
				docIDs = ids
			}

			results, err := searchOneKB(ctx, id, query, topK, docIDs)
			ch <- kbResult{results: results, err: err, kbID: id}
		}(kbID)
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	allResults := make([]schema.SearchResult, 0)
	for res := range ch {
		if res.err != nil {
			applogger.Warn("multiSearch: KB search failed, skipping this KB", "kb_id", res.kbID, "error", res.err)
			continue
		}
		allResults = append(allResults, res.results...)
	}

	// Each KB returns its own top-K hits: rank them globally by score and
	// truncate to topK so the combined result has the same shape and
	// relevance ordering as a single-KB search, regardless of how many KBs
	// were searched. Scores are comparable across KBs because every KB uses
	// the same embedding model and similarity metric.
	sort.Slice(allResults, func(i, j int) bool { return allResults[i].Score > allResults[j].Score })
	if len(allResults) > topK {
		allResults = allResults[:topK]
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
