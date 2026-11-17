package dev

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestReleasePromotionExecutesTaggedSource(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(source, "server", "cmd"), 0700); err != nil {
		t.Fatal(err)
	}
	repo, err := git.PlainInit(source, false)
	if err != nil {
		t.Fatal(err)
	}
	module := []byte("module example.invalid/release\n\ngo 1.26.1\n")
	program := []byte("package main\nimport (\"os\"; \"path/filepath\")\nfunc main() { if os.Getenv(\"RELEASE_MIGRATION_ONLY\") != \"1\" { panic(\"wrong startup mode\") }; if err := os.WriteFile(filepath.Join(os.Getenv(\"DATA_ROOT\"), \"tagged-release.txt\"), []byte(\"released source ran\"), 0600); err != nil { panic(err) } }\n")
	if err := os.WriteFile(filepath.Join(source, "server", "go.mod"), module, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "server", "cmd", "main.go"), program, 0600); err != nil {
		t.Fatal(err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"server/go.mod", "server/cmd/main.go"} {
		if _, err := worktree.Add(path); err != nil {
			t.Fatal(err)
		}
	}
	hash, err := worktree.Commit("release", &git.CommitOptions{Author: &object.Signature{
		Name: "Test", Email: "test@example.invalid", When: time.Now(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	commit, err := repo.CommitObject(hash)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := Open(source)
	if err != nil {
		t.Fatal(err)
	}
	m := &manager{active: filepath.Join(root, "active"), store: filepath.Join(root, "store"), repo: reader}
	data := filepath.Join(root, "staged-data")
	if err := os.MkdirAll(data, 0700); err != nil {
		t.Fatal(err)
	}
	if err := m.runTaggedRelease(data, commit); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(data, "tagged-release.txt"))
	if err != nil || string(content) != "released source ran" {
		t.Fatalf("tagged release output = %q, error = %v", content, err)
	}
}
