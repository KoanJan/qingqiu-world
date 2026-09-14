// Package workspace manages Agent Owned Space paths and their runtime metadata.
//
// Agent resources and runtime metadata are physically separated:
//
//	{DATA_ROOT}/aos/{person_id}/work/{session_id}/
//	{DATA_ROOT}/aos/{person_id}/private/
//	{DATA_ROOT}/aosmeta/{person_id}/work/{session_id}/.meta/
//	{DATA_ROOT}/aosmeta/{person_id}/private/.meta/
package workspace

import (
	"encoding/json"
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
		applogger.Error("workspace: failed to resolve absolute path", "path", path, "error", err)
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

// GetWorkspacePath returns the AOS work directory for a person/session pair.
func GetWorkspacePath(personID, sessionID int64) string {
	return filepath.Join(GetAgentOwnedSpacePath(personID), "work", strconv.FormatInt(sessionID, 10))
}

// GetMetaDir returns the system-managed metadata directory for one session.
func GetMetaDir(personID, sessionID int64) string {
	return filepath.Join(GetAgentMetaPath(personID), "work", strconv.FormatInt(sessionID, 10), ".meta")
}

// GetFocusHandoffPath returns the system-owned append-only handoff projection.
func GetFocusHandoffPath(personID, sessionID int64) string {
	if sessionID > 0 {
		return filepath.Join(GetMetaDir(personID, sessionID), "handoffs.jsonl")
	}
	return filepath.Join(GetPrivateMetaDir(personID), "handoffs.jsonl")
}

// GetOutputDir returns the default working directory inside a session AOS path.
func GetOutputDir(personID, sessionID int64) string {
	return filepath.Join(GetWorkspacePath(personID, sessionID), "output")
}

// ResolveAOSLocator resolves a user-facing resource locator into AOS. Explicit
// locators begin with work/ or private/; legacy relative paths remain relative
// to the current session output directory for compatibility.
func ResolveAOSLocator(personID, sessionID int64, locator string) (string, string, error) {
	return ResolveAOSLocatorFromDefault(personID, GetOutputDir(personID, sessionID), locator)
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
			// Preserve legacy output/private-relative delivery names.
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
		return "", fmt.Errorf("inspection scope must be root, work, private, or work/<session_id>")
	}
	sessionID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || sessionID <= 0 {
		return "", fmt.Errorf("inspection work scope requires a positive session ID")
	}
	return filepath.Join("work", strconv.FormatInt(sessionID, 10)), nil
}

// pathWithin reports whether path remains beneath root after lexical cleaning.
func pathWithin(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		applogger.Error("workspace: failed to compare paths", "path", path, "root", root, "error", err)
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// InitWorkspace creates the AOS/AOSMeta pair for a session.
func InitWorkspace(personID, sessionID int64) string {
	ws := GetWorkspacePath(personID, sessionID)
	metaDir := GetMetaDir(personID, sessionID)
	for _, dir := range []string{ws, metaDir, GetOutputDir(personID, sessionID)} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			applogger.Error("workspace: failed to initialize directory", "person_id", personID, "session_id", sessionID, "path", dir, "error", err)
		}
	}
	return ws
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

// AppendFocusHandoff writes an immutable metadata projection after the
// database record receives its ID. It is not an agent-editable resource.
func AppendFocusHandoff(record *model.FocusHandoff) error {
	if record == nil || record.PersonID <= 0 {
		return fmt.Errorf("focus handoff requires a valid person")
	}
	if record.SessionID > 0 {
		InitWorkspace(record.PersonID, record.SessionID)
	} else {
		if _, _, _, err := InitPrivateSpace(record.PersonID); err != nil {
			return err
		}
	}
	path := GetFocusHandoffPath(record.PersonID, record.SessionID)
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal focus handoff: %w", err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open focus handoff projection: %w", err)
	}
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		if closeErr := file.Close(); closeErr != nil {
			applogger.Error("workspace: failed to close incomplete handoff projection", "path", path, "error", closeErr)
		}
		return fmt.Errorf("append focus handoff projection: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close focus handoff projection: %w", err)
	}
	return nil
}

// RemoveWorkspace removes only the paired AOS and AOSMeta paths of one session.
func RemoveWorkspace(personID, sessionID int64) {
	removePath(GetWorkspacePath(personID, sessionID), "workspace resource cleanup", personID, sessionID)
	removePath(filepath.Join(GetAgentMetaPath(personID), "work", strconv.FormatInt(sessionID, 10)), "workspace metadata cleanup", personID, sessionID)
}

// RemoveAgentOwnedSpace removes both roots owned by one deleted agent.
func RemoveAgentOwnedSpace(personID int64) {
	removePath(GetAgentOwnedSpacePath(personID), "agent AOS cleanup", personID, 0)
	removePath(GetAgentMetaPath(personID), "agent AOS metadata cleanup", personID, 0)
}

// removePath removes one explicitly computed AOS path and records the outcome.
func removePath(path, operation string, personID, sessionID int64) {
	if err := os.RemoveAll(path); err != nil {
		applogger.Error("workspace: cleanup failed", "operation", operation, "person_id", personID, "session_id", sessionID, "path", path, "error", err)
		return
	}
	applogger.Info("workspace: cleanup completed", "operation", operation, "person_id", personID, "session_id", sessionID, "path", path)
}
