// Package aos owns Agent Owned Space paths and directory lifecycle.
//
// Agent resources and runtime metadata are physically separated:
//
//	{DATA_ROOT}/aos/{person_id}/work/{directory_id}/
//	{DATA_ROOT}/aosmeta/{person_id}/work/{directory_id}/.meta/
//	{DATA_ROOT}/aos/{person_id}/private/
//	{DATA_ROOT}/aosmeta/{person_id}/private/.meta/
package aos

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/model"

	applogger "qingqiu-world-server/internal/logger"
)

// absolutePath resolves a configured filesystem path while preserving a usable fallback.
func absolutePath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	} else {
		applogger.Error("aos: failed to resolve absolute path", "path", path, "error", err)
	}
	return path
}

// GetAOSRoot returns the absolute Agent Owned Space root.
func GetAOSRoot() string {
	return absolutePath(config.Get().GetAOSRoot())
}

// GetAOSMetaRoot returns the absolute runtime metadata root for Agent Owned Space.
func GetAOSMetaRoot() string {
	return absolutePath(config.Get().GetAOSMetaRoot())
}

// GetAgentOwnedSpacePath returns the resource root owned by one agent.
func GetAgentOwnedSpacePath(personID int64) string {
	return filepath.Join(GetAOSRoot(), strconv.FormatInt(personID, 10))
}

// GetAgentMetaPath returns the runtime metadata root corresponding to one agent.
func GetAgentMetaPath(personID int64) string {
	return filepath.Join(GetAOSMetaRoot(), strconv.FormatInt(personID, 10))
}

// GetPrivateSpacePath returns the default private resource directory for an agent.
func GetPrivateSpacePath(personID int64) string {
	return filepath.Join(GetAgentOwnedSpacePath(personID), "private")
}

// GetPrivateMetaDir returns the private-loop metadata directory for an agent.
func GetPrivateMetaDir(personID int64) string {
	return filepath.Join(GetAgentMetaPath(personID), "private", ".meta")
}

// GetPrivateLogPath returns the runtime-owned private activity log path.
func GetPrivateLogPath(personID int64) string {
	return filepath.Join(GetPrivateMetaDir(personID), "log.jsonl")
}

// ResolveRegisteredPath resolves a persisted Workspace path below its owner's
// AOS root. Persisted paths are still validated because they are a file access
// boundary; an absent historical directory is reported, never recreated.
func ResolveRegisteredPath(record model.Workspace) (string, error) {
	root := GetAgentOwnedSpacePath(record.PersonID)
	rel := filepath.Clean(record.RelativePath)
	if rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("workspace %d has an invalid relative path", record.ID)
	}
	path := filepath.Join(root, rel)
	if !pathWithin(path, root) {
		return "", fmt.Errorf("workspace %d escapes its owner root", record.ID)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("workspace %d directory unavailable: %w", record.ID, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("workspace %d path is not a real directory", record.ID)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("workspace %d owner root unavailable: %w", record.ID, err)
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil || !pathWithin(resolvedPath, resolvedRoot) {
		return "", fmt.Errorf("workspace %d resolves outside its owner root or is unavailable: %v", record.ID, err)
	}
	return path, nil
}

// GetWorkspaceMetaDir resolves the runtime-owned metadata paired with a
// registered Workspace. Historical work/<session_id> paths keep their original
// metadata, so reuse of that Workspace also preserves its existing notes.
func GetWorkspaceMetaDir(record model.Workspace) (string, error) {
	if _, err := ResolveRegisteredPath(record); err != nil {
		return "", err
	}
	return filepath.Join(GetAgentMetaPath(record.PersonID), filepath.Clean(record.RelativePath), ".meta"), nil
}

// AvailableWorkDirectoryID avoids reusing a historical Session-named directory
// when a new Workspace's database ID happens to have the same number. The
// metadata side is checked too, because it can survive a missing resource dir.
func AvailableWorkDirectoryID(personID, firstID int64) (int64, error) {
	if personID <= 0 || firstID <= 0 {
		return 0, fmt.Errorf("invalid owner or Workspace ID")
	}
	for id := firstID; id > 0; id++ {
		rel := filepath.Join("work", strconv.FormatInt(id, 10))
		occupied := false
		for _, root := range []string{GetAgentOwnedSpacePath(personID), GetAgentMetaPath(personID)} {
			_, err := os.Lstat(filepath.Join(root, rel))
			if err == nil {
				occupied = true
				break
			}
			if !os.IsNotExist(err) {
				return 0, fmt.Errorf("inspect Workspace directory %s: %w", rel, err)
			}
		}
		if !occupied {
			return id, nil
		}
	}
	return 0, fmt.Errorf("no available Workspace directory ID")
}

// PrepareWorkspaceDirectories creates the selected Workspace resource directory
// and paired metadata. A new Workspace claims its numbered directory exclusively;
// an existing Workspace must resolve to a real registered directory.
func PrepareWorkspaceDirectories(record model.Workspace, newWorkspace bool) (directory, metaDir string, err error) {
	createdMeta := false
	if newWorkspace {
		if err = validateNewWorkspacePath(record); err != nil {
			return "", "", err
		}
		directory = filepath.Join(GetAgentOwnedSpacePath(record.PersonID), record.RelativePath)
		if err = os.MkdirAll(filepath.Dir(directory), 0755); err != nil {
			return "", "", fmt.Errorf("create Workspace root: %w", err)
		}
		if err = os.Mkdir(directory, 0755); err != nil {
			return "", "", fmt.Errorf("claim Workspace directory: %w", err)
		}
		claimedDirectory := directory
		defer func() {
			if err == nil {
				return
			}
			if createdMeta {
				err = errors.Join(err, CleanupUncommittedWorkspace(record))
			} else {
				// Another writer may have claimed the metadata path between
				// allocation and Mkdir; remove only our resource directory.
				err = errors.Join(err, os.RemoveAll(claimedDirectory))
			}
		}()
	} else {
		if directory, err = ResolveRegisteredPath(record); err != nil {
			return "", "", err
		}
	}
	if metaDir, err = GetWorkspaceMetaDir(record); err != nil {
		return "", "", err
	}
	if newWorkspace {
		if err = os.MkdirAll(filepath.Dir(metaDir), 0755); err == nil {
			err = os.Mkdir(metaDir, 0755)
			if err == nil {
				createdMeta = true
			}
		}
	} else {
		err = os.MkdirAll(metaDir, 0755)
	}
	if err != nil {
		return "", "", fmt.Errorf("prepare Workspace metadata: %w", err)
	}
	return directory, metaDir, nil
}

// CleanupUncommittedWorkspace removes only a newly claimed numbered directory
// pair when the transaction that would register it did not commit.
func CleanupUncommittedWorkspace(record model.Workspace) error {
	if err := validateNewWorkspacePath(record); err != nil {
		return err
	}
	resourceDir := filepath.Join(GetAgentOwnedSpacePath(record.PersonID), record.RelativePath)
	metaDir := filepath.Join(GetAgentMetaPath(record.PersonID), record.RelativePath)
	return errors.Join(os.RemoveAll(metaDir), os.RemoveAll(resourceDir))
}

// validateNewWorkspacePath prevents cleanup or creation outside the numbered
// work/ branch, including historical directories with a different identity.
func validateNewWorkspacePath(record model.Workspace) error {
	if record.PersonID <= 0 || record.ID <= 0 || record.RelativePath != filepath.Join("work", strconv.FormatInt(record.ID, 10)) {
		return fmt.Errorf("Workspace %d has no valid new directory path", record.ID)
	}
	return nil
}

// ResolveAOSLocatorFromDefault resolves a locator using defaultDir for legacy
// bare paths. defaultDir must itself be located inside the person's AOS.
func ResolveAOSLocatorFromDefault(personID int64, defaultDir, locator string) (string, string, error) {
	locator = strings.TrimSpace(locator)
	if locator == "" || filepath.IsAbs(locator) {
		return "", "", fmt.Errorf("locator must be a non-empty relative AOS path")
	}
	clean := filepath.Clean(locator)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("locator escapes Agent Owned Space")
	}

	root := GetAgentOwnedSpacePath(personID)
	if !pathWithin(defaultDir, root) {
		return "", "", fmt.Errorf("default directory is outside Agent Owned Space")
	}
	path := ""
	if clean == "work" || clean == "private" {
		return "", "", fmt.Errorf("AOS root directories are not deliverable resources")
	}
	if strings.HasPrefix(clean, "work"+string(filepath.Separator)) || strings.HasPrefix(clean, "private"+string(filepath.Separator)) {
		path = filepath.Join(root, clean)
	} else {
		path = filepath.Join(defaultDir, clean)
	}
	if !pathWithin(path, root) {
		return "", "", fmt.Errorf("locator resolves outside Agent Owned Space")
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("invalid AOS locator")
	}
	return path, filepath.ToSlash(rel), nil
}

// ResolveAOSFiles resolves existing deliverable resources and rejects paths
// whose final symlink target escapes AOS.
func ResolveAOSFiles(personID int64, defaultDir string, locators []string) (map[string]string, []string, error) {
	if len(locators) == 0 {
		return nil, nil, fmt.Errorf("at least one AOS locator is required")
	}
	root := GetAgentOwnedSpacePath(personID)
	if resolvedRoot, err := filepath.EvalSymlinks(root); err == nil {
		root = resolvedRoot
	}
	files := make(map[string]string, len(locators))
	relPaths := make([]string, 0, len(locators))
	for _, locator := range locators {
		path, relPath, err := ResolveAOSLocatorFromDefault(personID, defaultDir, locator)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve %q: %w", locator, err)
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve symlink target for %q: %w", locator, err)
		}
		if !pathWithin(resolved, root) {
			return nil, nil, fmt.Errorf("locator %q resolves outside Agent Owned Space", locator)
		}
		if _, err := os.Stat(resolved); err != nil {
			return nil, nil, fmt.Errorf("stat %q: %w", locator, err)
		}
		deliveryPath := relPath
		cleanLocator := filepath.Clean(locator)
		if !strings.HasPrefix(cleanLocator, "work"+string(filepath.Separator)) && !strings.HasPrefix(cleanLocator, "private"+string(filepath.Separator)) {
			// Keep the caller's relative delivery name instead of exposing its
			// AOS location to the recipient.
			deliveryPath = filepath.ToSlash(cleanLocator)
		}
		if _, exists := files[deliveryPath]; exists {
			return nil, nil, fmt.Errorf("duplicate delivery path %q", deliveryPath)
		}
		files[deliveryPath] = resolved
		relPaths = append(relPaths, deliveryPath)
	}
	return files, relPaths, nil
}

// InspectionEntry is filesystem metadata exposed by the bounded outer-loop inspection action.
type InspectionEntry struct {
	Path     string
	Type     string
	Size     int64
	Modified string
}

// InspectOwnedSpace lists one AOS directory level as filesystem metadata without reading contents.
func InspectOwnedSpace(personID int64, scope, query string, limit int) ([]InspectionEntry, error) {
	if limit < 1 || limit > 50 {
		return nil, fmt.Errorf("inspection limit must be between 1 and 50")
	}
	root := GetAgentOwnedSpacePath(personID)
	normalizedScope, err := NormalizeInspectionScope(scope)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(root, normalizedScope)
	if !pathWithin(dir, root) {
		return nil, fmt.Errorf("inspection scope escapes Agent Owned Space")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	capacity := limit
	if len(entries) < capacity {
		capacity = len(entries)
	}
	result := make([]InspectionEntry, 0, capacity)
	query = strings.ToLower(strings.TrimSpace(query))
	for _, entry := range entries {
		if query != "" && !strings.Contains(strings.ToLower(entry.Name()), query) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("inspect %s: %w", entry.Name(), err)
		}
		kind := "file"
		if info.IsDir() {
			kind = "directory"
		}
		rel, err := filepath.Rel(root, filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("resolve inspection entry %s: %w", entry.Name(), err)
		}
		result = append(result, InspectionEntry{Path: filepath.ToSlash(rel), Type: kind, Size: info.Size(), Modified: info.ModTime().Format(time.RFC3339)})
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

// NormalizeInspectionScope accepts only the deliberately small inspection surface.
// A FocusedLoop can use normal tools for deeper or content-level exploration.
func NormalizeInspectionScope(scope string) (string, error) {
	scope = strings.TrimSpace(scope)
	if scope == "" || scope == "root" {
		return ".", nil
	}
	if scope == "work" || scope == "private" {
		return scope, nil
	}
	parts := strings.Split(filepath.ToSlash(scope), "/")
	if len(parts) != 2 || parts[0] != "work" {
		return "", fmt.Errorf("inspection scope must be root, work, private, or work/<directory_id>")
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id <= 0 {
		return "", fmt.Errorf("inspection scope requires a positive ID")
	}
	return filepath.Join(parts[0], strconv.FormatInt(id, 10)), nil
}

// pathWithin reports whether path remains beneath root after lexical cleaning.
func pathWithin(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		applogger.Error("aos: failed to compare paths", "path", path, "root", root, "error", err)
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// InitAgentOwnedSpace creates the stable per-agent AOS skeleton.
//
// The private branch is initialized through InitPrivateSpace instead of a
// raw MkdirAll so private resources and private metadata stay in lockstep.
// Existing work/ directories are left in place. New Workspace directories
// use the same work/ branch and are created when a Work selects one.
func InitAgentOwnedSpace(personID int64) error {
	if personID <= 0 {
		return fmt.Errorf("initialize AOS: invalid person ID %d", personID)
	}
	if _, _, _, err := InitPrivateSpace(personID); err != nil {
		return err
	}
	for _, dir := range []string{
		filepath.Join(GetAgentOwnedSpacePath(personID), "work"),
		filepath.Join(GetAgentMetaPath(personID), "work"),
	} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("initialize AOS directory %s: %w", dir, err)
		}
	}
	return nil
}

// InitPrivateSpace creates the AOS/AOSMeta pair used by the private loop.
func InitPrivateSpace(personID int64) (rootDir, workDir, metaDir string, err error) {
	rootDir = GetAgentOwnedSpacePath(personID)
	workDir = GetPrivateSpacePath(personID)
	metaDir = GetPrivateMetaDir(personID)
	for _, dir := range []string{rootDir, workDir, metaDir} {
		if mkdirErr := os.MkdirAll(dir, 0755); mkdirErr != nil {
			return "", "", "", fmt.Errorf("initialize AOS directory %s: %w", dir, mkdirErr)
		}
	}
	return rootDir, workDir, metaDir, nil
}
