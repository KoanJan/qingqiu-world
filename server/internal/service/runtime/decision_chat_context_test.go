package runtime

import (
	"strings"
	"testing"
	"time"

	comprehendTypes "qingqiu-world-server/internal/service/comprehend/types"
)

// TestDecideChatContextPreservesAntecedent verifies that Decide receives the
// original preceding speech once, even when the trigger is in the same window.
func TestDecideChatContextPreservesAntecedent(t *testing.T) {
	base := time.Date(2026, 10, 6, 7, 49, 0, 0, time.Local)
	comp := &comprehendTypes.ChatComprehension{
		ReadMessageIDs: []int64{3},
		Narrative:      "之前在讨论知识库报告。",
		RecentMessages: []comprehendTypes.ConversationMessage{
			{ID: 1, PersonName: "Patrick", Content: "他把表格发给你了吗？", CreatedAt: base},
			{ID: 2, PersonName: "Kiki", Content: "只发给了我，还没发给你。", CreatedAt: base.Add(time.Second), OwnAction: &comprehendTypes.MessageActionContext{
				Background: "Patrick让我问蛋挞", Reason: "需要取得蛋挞的答复", Guidance: "询问蛋挞是否有空",
			}},
			{ID: 3, PersonName: "Patrick", Content: "那你直接转发给我。", CreatedAt: base.Add(2 * time.Second)},
		},
	}
	context := buildChatComprehensionContext(comp)
	for _, text := range []string{"之前在讨论知识库报告。", "他把表格发给你了吗？", "只发给了我，还没发给你。"} {
		if !strings.Contains(context, text) {
			t.Fatalf("missing antecedent %q in %q", text, context)
		}
	}
	if strings.Contains(context, "那你直接转发给我。") {
		t.Fatalf("current event was repeated in session context: %q", context)
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
	if context := buildChatComprehensionContext(comp); strings.Contains(context, "conversational purpose") {
		t.Fatalf("other purpose was injected into Decide: %q", context)
	}
	comp.PersonState.Purpose = comprehendTypes.PersonPurposeRequest
	if context := buildChatComprehensionContext(comp); !strings.Contains(context, "requesting information or action") {
		t.Fatalf("request purpose was omitted from Decide: %q", context)
	}
}
