package chat

import (
	"testing"
)

// TestNewChatResultRequiresExpressionInstruction rejects an empty speech cue.
func TestNewChatResultRequiresExpressionInstruction(t *testing.T) {
	if _, err := newChatResult(structuredChatResponse{
		Content:               "I understand.",
		ExpressionInstruction: " \n\t ",
	}); err == nil {
		t.Fatal("whitespace-only expression_instruction was accepted")
	}

	result, err := newChatResult(structuredChatResponse{
		Content:               "I understand.",
		ExpressionInstruction: "  Speak gently and reassuringly.  ",
	})
	if err != nil {
		t.Fatalf("valid structured response failed: %v", err)
	}
	if result.ExpressionInstruction != "Speak gently and reassuringly." {
		t.Fatalf("expression instruction = %q", result.ExpressionInstruction)
	}
}
