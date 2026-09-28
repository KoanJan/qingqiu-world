package chat

import (
	"context"
	"testing"

	"qingqiu-world-server/internal/model"
)

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

func TestAssembleContextHonorsClarificationBeforeWindowBranch(t *testing.T) {
	p := &pipeline{
		session:            &model.Session{ID: 42},
		messageCount:       1,
		windowSize:         50,
		needsClarification: true,
		clarification:      "Which date should I use?",
	}
	messages, content, earlyReturn := p.assembleContext(context.Background())
	if !earlyReturn || content != p.clarification || messages != nil {
		t.Fatalf("clarification was not returned before simple context: early=%v content=%q messages=%v", earlyReturn, content, messages)
	}
}
