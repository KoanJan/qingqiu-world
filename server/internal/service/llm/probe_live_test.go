//go:build live_llm

package llm

import (
	"context"
	"os"
	"testing"
)

// This test calls the official API with synthetic data and never executes a tool.
func TestLiveDeepSeekCompatibility(t *testing.T) {
	key := os.Getenv("DEEPSEEK_API_KEY")
	if key == "" {
		t.Fatal("DEEPSEEK_API_KEY is required for the live DeepSeek contract test")
	}
	result := Probe(context.Background(), "https://api.deepseek.com", key, "deepseek-flash")
	if !result.RequiredTools.OK {
		t.Errorf("required tool choice failed: %s", result.RequiredTools.Error)
	}
	if !result.StructuredOutput.OK {
		t.Errorf("named tool choice failed: %s", result.StructuredOutput.Error)
	}
}
