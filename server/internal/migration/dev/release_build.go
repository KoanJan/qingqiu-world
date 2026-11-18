package dev

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

const releaseBuildTimeout = 5 * time.Minute

// runTaggedRelease builds and runs the actual released source in a staging
// directory. Later development code cannot silently change release migration
// semantics while promoting a frozen baseline.
func (m *manager) runTaggedRelease(data string, release *object.Commit) error {
	buildRoot, err := m.newStage("release-source-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(buildRoot)
	source := filepath.Join(buildRoot, "checkout")
	if err := writeCommitTree(release, source); err != nil {
		return fmt.Errorf("extract release source %s: %w", release.Hash, err)
	}
	serverDir := filepath.Join(source, "server")
	if _, err := os.Stat(filepath.Join(serverDir, "go.mod")); err != nil {
		return fmt.Errorf("released server source is unavailable: %w", err)
	}
	mainSource, err := os.ReadFile(filepath.Join(serverDir, "cmd", "main.go"))
	if err != nil {
		return fmt.Errorf("read released server entrypoint: %w", err)
	}
	if !strings.Contains(string(mainSource), "RELEASE_MIGRATION_ONLY") {
		return fmt.Errorf("release %s does not support isolated migration execution; provide a baseline already at that release", release.Hash)
	}
	binary := filepath.Join(buildRoot, "release-server")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), releaseBuildTimeout)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-o", binary, "./cmd")
	build.Dir = serverDir
	build.Env = buildEnvironment()
	output, err := build.CombinedOutput()
	if err != nil {
		return fmt.Errorf("build released server %s: %w: %s", release.Hash, err, limitedOutput(output))
	}
	upgrade := exec.CommandContext(ctx, binary)
	upgrade.Dir = serverDir
	upgrade.Env = m.releaseEnvironment(data)
	output, err = upgrade.CombinedOutput()
	if err != nil {
		return fmt.Errorf("run released migration %s: %w: %s", release.Hash, err, limitedOutput(output))
	}
	return nil
}

// writeCommitTree materializes one committed tree without creating a worktree
// or invoking the Git executable, so promotion stays a pure Go read.
func writeCommitTree(commit *object.Commit, root string) error {
	tree, err := commit.Tree()
	if err != nil {
		return err
	}
	files := tree.Files()
	defer files.Close()
	for {
		file, err := files.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(root, filepath.FromSlash(file.Name))
		if !inside(root, target) {
			return fmt.Errorf("release tree path escapes checkout: %s", file.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		reader, err := file.Reader()
		if err != nil {
			return fmt.Errorf("read %s: %w", file.Name, err)
		}
		if file.Mode == filemode.Symlink {
			link, readErr := io.ReadAll(reader)
			reader.Close()
			if readErr != nil {
				return readErr
			}
			if err := os.Symlink(string(link), target); err != nil {
				return err
			}
			continue
		}
		permissions := os.FileMode(0644)
		if file.Mode == filemode.Executable {
			permissions = 0755
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, permissions)
		if err != nil {
			reader.Close()
			return err
		}
		_, copyErr := io.Copy(out, reader)
		closeErr := out.Close()
		reader.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
}

// maxReleaseOutputBytes bounds the build output quoted in an error so a failed
// release build stays diagnosable without flooding the log.
const maxReleaseOutputBytes = 4000

// buildEnvironment isolates the tagged build from local Go settings. A GOWORK
// file or GOFLAGS from the developer environment could resolve packages from
// the live checkout instead of the extracted tag, which would promote a
// baseline with code that is not actually part of the release.
func buildEnvironment() []string {
	env := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			env[key] = value
		}
	}
	env["GOWORK"] = "off"
	env["GOFLAGS"] = ""
	result := make([]string, 0, len(env))
	for key, value := range env {
		result = append(result, key+"="+value)
	}
	return result
}

func limitedOutput(output []byte) string {
	if len(output) > maxReleaseOutputBytes {
		output = output[len(output)-maxReleaseOutputBytes:]
	}
	return strings.TrimSpace(string(output))
}

func (m *manager) releaseEnvironment(data string) []string {
	env := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			env[key] = value
		}
	}
	for _, key := range []string{"AOS_ROOT", "AOS_META_ROOT", "JINSHU_ROOT", "WORKSPACE_ROOT", "PRIVATE_SPACE_ROOT"} {
		path := env[key]
		if path == "" {
			continue
		}
		abs, err := absoluteRoot(path)
		if err != nil || !inside(m.active, abs) {
			continue
		}
		rel, _ := filepath.Rel(m.active, abs)
		env[key] = filepath.Join(data, rel)
	}
	env["DATA_ROOT"] = data
	env["LOG_DIR"] = filepath.Join(filepath.Dir(data), "release-logs")
	env["DEV_DATA_MODE"] = "0"
	env["RELEASE_MIGRATION_ONLY"] = "1"
	result := make([]string, 0, len(env))
	for key, value := range env {
		result = append(result, key+"="+value)
	}
	return result
}
