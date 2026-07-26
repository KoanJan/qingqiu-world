package tools

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"qingqiu-world-server/internal/service/workspace"
)

// maxFileBytes is the maximum file size allowed for read_text_file (10MB).
const maxFileBytes = 10 * 1024 * 1024

// binaryFileExtensions defines file extensions that are treated as binary
// and should be rejected by read_text_file and edit_text_file.
var binaryFileExtensions = map[string]bool{
	".png":    true,
	".jpg":    true,
	".jpeg":   true,
	".gif":    true,
	".bmp":    true,
	".ico":    true,
	".webp":   true,
	".tiff":   true,
	".tif":    true,
	".pdf":    true,
	".zip":    true,
	".gz":     true,
	".tar":    true,
	".tgz":    true,
	".rar":    true,
	".7z":     true,
	".bz2":    true,
	".exe":    true,
	".dll":    true,
	".so":     true,
	".dylib":  true,
	".bin":    true,
	".dat":    true,
	".db":     true,
	".sqlite": true,
	".mp3":    true,
	".mp4":    true,
	".avi":    true,
	".mov":    true,
	".wav":    true,
	".flv":    true,
	".wmv":    true,
}

// resolvePath resolves a file path to an absolute path within the session workspace.
//
// Relative paths are resolved against the session's output/ directory.
// Absolute paths must be within the session workspace root.
// Access to .meta directory is blocked.
// Symlinks are resolved and their targets must be within the session workspace.
//
// Returns the resolved absolute path, or an error if the path violates security constraints.
func resolvePath(filePath string, personID, sessionID int64) (string, error) {
	sessionRoot := workspace.GetWorkspacePath(personID, sessionID)
	outputDir := workspace.GetOutputDir(personID, sessionID)

	var absPath string
	if filepath.IsAbs(filePath) {
		absPath = filepath.Clean(filePath)
	} else {
		absPath = filepath.Clean(filepath.Join(outputDir, filePath))
	}

	// Block access to .meta directory (the directory itself or anything under it)
	metaDir := filepath.Join(sessionRoot, ".meta")
	if absPath == metaDir || strings.HasPrefix(absPath, metaDir+string(filepath.Separator)) {
		return "", fmt.Errorf("access to .meta directory is not allowed")
	}

	// Check that the resolved path is within the session root
	if !isPathWithin(absPath, sessionRoot) {
		return "", fmt.Errorf("path '%s' is outside the session workspace", filePath)
	}

	// Resolve symlinks and verify the real path is also within session root
	if realPath, err := filepath.EvalSymlinks(absPath); err == nil {
		if !isPathWithin(realPath, sessionRoot) {
			return "", fmt.Errorf("symlink target '%s' is outside the session workspace", filePath)
		}
		absPath = realPath
	}

	return absPath, nil
}

// isPathWithin checks if a path is contained within the given base directory.
// Both paths should be cleaned absolute paths.
func isPathWithin(path, base string) bool {
	if path == base {
		return true
	}
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return false
	}
	return rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}

// isBinaryFile detects whether a file is binary by checking its extension
// and scanning the first 512 bytes for null bytes.
//
// Parameters:
//   - data: the file content (or at least the first 512 bytes)
//   - path: the file path (used for extension-based detection)
//
// binarySniffBytes is the number of leading bytes scanned for null bytes
// to detect binary file content.
const binarySniffBytes = 512

// Returns true if the file appears to be binary.
func isBinaryFile(data []byte, path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if binaryFileExtensions[ext] {
		return true
	}

	// Sniff the first binarySniffBytes bytes for null bytes
	scanLen := len(data)
	if scanLen > binarySniffBytes {
		scanLen = binarySniffBytes
	}
	return bytes.Contains(data[:scanLen], []byte{0})
}

// atomicWrite writes content to the target path atomically using a temp file + rename.
//
// The temp file is created in the same directory as the target to ensure
// the rename operation is atomic (same filesystem). If any step fails,
// the temp file is cleaned up and the original file remains untouched.
func atomicWrite(targetPath, content string) error {
	dir := filepath.Dir(targetPath)

	tmp, err := os.CreateTemp(dir, ".pb-tmp.*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // Clean up on failure; no-op after successful rename

	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return fmt.Errorf("failed to write to temp file: %w", err)
	}

	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("failed to sync temp file: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	if err := os.Rename(tmpPath, targetPath); err != nil {
		return fmt.Errorf("failed to rename temp file to target: %w", err)
	}

	return nil
}
