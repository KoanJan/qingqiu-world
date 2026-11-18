package scripts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qingqiu-world-server/internal/config"
)

func TestLegacyRootEquivalentPathNeedsNoCopy(t *testing.T) {
	root := t.TempDir()
	active := filepath.Join(root, "active")
	stage := filepath.Join(root, "stage")
	for _, dir := range []string{filepath.Join(active, "aos"), filepath.Join(stage, "aos")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("DATA_ROOT", active)
	t.Setenv("AOS_ROOT", filepath.Join(active, "aos"))
	config.Init()
	if err := copyLegacyRoot("AOS_ROOT", "aos", stage); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(stage, "aos"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("equivalent root copied data: %v, %v", entries, err)
	}
}

func TestLegacyRootConflictStopsWithoutOverwriting(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "legacy")
	stage := filepath.Join(root, "stage")
	for _, dir := range []string{legacy, filepath.Join(stage, "aos")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	for path, content := range map[string]string{
		filepath.Join(legacy, "record.txt"):       "legacy",
		filepath.Join(stage, "aos", "record.txt"): "new",
	} {
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("DATA_ROOT", filepath.Join(root, "active"))
	t.Setenv("AOS_ROOT", legacy)
	config.Init()
	if err := copyLegacyRoot("AOS_ROOT", "aos", stage); err == nil || !strings.Contains(err.Error(), "different content") {
		t.Fatalf("conflict error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(stage, "aos", "record.txt"))
	if err != nil || string(data) != "new" {
		t.Fatalf("conflict overwrote destination: %q, %v", data, err)
	}
}

func TestLegacyRootOverlappingDestinationStops(t *testing.T) {
	root := t.TempDir()
	active := filepath.Join(root, "active")
	stage := filepath.Join(root, "stage")
	for _, dir := range []string{active, stage} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("DATA_ROOT", active)
	t.Setenv("AOS_ROOT", active)
	config.Init()
	if err := copyLegacyRoot("AOS_ROOT", "aos", stage); err == nil || !strings.Contains(err.Error(), "overlaps target") {
		t.Fatalf("unsafe overlap error = %v", err)
	}
}
