package runtime

import "testing"

// TestExtractHandoffSection keeps the runtime handoff projection tied to
// explicit final-output sections rather than guessing from file paths.
func TestExtractHandoffSection(t *testing.T) {
	content := "Confirmed Findings:\n- tests pass\nArtifacts:\n- jinshu #12\n- work/7/report.txt\nUnresolved:\n- none\nNext Step:\n- notify the requester"
	if got := extractHandoffSection(content, "artifacts", "artifact"); got != "jinshu #12\nwork/7/report.txt" {
		t.Fatalf("artifact section mismatch: %q", got)
	}
	if got := extractHandoffSection(content, "confirmed findings", "confirmed"); got != "tests pass" {
		t.Fatalf("confirmed section mismatch: %q", got)
	}
}

func TestExtractHandoffSectionChineseHeading(t *testing.T) {
	content := "已确认：验证通过\n产物：jinshu #8\n下一步：等待反馈"
	if got := extractHandoffSection(content, "产物"); got != "jinshu #8" {
		t.Fatalf("Chinese artifact section mismatch: %q", got)
	}
}
