package tools

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// maxFileBytes is the maximum file size allowed for read_text_file (10MB).
const maxFileBytes = 10 * 1024 * 1024

// binarySniffBytes is the number of leading bytes scanned for null-byte detection.
const binarySniffBytes = 512

// binaryFileExtensions defines file extensions treated as binary.
var binaryFileExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".bmp": true,
	".ico": true, ".webp": true, ".tiff": true, ".tif": true, ".pdf": true,
	".zip": true, ".gz": true, ".tar": true, ".tgz": true, ".rar": true,
	".7z": true, ".bz2": true, ".exe": true, ".dll": true, ".so": true,
	".dylib": true, ".bin": true, ".dat": true, ".db": true, ".sqlite": true,
	".mp3": true, ".mp4": true, ".avi": true, ".mov": true, ".wav": true,
	".flv": true, ".wmv": true,
}

// ResolvePath resolves a file path to an absolute path within the given root.
//
// Relative paths are resolved against workDir. Absolute paths must be
// within rootDir. Access to .meta directory is blocked.
// Symlink targets are verified to be within rootDir.
func ResolvePath(filePath, rootDir, workDir string) (string, error) {
	var absPath string
	if filepath.IsAbs(filePath) {
		absPath = filepath.Clean(filePath)
	} else {
		absPath = filepath.Clean(filepath.Join(workDir, filePath))
	}

	// Block access to .meta directory
	metaDir := filepath.Join(rootDir, ".meta")
	if absPath == metaDir || strings.HasPrefix(absPath, metaDir+string(filepath.Separator)) {
		return "", fmt.Errorf("access to .meta directory is not allowed")
	}

	if !isPathWithin(absPath, rootDir) {
		return "", fmt.Errorf("path '%s' is outside the workspace", filePath)
	}

	if realPath, err := filepath.EvalSymlinks(absPath); err == nil {
		if !isPathWithin(realPath, rootDir) {
			return "", fmt.Errorf("symlink target '%s' is outside the workspace", filePath)
		}
		absPath = realPath
	}

	return absPath, nil
}

// isPathWithin checks if path is contained within base.
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

// IsBinaryFile detects whether a file is binary by extension and null-byte sniffing.
func IsBinaryFile(data []byte, path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if binaryFileExtensions[ext] {
		return true
	}
	scanLen := len(data)
	if scanLen > binarySniffBytes {
		scanLen = binarySniffBytes
	}
	return bytes.Contains(data[:scanLen], []byte{0})
}

// AtomicWrite writes content to targetPath atomically using temp file + rename.
func AtomicWrite(targetPath, content string) error {
	dir := filepath.Dir(targetPath)

	tmp, err := os.CreateTemp(dir, ".pb-tmp.*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmpPath, targetPath); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}

	return nil
}
