package kb

import (
	"testing"

	"qingqiu-world-server/internal/config"
)

func TestNewRetrievalTokenProfile(t *testing.T) {
	valid := config.Settings{
		KBRetrievalMinTokens:     64,
		KBRetrievalMaxTokens:     512,
		KBChunkOverlapTokens:     64,
		KBEmbeddingHardMaxTokens: 8192,
	}
	if _, err := newRetrievalTokenProfile(&valid); err != nil {
		t.Fatalf("expected valid profile, got %v", err)
	}

	testCases := []struct {
		name   string
		mutate func(*config.Settings)
	}{
		{
			name: "non-positive minimum",
			mutate: func(settings *config.Settings) {
				settings.KBRetrievalMinTokens = 0
			},
		},
		{
			name: "maximum does not exceed minimum",
			mutate: func(settings *config.Settings) {
				settings.KBRetrievalMaxTokens = 64
			},
		},
		{
			name: "invalid overlap",
			mutate: func(settings *config.Settings) {
				settings.KBChunkOverlapTokens = 512
			},
		},
		{
			name: "embedding hard limit too small",
			mutate: func(settings *config.Settings) {
				settings.KBEmbeddingHardMaxTokens = 511
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			settings := valid
			testCase.mutate(&settings)
			if _, err := newRetrievalTokenProfile(&settings); err == nil {
				t.Fatal("expected profile validation error")
			}
		})
	}
}
