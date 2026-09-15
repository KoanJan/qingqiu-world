package tools

import "testing"

func TestNormalizeOutputRelativePath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "report.md", want: "report.md"},
		{input: "output/report.md", want: "report.md"},
		{input: "./output/report.md", want: "report.md"},
		{input: "output/", want: "."},
	}
	for _, test := range tests {
		if got := normalizeOutputRelativePath(test.input); got != test.want {
			t.Errorf("normalizeOutputRelativePath(%q): got %q, want %q", test.input, got, test.want)
		}
	}
}
