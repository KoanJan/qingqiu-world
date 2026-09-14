package runtime

import (
	"strings"
	"testing"
)

// TestDecisionPromptFocusEntryCriteria keeps Focus entry tied to sustained work,
// rather than allowing mere tool availability to force an inner loop.
func TestDecisionPromptFocusEntryCriteria(t *testing.T) {
	required := []string{
		"continuing course of work",
		"A FocusedWork is sustained, concentrated execution",
		"not automatic reasons by themselves",
		"Do not create a competing Focus",
	}
	for _, text := range required {
		if !strings.Contains(decidePromptTemplate, text) {
			t.Fatalf("decision prompt is missing Focus entry criterion %q", text)
		}
	}
	rulesStart := strings.Index(decidePromptTemplate, "Decision rules (apply in order):")
	if rulesStart < 0 {
		t.Fatal("decision prompt is missing ordered decision rules")
	}
	rules := decidePromptTemplate[rulesStart:]
	if strings.Index(rules, "First check Active works") > strings.Index(rules, "Start a FocusedWork (type=1)") {
		t.Fatal("active Focus routing must be considered before creating new focused work")
	}
}
