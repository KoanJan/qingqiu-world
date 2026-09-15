package kb

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// phase1BenchmarkCase is an offline, versioned acceptance fixture. Expected
// outcomes are checked by deterministic tests, never by an LLM judge.
type phase1BenchmarkCase struct {
	ID                  string  `json:"id"`
	Kind                string  `json:"kind"`
	Query               string  `json:"query"`
	ExpectedStatus      int     `json:"expected_status"`
	ExpectedEvidenceIDs []int64 `json:"expected_evidence_ids"`
}

func TestPhase1BenchmarkCorpus_IsWellFormed(t *testing.T) {
	path := filepath.Join("testdata", "phase1_benchmark.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read benchmark corpus: %v", err)
	}
	var cases []phase1BenchmarkCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatalf("decode benchmark corpus: %v", err)
	}
	if len(cases) < 3 {
		t.Fatalf("benchmark corpus is too small: %d", len(cases))
	}
	seen := make(map[string]struct{}, len(cases))
	for _, benchmark := range cases {
		if benchmark.ID == "" || benchmark.Query == "" || benchmark.Kind == "" {
			t.Fatalf("incomplete benchmark case: %#v", benchmark)
		}
		if _, exists := seen[benchmark.ID]; exists {
			t.Fatalf("duplicate benchmark ID: %s", benchmark.ID)
		}
		seen[benchmark.ID] = struct{}{}
		if benchmark.ExpectedStatus != ScanStatusComplete &&
			benchmark.ExpectedStatus != ScanStatusPartial &&
			benchmark.ExpectedStatus != ScanStatusInsufficientEvidence {
			t.Fatalf("unknown expected status in %s: %d", benchmark.ID, benchmark.ExpectedStatus)
		}
	}
}
