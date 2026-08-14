package jinshu

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"qingqiu-world-server/internal/dops"
	"qingqiu-world-server/internal/model"
)

// FileEntry describes one file or directory relative to a jinshu directory.
// The path is always slash-separated and relative to the jinshu's directory.
type FileEntry struct {
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

// ReceivedDirFor returns the recipient's received/{id} directory for a jinshu.
func ReceivedDirFor(personID, jinshuID int64) string {
	return filepath.Join(ReceivedDir(personID), strconv.FormatInt(jinshuID, 10))
}

// GetReceived loads a jinshu and verifies it was received by personID. This
// prevents an agent from reading or copying a jinshu that is not addressed to
// it. Sent jinshu copies are not exposed through these access helpers.
func GetReceived(personID, jinshuID int64) (*model.Jinshu, error) {
	record, err := dops.GetJinshu(jinshuID)
	if err != nil {
		return nil, err
	}
	if record.ToPersonID != personID {
		return nil, fmt.Errorf("jinshu %d is not received by person %d", jinshuID, personID)
	}
	return record, nil
}

// ListReceivedFiles returns a flat, sorted list of files under the recipient's
// received/{id} directory. Directories are included with is_dir=true and size 0.
func ListReceivedFiles(personID, jinshuID int64) ([]FileEntry, error) {
	dir := ReceivedDirFor(personID, jinshuID)
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("stat jinshu directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("jinshu %d directory is not a directory", jinshuID)
	}

	files := []FileEntry{}
	filepath.Walk(dir, func(path string, fi os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path == dir {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return nil
		}
		entry := FileEntry{
			Path:  filepath.ToSlash(rel),
			IsDir: fi.IsDir(),
		}
		if !fi.IsDir() {
			entry.Size = fi.Size()
		}
		files = append(files, entry)
		return nil
	})

	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// CopyReceivedTo copies the contents of the recipient's received/{id}
// directory into targetDir, returning the copied top-level relative paths.
// targetDir is created if missing. Existing files with the same name are
// overwritten.
func CopyReceivedTo(personID, jinshuID int64, targetDir string) ([]string, error) {
	srcDir := ReceivedDirFor(personID, jinshuID)
	info, err := os.Stat(srcDir)
	if err != nil {
		return nil, fmt.Errorf("stat jinshu directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("jinshu %d directory is not a directory", jinshuID)
	}

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return nil, fmt.Errorf("read jinshu directory: %w", err)
	}

	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return nil, fmt.Errorf("create target directory: %w", err)
	}

	copied := make([]string, 0, len(entries))
	for _, entry := range entries {
		relPath := entry.Name()
		if err := copyPath(filepath.Join(srcDir, relPath), filepath.Join(targetDir, relPath)); err != nil {
			return nil, fmt.Errorf("copy %q: %w", relPath, err)
		}
		copied = append(copied, relPath)
	}
	sort.Strings(copied)
	return copied, nil
}
