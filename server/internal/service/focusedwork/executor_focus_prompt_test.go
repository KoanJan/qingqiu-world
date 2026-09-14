package focusedwork

import (
	"strings"
	"testing"

	"qingqiu-world-server/internal/model"
)

// TestFocusedModePrompt explains the execution contract after the runtime has
// selected a FocusedLoop, including iterative work and durable handoff.
func TestFocusedModePrompt(t *testing.T) {
	prompt := buildSystemPrompt("background", "", nil, "", "/aos/1", "/aos/1/work/2", 3, model.FocusPhaseExecuting, "checkpoint")
	for _, text := range []string{
		"[Focused Mode]",
		"sustained course of work",
		"Use tools iteratively",
		"recorded the blocker, unresolved work, and a concrete next step",
	} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("FocusedLoop prompt is missing %q", text)
		}
	}
}
