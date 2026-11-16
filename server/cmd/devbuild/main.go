// Command devbuild stamps the checked-out Git commit into a development server
// binary without requiring the Git executable.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"qingqiu-world-server/internal/migration/dev"
)

func main() {
	repoPath := flag.String("repo", "", "explicit source repository root")
	output := flag.String("output", "", "server binary output path")
	flag.Parse()
	if *repoPath == "" || *output == "" {
		fmt.Fprintln(os.Stderr, "devbuild requires -repo and -output")
		os.Exit(2)
	}
	repo, err := dev.Open(*repoPath)
	if err != nil {
		fail(err)
	}
	commit, err := repo.HeadHash()
	if err != nil {
		fail(err)
	}
	path, err := filepath.Abs(*output)
	if err != nil {
		fail(err)
	}
	serverDir := filepath.Join(repo.Root(), "server")
	if _, err := os.Stat(filepath.Join(serverDir, "go.mod")); err != nil {
		fail(fmt.Errorf("server module missing from repository root: %w", err))
	}
	command := exec.Command("go", "build", "-ldflags", "-X=main.buildCommit="+commit, "-o", path, "./cmd")
	command.Dir = serverDir
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
