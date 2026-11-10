package runtime

import (
	"strings"
	"testing"
)

// TestDecisionPromptFocusEntryCriteria checks the stated Focus capabilities,
// KB entry requirement, and priority of routing existing work.
func TestDecisionPromptFocusEntryCriteria(t *testing.T) {
	focusStart := strings.Index(decidePromptTemplate, "Type 1 (start_focused_work)")
	focusEnd := strings.Index(decidePromptTemplate, "Type 2 (route_focused_work)")
	if focusStart < 0 || focusEnd <= focusStart {
		t.Fatal("decision prompt is missing the Focus action definition")
	}
	focusDefinition := decidePromptTemplate[focusStart:focusEnd]
	required := []string{
		"Workspace-backed execution context",
		"retrieve content from authorized sources",
		"whether it takes one step or several",
		"If retrieving KB content is needed",
		"Do NOT start a FocusedWork merely",
	}
	for _, text := range required {
		if !strings.Contains(focusDefinition, text) {
			t.Fatalf("Focus action definition is missing %q", text)
		}
	}
	rulesStart := strings.Index(decidePromptTemplate, "Decision rules (apply in order):")
	if rulesStart < 0 {
		t.Fatal("decision prompt is missing ordered decision rules")
	}
	rules := decidePromptTemplate[rulesStart:]
	routeIndex := strings.Index(rules, "First check Active works")
	startIndex := strings.Index(rules, "Start a FocusedWork (type=1)")
	if routeIndex < 0 || startIndex < 0 || routeIndex >= startIndex {
		t.Fatal("active Focus routing must be considered before creating new focused work")
	}
	for _, text := range []string{"Do not create a competing FocusedWork", "KB content must be retrieved", "not automatic reasons by themselves"} {
		if !strings.Contains(rules, text) {
			t.Fatalf("decision rules are missing Focus criterion %q", text)
		}
	}
}
