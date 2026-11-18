package focusedworkcontext

import (
	"testing"

	"qingqiu-world-server/internal/service/llm"
)

func TestBuildMessagesHandlesDirectlyConstructedInvalidWindow(t *testing.T) {
	for _, window := range [][2]int{{100, 10}, {-5, -1}} {
		manager := NewContextManager("rules", window[0], window[1], "", "", "")
		for i := 0; i < 12; i++ {
			manager.AddIteration(llm.Message{Role: "assistant", Content: "step"}, nil)
		}
		messages := manager.BuildMessages()
		if len(messages) < 2 || messages[0].Role != "system" || messages[len(messages)-1].Role != "user" {
			t.Fatalf("invalid window %+v produced malformed context: %+v", window, messages)
		}
	}
}
