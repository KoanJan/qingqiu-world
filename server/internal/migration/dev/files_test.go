package dev

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyTreeRejectsOverlappingRoots(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{source, filepath.Join(source, "child"), root} {
		if err := copyTree(source, target); err == nil {
			t.Fatalf("copyTree accepted overlapping destination %s", target)
		}
	}
}
