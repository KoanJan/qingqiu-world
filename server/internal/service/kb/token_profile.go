package kb

import (
	"fmt"

	"qingqiu-world-server/internal/config"
)

// retrievalTokenProfile defines the deterministic quality budget shared by
// ContentNode construction and retrieval-unit generation.
type retrievalTokenProfile struct {
	MinTokens       int
	MaxTokens       int
	OverlapTokens   int
	EmbeddingMaxLen int
}

// newRetrievalTokenProfile validates the complete KB retrieval budget before
// document processing starts. Invalid configuration must fail processing rather
// than silently truncating embedding input or producing unbounded chunks.
func newRetrievalTokenProfile(settings *config.Settings) (retrievalTokenProfile, error) {
	profile := retrievalTokenProfile{
		MinTokens:       settings.KBRetrievalMinTokens,
		MaxTokens:       settings.KBRetrievalMaxTokens,
		OverlapTokens:   settings.KBChunkOverlapTokens,
		EmbeddingMaxLen: settings.KBEmbeddingHardMaxTokens,
	}
	if profile.MinTokens <= 0 {
		return retrievalTokenProfile{}, fmt.Errorf("KB_RETRIEVAL_MIN_TOKENS must be positive: %d", profile.MinTokens)
	}
	if profile.MaxTokens <= profile.MinTokens {
		return retrievalTokenProfile{}, fmt.Errorf("KB_RETRIEVAL_MAX_TOKENS must exceed KB_RETRIEVAL_MIN_TOKENS: max=%d min=%d", profile.MaxTokens, profile.MinTokens)
	}
	if profile.OverlapTokens < 0 || profile.OverlapTokens >= profile.MaxTokens {
		return retrievalTokenProfile{}, fmt.Errorf("KB_CHUNK_OVERLAP_TOKENS must be within [0, max): overlap=%d max=%d", profile.OverlapTokens, profile.MaxTokens)
	}
	if profile.EmbeddingMaxLen < profile.MaxTokens {
		return retrievalTokenProfile{}, fmt.Errorf("KB_EMBEDDING_HARD_MAX_TOKENS must be at least KB_RETRIEVAL_MAX_TOKENS: embedding_max=%d max=%d", profile.EmbeddingMaxLen, profile.MaxTokens)
	}
	return profile, nil
}
