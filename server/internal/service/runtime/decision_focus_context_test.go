package runtime

import (
	"testing"

	"qingqiu-world-server/internal/model"
)

// TestFocusHandoffRelevance verifies that a current Focus hint ranks a related
// handoff above an unrelated one without asserting any causal relation.
func TestFocusHandoffRelevance(t *testing.T) {
	terms := focusContextTerms("继续修复登录接口")
	related := model.FocusHandoff{Orientation: "修复登录接口", Summary: "已定位认证中间件问题"}
	unrelated := model.FocusHandoff{Orientation: "整理首页配色", Summary: "完成视觉调整"}
	if focusHandoffRelevance(related, terms) <= focusHandoffRelevance(unrelated, terms) {
		t.Fatalf("expected related handoff to rank higher: related=%d unrelated=%d", focusHandoffRelevance(related, terms), focusHandoffRelevance(unrelated, terms))
	}
}

// TestFocusContextTermsIgnoresPunctuation verifies that lexical selection is
// deterministic for prompt punctuation and does not require a causal model.
func TestFocusContextTermsIgnoresPunctuation(t *testing.T) {
	terms := focusContextTerms("Check API, please!")
	if _, ok := terms["check"]; !ok {
		t.Fatalf("expected normalized token in %v", terms)
	}
	if _, ok := terms["api"]; !ok {
		t.Fatalf("expected normalized token in %v", terms)
	}
}
