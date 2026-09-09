package kb

import (
	"math"
	"sort"
	"strings"
	"sync"
	"unicode"

	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
)

// BM25 ranking parameters (standard Okapi values).
const (
	// bm25K1 controls term-frequency saturation.
	bm25K1 = 1.2
	// bm25B controls document-length normalization strength.
	bm25B = 0.75
)

// bm25Index is an in-memory BM25 full-text index for a single knowledge base.
// It complements the vector index with a keyword (lexical) search path for
// hybrid retrieval.
//
// Lifecycle: lazily built from the database on first access, incrementally
// updated when a document finishes processing, and released when the KB is
// deleted or the service shuts down. Chunk contents are immutable and chunk
// IDs are auto-increment, so entries are never mutated once indexed.
//
// Deletion handling mirrors the vector path: soft-deleted chunks stay in the
// index and are filtered at query time via the deleted-chunk set loaded by
// the retriever; hard-deleted chunks (failed-processing cleanup) are filtered
// when results are converted back to chunk rows.
type bm25Index struct {
	mu       sync.RWMutex
	docLens  map[int64]int            // chunkID -> token count
	inverted map[string]map[int64]int // term -> chunkID -> term frequency
	totalLen int                      // sum of all indexed document lengths
}

// bm25ChunkDoc is a chunk document to be indexed.
type bm25ChunkDoc struct {
	ChunkID int64
	Content string
}

// newBM25Index creates an empty BM25 index.
func newBM25Index() *bm25Index {
	return &bm25Index{
		docLens:  make(map[int64]int),
		inverted: make(map[string]map[int64]int),
	}
}

// AddDocuments indexes chunk documents. Chunks already present are skipped,
// which keeps repeated adds (e.g. a lazy build racing with an incremental
// add) idempotent.
func (idx *bm25Index) AddDocuments(docs []bm25ChunkDoc) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	for _, d := range docs {
		if _, ok := idx.docLens[d.ChunkID]; ok {
			continue
		}
		tokens := tokenize(d.Content)
		idx.docLens[d.ChunkID] = len(tokens)
		idx.totalLen += len(tokens)
		for _, t := range tokens {
			postings, ok := idx.inverted[t]
			if !ok {
				postings = make(map[int64]int)
				idx.inverted[t] = postings
			}
			postings[d.ChunkID]++
		}
	}
}

// Search scores indexed chunks against the query with BM25 and returns the
// top-K candidates in descending score order.
func (idx *bm25Index) Search(query string, topK int) []searchCandidate {
	if topK <= 0 {
		return nil
	}
	terms := tokenize(query)
	if len(terms) == 0 {
		return nil
	}

	idx.mu.RLock()
	defer idx.mu.RUnlock()

	n := len(idx.docLens)
	if n == 0 {
		return nil
	}
	avgdl := float64(idx.totalLen) / float64(n)
	if avgdl <= 0 {
		return nil
	}

	// Duplicate query terms count once so repeated keywords cannot skew ranking.
	uniq := make(map[string]struct{}, len(terms))
	for _, t := range terms {
		uniq[t] = struct{}{}
	}

	scores := make(map[int64]float64)
	for term := range uniq {
		postings := idx.inverted[term]
		df := len(postings)
		if df == 0 {
			continue
		}
		idf := math.Log(1 + (float64(n)-float64(df)+0.5)/(float64(df)+0.5))
		for chunkID, tf := range postings {
			dl := float64(idx.docLens[chunkID])
			norm := float64(tf) + bm25K1*(1-bm25B+bm25B*dl/avgdl)
			scores[chunkID] += idf * (float64(tf) * (bm25K1 + 1)) / norm
		}
	}
	if len(scores) == 0 {
		return nil
	}

	results := make([]searchCandidate, 0, len(scores))
	for chunkID, score := range scores {
		results = append(results, searchCandidate{ChunkID: uint64(chunkID), Score: score})
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	if len(results) > topK {
		results = results[:topK]
	}
	return results
}

// tokenize splits text into indexable tokens:
//   - runs of CJK characters become overlapping bigrams (a run of length one
//     becomes a single unigram), the standard granularity for Chinese lexical
//     matching without a segmentation dictionary;
//   - non-CJK segments are lowercased and split into alphanumeric words.
func tokenize(text string) []string {
	var tokens []string
	var cjkRun []rune
	var wordRun []rune

	flushCJK := func() {
		if len(cjkRun) == 0 {
			return
		}
		if len(cjkRun) == 1 {
			tokens = append(tokens, string(cjkRun))
		}
		for i := 0; i+1 < len(cjkRun); i++ {
			tokens = append(tokens, string(cjkRun[i:i+2]))
		}
		cjkRun = cjkRun[:0]
	}
	flushWord := func() {
		if len(wordRun) == 0 {
			return
		}
		tokens = append(tokens, string(wordRun))
		wordRun = wordRun[:0]
	}

	for _, r := range strings.ToLower(text) {
		if isCJK(r) {
			flushWord()
			cjkRun = append(cjkRun, r)
			continue
		}
		flushCJK()
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			wordRun = append(wordRun, r)
			continue
		}
		flushWord()
	}
	flushCJK()
	flushWord()
	return tokens
}

// isCJK reports whether r is a CJK ideograph (Han script including
// extensions and compatibility forms).
func isCJK(r rune) bool {
	return (r >= 0x3400 && r <= 0x4DBF) || // CJK Extension A
		(r >= 0x4E00 && r <= 0x9FFF) || // CJK Unified Ideographs
		(r >= 0xF900 && r <= 0xFAFF) // CJK Compatibility Ideographs
}

var (
	bm25Mu      sync.Mutex
	bm25Indexes = make(map[int64]*bm25Index)
)

// getOrCreateBM25Index returns the BM25 index for a knowledge base, building
// it lazily from the database on first access.
func getOrCreateBM25Index(kbID int64) (*bm25Index, error) {
	bm25Mu.Lock()
	defer bm25Mu.Unlock()

	if idx, ok := bm25Indexes[kbID]; ok {
		return idx, nil
	}

	var chunks []model.DocumentChunk
	if err := database.DB.Select("id, content").
		Where("knowledge_base_id = ? AND deleted = 0", kbID).
		Find(&chunks).Error; err != nil {
		return nil, err
	}

	idx := newBM25Index()
	docs := make([]bm25ChunkDoc, 0, len(chunks))
	for _, c := range chunks {
		docs = append(docs, bm25ChunkDoc{ChunkID: c.ID, Content: c.Content})
	}
	idx.AddDocuments(docs)
	bm25Indexes[kbID] = idx
	applogger.Info("Built BM25 index", "kb_id", kbID, "docs", len(docs))
	return idx, nil
}

// updateBM25ForDocument adds a processed document's chunks to the KB's BM25
// index. It builds the index when absent: either the build reads the chunks
// from the database (they are already stored), or it ran before the insert
// and the explicit add below covers them, so chunks cannot be lost.
func updateBM25ForDocument(kbID, docID int64) {
	idx, err := getOrCreateBM25Index(kbID)
	if err != nil {
		applogger.Error("failed to get BM25 index for document update", "kb_id", kbID, "doc_id", docID, "error", err)
		return
	}

	var chunks []model.DocumentChunk
	if err := database.DB.Select("id, content").Where("document_id = ?", docID).Find(&chunks).Error; err != nil {
		applogger.Error("failed to load chunks for BM25 update", "kb_id", kbID, "doc_id", docID, "error", err)
		return
	}

	docs := make([]bm25ChunkDoc, 0, len(chunks))
	for _, c := range chunks {
		docs = append(docs, bm25ChunkDoc{ChunkID: c.ID, Content: c.Content})
	}
	idx.AddDocuments(docs)
}

// releaseBM25Index drops the in-memory BM25 index of a knowledge base.
func releaseBM25Index(kbID int64) {
	bm25Mu.Lock()
	defer bm25Mu.Unlock()
	delete(bm25Indexes, kbID)
}

// releaseAllBM25Indexes drops all in-memory BM25 indexes.
func releaseAllBM25Indexes() {
	bm25Mu.Lock()
	defer bm25Mu.Unlock()
	bm25Indexes = make(map[int64]*bm25Index)
}
