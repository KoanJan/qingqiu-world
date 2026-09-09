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

	// DefaultKeywordRatio is the default weight (alpha) of the keyword (BM25)
	// score in hybrid retrieval when a KB has no explicit configuration.
	DefaultKeywordRatio = 0.3
)

// getKeywordRatio loads the per-KB keyword score weight (alpha) used by
// hybrid retrieval: score = (1-alpha)*vector + alpha*keyword. The value is
// clamped to [0, 1]. On load failure it falls back to the default and logs;
// a missing configuration must not break retrieval.
func getKeywordRatio(kbID int64) float64 {
	var kb model.KnowledgeBase
	if err := database.DB.Select("keyword_ratio").First(&kb, kbID).Error; err != nil {
		applogger.Warn("search: failed to load keyword ratio, using default", "kb_id", kbID, "error", err)
		return DefaultKeywordRatio
	}
	ratio := kb.KeywordRatio
	if ratio < 0 || ratio > 1 {
		applogger.Warn("search: keyword ratio out of range, clamping", "kb_id", kbID, "ratio", ratio)
		if ratio < 0 {
			ratio = 0
		} else {
			ratio = 1
		}
	}
	return ratio
}

// searchOneKB searches a single knowledge base for relevant chunks.
// Retrieval is hybrid: the vector path (cosine similarity) and the keyword
// path (BM25) each produce a candidate list, both lists are normalized by
// their maximum score and blended with the per-KB keyword ratio alpha:
// score = (1-alpha)*vecNorm + alpha*kwNorm. alpha=0/1 degrades to pure
// vector/keyword search and skips the unused path entirely.
// When docIDs is non-empty, results are restricted to chunks of these
// documents (metadata filter); extra candidates are fetched before truncating
// to topK so the filter does not starve the result set.
func searchOneKB(ctx context.Context, kbID int64, query string, topK int, docIDs []int64) ([]schema.SearchResult, error) {
	// Normalize an unset or non-positive topK to the default so callers
	// passing 0 still get meaningful results.
	if topK <= 0 {
		topK = DefaultSearchTopK
	}

	alpha := getKeywordRatio(kbID)

	fetchK := topK
	if len(docIDs) > 0 {
		fetchK = topK * candidateFetchFactor
		if fetchK > maxFilteredFetchK {
			fetchK = maxFilteredFetchK
		}
	}

	// Soft-deleted chunks must be excluded from both retrieval paths.
	var deletedChunkIDs []int64
	if err := database.DB.Model(&model.DocumentChunk{}).
		Where("knowledge_base_id = ? AND deleted = 1", kbID).
		Pluck("id", &deletedChunkIDs).Error; err != nil {
		applogger.Error("search: failed to load deleted chunk IDs, results may include deleted chunks", "kb_id", kbID, "error", err)
	}
	tracker := newDeletedVectorTracker()
	tracker.LoadDeletedChunkIDs(deletedChunkIDs)

	var vecCandidates, kwCandidates []searchCandidate

	if alpha < 1 {
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
		candidates, err := mgr.Search(queryVec, fetchK)
		if err != nil {
			return nil, fmt.Errorf("search failed: %w", err)
		}
		vecCandidates = tracker.FilterCandidates(candidates)
	}

	if alpha > 0 {
		idx, err := getOrCreateBM25Index(kbID)
		if err != nil {
			// A broken keyword path degrades to vector-only search instead
			// of failing the whole request.
			applogger.Error("search: failed to get BM25 index, degrading to vector-only", "kb_id", kbID, "error", err)
		} else {
			kwCandidates = tracker.FilterCandidates(idx.Search(query, fetchK))
		}
	}

	merged := blendHybrid(vecCandidates, kwCandidates, alpha)

	if len(docIDs) > 0 {
		merged = filterCandidatesByDocuments(merged, docIDs)
	}
	if len(merged) > topK {
		merged = merged[:topK]
	}

	return candidatesToResults(merged, kbID), nil
}

// blendHybrid merges vector and keyword candidates into a single ranked list.
// Each list is normalized by its maximum score first (BM25 is unbounded while
// cosine is not directly comparable to it), then weighted linearly:
//
//	score = (1-alpha)*vecNorm + alpha*kwNorm
//
// A chunk found by only one path keeps the weighted score of that path alone.
func blendHybrid(vec, kw []searchCandidate, alpha float64) []searchCandidate {
	normalizeScores(vec)
	normalizeScores(kw)

	merged := make(map[uint64]float64, len(vec)+len(kw))
	for _, c := range vec {
		merged[c.ChunkID] += (1 - alpha) * c.Score
	}
	for _, c := range kw {
		merged[c.ChunkID] += alpha * c.Score
	}

	results := make([]searchCandidate, 0, len(merged))
	for chunkID, score := range merged {
		results = append(results, searchCandidate{ChunkID: chunkID, Score: score})
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	return results
}

// normalizeScores scales scores in place so the maximum becomes 1, making
// BM25 (unbounded) and cosine scores comparable before weighting. A list
// whose maximum is non-positive carries no meaningful signal and is left
// unchanged.
func normalizeScores(candidates []searchCandidate) {
	var max float64
	for _, c := range candidates {
		if c.Score > max {
			max = c.Score
		}
	}
	if max <= 0 {
		return
	}
	for i := range candidates {
		candidates[i].Score /= max
	}
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
