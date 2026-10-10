package runtime

import (
	"fmt"
	"strings"
	"testing"
	"time"

	comprehendTypes "qingqiu-world-server/internal/service/comprehend/types"
)

// TestDecideTemplatesSeparateSituationSections catches misplaced format
// arguments that would silently enter either agent-facing prompt as %! output.
func TestDecideTemplatesSeparateSituationSections(t *testing.T) {
	for _, spec := range []struct {
		template string
		args     []any
	}{
		{decidePromptTemplate, []any{"Agent", "Character", "Bio", "Event text", "Origin", "Comprehension", "Subject", "Sessions", "Persons", "Resources", "Recall guidance", "Current time"}},
		{heartbeatPromptTemplate, []any{"Agent", "Character", "Bio", "Heartbeat", "Subject", "Sessions", "Persons", "Resources", "Recall guidance", "Current time"}},
	} {
		prompt := fmt.Sprintf(spec.template, spec.args...)
		if strings.Contains(prompt, "%!") {
			t.Fatalf("malformed Decide prompt placeholders: %s", prompt[len(prompt)-500:])
		}
		for _, section := range []string{"Your present state and recent experience:\nSubject", "Your surroundings:\nSessions\nPersons\nResources", "Memory tools:\nRecall guidance", "Time and energy:\nCurrent time"} {
			if !strings.Contains(prompt, section) {
				t.Fatalf("Decide prompt misplaced %q", section)
			}
		}
	}
}

// TestDecideChatContextPreservesAntecedent verifies that Decide receives the
// original preceding speech once, even when the trigger is in the same window.
func TestDecideChatContextPreservesAntecedent(t *testing.T) {
	base := time.Date(2026, 10, 6, 7, 49, 0, 0, time.Local)
	comp := &comprehendTypes.ChatComprehension{
		ReadMessageIDs: []int64{4},
		Narrative:      "之前在讨论知识库报告。",
		RecentMessages: []comprehendTypes.ConversationMessage{
			{ID: 1, PersonID: 1, PersonName: "Patrick", Content: "他把表格发给你了吗？", CreatedAt: base},
			{ID: 2, PersonID: 2, PersonName: "Kiki", Content: "只发给了我，还没发给你。", CreatedAt: base.Add(time.Second), OwnAction: &comprehendTypes.MessageActionContext{
				Background: "Patrick让我问蛋挞", Reason: "需要取得蛋挞的答复", Guidance: "询问蛋挞是否有空",
			}},
			{ID: 3, PersonID: 2, PersonName: "Kiki", Content: "嗯", CreatedAt: base.Add(2 * time.Second)},
			{ID: 4, PersonID: 1, PersonName: "Patrick", Content: "那你直接转发给我。", CreatedAt: base.Add(3 * time.Second)},
		},
	}
	context := buildChatComprehensionContext(comp, 2)
	for _, text := range []string{"之前在讨论知识库报告。", "他把表格发给你了吗？", "只发给了我，还没发给你。"} {
		if !strings.Contains(context, text) {
			t.Fatalf("missing antecedent %q in %q", text, context)
		}
	}
	if strings.Contains(context, "那你直接转发给我。") {
		t.Fatalf("current event was repeated in session context: %q", context)
	}
	if strings.Count(context, "— You said:") != 2 || strings.Contains(context, "— Kiki said:") {
		t.Fatalf("own speech was not labeled from its sender identity: %q", context)
	}
	for _, text := range []string{"Patrick让我问蛋挞", "需要取得蛋挞的答复", "询问蛋挞是否有空"} {
		if !strings.Contains(context, text) {
			t.Fatalf("own message intention missing %q in %q", text, context)
		}
	}
	if strings.Index(context, "只发给了我，还没发给你。") > strings.Index(context, "Patrick让我问蛋挞") {
		t.Fatalf("own action context appeared before its message: %q", context)
	}
}

// TestDecideChatContextOmitsOtherPurpose keeps an uncertain category from
// becoming a false conversational-intent claim in the decision prompt.
func TestDecideChatContextOmitsOtherPurpose(t *testing.T) {
	comp := &comprehendTypes.ChatComprehension{
		PersonState: &comprehendTypes.PersonState{Purpose: comprehendTypes.PersonPurposeOther},
	}
	if context := buildChatComprehensionContext(comp, 2); strings.Contains(context, "conversational purpose") {
		t.Fatalf("other purpose was injected into Decide: %q", context)
	}
	comp.PersonState.Purpose = comprehendTypes.PersonPurposeRequest
	if context := buildChatComprehensionContext(comp, 2); !strings.Contains(context, "requesting information or action") {
		t.Fatalf("request purpose was omitted from Decide: %q", context)
	}
}
