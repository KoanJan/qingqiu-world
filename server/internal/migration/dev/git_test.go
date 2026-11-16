package dev

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestLinkedWorktreeWithoutGitAtRuntime(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git is used only to construct the linked worktree fixture")
	}
	root := t.TempDir()
	main := filepath.Join(root, "main")
	linked := filepath.Join(root, "linked")
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.MkdirAll(main, 0700); err != nil {
		t.Fatal(err)
	}
	run(main, "init")
	run(main, "config", "user.name", "Test")
	run(main, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(main, "README.md"), []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	run(main, "add", "README.md")
	run(main, "commit", "-m", "release")
	run(main, "tag", "v0.1.21")
	run(main, "worktree", "add", linked)
	if info, err := os.Stat(filepath.Join(linked, ".git")); err != nil || info.IsDir() {
		t.Fatalf("linked worktree .git should be a file: %v", err)
	}
	repo, err := Open(linked)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Clean(); err != nil {
		worktree, worktreeErr := repo.repo.Worktree()
		if worktreeErr != nil {
			t.Fatal(worktreeErr)
		}
		status, statusErr := worktree.Status()
		t.Fatalf("%v: status=%v, status error=%v", err, status, statusErr)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	release, err := repo.ReleaseCommit("0.1.21")
	if err != nil || head.Hash != release.Hash {
		t.Fatalf("linked worktree HEAD and release differ: %v", err)
	}
}
