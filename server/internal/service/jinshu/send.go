package jinshu

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"qingqiu-world-server/internal/dops"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/eventqueue"
	"qingqiu-world-server/internal/service/memory"
	servicetools "qingqiu-world-server/internal/service/tools"

	applogger "qingqiu-world-server/internal/logger"
)

// SendParams holds the inputs for delivering a jinshu to another person.
type SendParams struct {
	FromPersonID int64
	ToPersonID   int64
	Topic        string
	Description  string
	// Files maps the destination relative path to the source absolute path.
	// Relative paths must be non-empty, non-absolute, and unique.
	Files map[string]string
}

// Send creates a jinshu record, copies each source file into both the sender's
// sent/{id}/ directory and the recipient's received/{id}/ directory, records a
// memory event, and notifies the recipient if it is an AI agent.
//
// This is the single delivery path shared by the task loop tool, the
// private-space tool, and the user-facing HTTP API. Callers only differ in how
// they build the Files map (output dir, private-space workdir, or uploads).
func Send(p SendParams) (*model.Jinshu, error) {
	if p.FromPersonID == p.ToPersonID {
		return nil, fmt.Errorf("cannot send jinshu to yourself")
	}
	if p.Topic == "" {
		return nil, fmt.Errorf("topic must not be empty")
	}
	if len(p.Files) == 0 {
		return nil, fmt.Errorf("no files to send")
	}

	// Validate files up front so a bad path cannot leave a half-written record.
	relPaths := make([]string, 0, len(p.Files))
	for relPath, src := range p.Files {
		if relPath == "" || filepath.IsAbs(relPath) || relPath == ".." || strings.HasPrefix(relPath, "../") {
			return nil, fmt.Errorf("invalid relative path %q", relPath)
		}
		if _, err := os.Stat(src); err != nil {
			return nil, fmt.Errorf("source for %q: %w", relPath, err)
		}
		relPaths = append(relPaths, relPath)
	}
	sort.Strings(relPaths)

	// Create the record first so its auto-increment id names both target dirs.
	record, err := dops.CreateJinshu(p.FromPersonID, p.ToPersonID, p.Topic, p.Description)
	if err != nil {
		return nil, err
	}

	id := strconv.FormatInt(record.ID, 10)
	targetDirs := []string{
		filepath.Join(SentDir(p.FromPersonID), id),
		filepath.Join(ReceivedDir(p.ToPersonID), id),
	}
	for _, dir := range targetDirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("create jinshu directory: %w", err)
		}
	}

	for _, relPath := range relPaths {
		src := p.Files[relPath]
		for _, dir := range targetDirs {
			dst := filepath.Join(dir, relPath)
			if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
				return nil, fmt.Errorf("create target directory: %w", err)
			}
			if err := copyPath(src, dst); err != nil {
				return nil, fmt.Errorf("copy %q: %w", relPath, err)
			}
		}
	}

	// Notification is best-effort: the jinshu is already delivered.
	if err := notify(record, relPaths); err != nil {
		applogger.Error("jinshu: notify failed", "jinshu_id", record.ID, "error", err)
	}

	return record, nil
}

// notify records a memory event for the jinshu and dispatches a
// NewJinshuReceived runtime event to the recipient when it is an AI agent.
// files is the delivered relative path list, surfaced to the recipient so it
// knows what content was actually delivered.
func notify(record *model.Jinshu, files []string) error {
	content := record.Topic
	if record.Description != "" {
		content = record.Topic + "\n" + record.Description
	}
	eventID, err := memory.RecordJinshuEvent(record.ID, content)
	if err != nil {
		return fmt.Errorf("record memory event: %w", err)
	}

	person, err := dops.GetPerson(record.ToPersonID)
	if err != nil {
		return fmt.Errorf("load recipient %d: %w", record.ToPersonID, err)
	}
	if person.Type != model.PersonTypeAI {
		return nil // Human recipient has no runtime to notify.
	}

	ac, err := dops.GetAgentConfigByPersonID(record.ToPersonID)
	if err != nil {
		return fmt.Errorf("load recipient agent config %d: %w", record.ToPersonID, err)
	}

	fromName := ""
	if from, err := dops.GetPerson(record.FromPersonID); err != nil {
		return fmt.Errorf("load sender %d: %w", record.FromPersonID, err)
	} else {
		fromName = from.Name
	}

	eventqueue.SendEvent(ac.ID, &eventqueue.AgentEvent{
		Type:      eventqueue.EventTypeNewJinshuReceived,
		SessionID: 0, // Jinshu is person-level, not session-scoped.
		EventID:   eventID,
		Payload: &eventqueue.JinshuReceivedPayload{
			JinshuID:    record.ID,
			FromName:    fromName,
			Topic:       record.Topic,
			Description: record.Description,
			Files:       files,
		},
	})
	return nil
}

// ResolveWorkDirFiles resolves each path (file or directory) relative to
// workDir and returns relPath -> absPath for delivery via Send, plus the
// ordered relative paths. Each path must resolve within workDir and exist.
// This is shared by the private-space send_jinshu tool and the SendJinshu
// action so path resolution stays a single implementation.
func ResolveWorkDirFiles(workDir string, paths []string) (map[string]string, []string, error) {
	if len(paths) == 0 {
		return nil, nil, fmt.Errorf("paths must not be empty")
	}
	files := make(map[string]string, len(paths))
	relPaths := make([]string, 0, len(paths))
	for _, p := range paths {
		resolved, err := servicetools.ResolvePath(p, workDir, workDir)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid path %q: %w", p, err)
		}
		if _, err := os.Stat(resolved); err != nil {
			if os.IsNotExist(err) {
				return nil, nil, fmt.Errorf("path %q does not exist in working directory", p)
			}
			return nil, nil, fmt.Errorf("stat path %q: %w", p, err)
		}
		relPath, err := filepath.Rel(workDir, resolved)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve relative path for %q: %w", p, err)
		}
		files[relPath] = resolved
		relPaths = append(relPaths, relPath)
	}
	return files, relPaths, nil
}

// copyPath copies a file or directory tree from src to dst.
// If src is a directory, its contents are copied recursively.
func copyPath(src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	if srcInfo.IsDir() {
		return copyDirectory(src, dst)
	}
	return copyFile(src, dst)
}

// copyDirectory recursively copies a directory tree from src to dst.
func copyDirectory(src, dst string) error {
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			if err := copyDirectory(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}
	return nil
}

// copyFile copies a single file from src to dst, preserving permissions.
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	srcInfo, err := srcFile.Stat()
	if err != nil {
		return err
	}

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, srcInfo.Mode())
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	return err
}
