package config

import (
	"path/filepath"
	"testing"
)

func TestNumericConfigurationFallsBackByConstraintGroup(t *testing.T) {
	t.Setenv("SUMMARY_WINDOW_SIZE", "0")
	t.Setenv("SUMMARY_TOKEN_THRESHOLD", "bad")
	t.Setenv("MIN_ITERATION_WINDOW", "200")
	t.Setenv("MAX_ITERATION_WINDOW", "100")
	t.Setenv("KB_RETRIEVAL_MIN_TOKENS", "900")
	t.Setenv("KB_RETRIEVAL_MAX_TOKENS", "512")
	t.Setenv("KB_CHUNK_OVERLAP_TOKENS", "0")
	t.Setenv("COGNITIVE_MAX_REQUESTS", "2")
	t.Setenv("COGNITIVE_ROUTE_MIN_SIMILARITY_PERCENT", "101")
	t.Setenv("DAILY_MEMORY_REQUEST_INTERVAL_MS", "999")
	Init()
	s := Get()
	if s.SummaryWindowSize != 50 || s.SummaryTokenThreshold != 16000 || s.MinIterationWindow != 10 || s.MaxIterationWindow != 100 {
		t.Fatalf("invalid summary or iteration settings were retained: %+v", s)
	}
	if s.KBRetrievalMinTokens != 64 || s.KBRetrievalMaxTokens != 512 || s.KBChunkOverlapTokens != 64 || s.KBEmbeddingHardMaxTokens != 8192 {
		t.Fatalf("KB group did not fall back together: %+v", s)
	}
	if s.CognitiveMaxRequests != 12 || s.CognitiveRouteMinSimilarity != 35 || s.DailyMemoryRequestIntervalMS != 5000 {
		t.Fatalf("invalid cognitive settings were retained: %+v", s)
	}
}

func TestNumericBoundaryValuesAndFixedRoots(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_ROOT", root)
	t.Setenv("AOS_ROOT", filepath.Join(root, "elsewhere"))
	t.Setenv("AOS_META_ROOT", filepath.Join(root, "elsewhere-meta"))
	t.Setenv("JINSHU_ROOT", filepath.Join(root, "elsewhere-jinshu"))
	t.Setenv("MIN_ITERATION_WINDOW", "1")
	t.Setenv("MAX_ITERATION_WINDOW", "2")
	t.Setenv("KB_RETRIEVAL_MIN_TOKENS", "1")
	t.Setenv("KB_RETRIEVAL_MAX_TOKENS", "2")
	t.Setenv("KB_CHUNK_OVERLAP_TOKENS", "0")
	t.Setenv("KB_EMBEDDING_HARD_MAX_TOKENS", "2")
	t.Setenv("COGNITIVE_MAX_REQUESTS", "3")
	t.Setenv("COGNITIVE_ROUTE_MIN_SIMILARITY_PERCENT", "100")
	t.Setenv("DAILY_MEMORY_REQUEST_INTERVAL_MS", "1000")
	Init()
	s := Get()
	if s.MinIterationWindow != 1 || s.MaxIterationWindow != 2 || s.KBRetrievalMinTokens != 1 || s.KBRetrievalMaxTokens != 2 || s.KBChunkOverlapTokens != 0 || s.KBEmbeddingHardMaxTokens != 2 || s.CognitiveMaxRequests != 3 || s.CognitiveRouteMinSimilarity != 100 || s.DailyMemoryRequestIntervalMS != 1000 {
		t.Fatalf("valid numeric boundaries were changed: %+v", s)
	}
	if s.GetAOSRoot() != filepath.Join(root, "aos") || s.GetAOSMetaRoot() != filepath.Join(root, "aosmeta") || s.GetJinshuRoot() != filepath.Join(root, "jinshu") {
		t.Fatalf("legacy roots still override runtime data directories: %+v", s)
	}
}
