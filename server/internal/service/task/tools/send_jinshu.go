package tools

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/jinshu"
	"qingqiu-world-server/internal/service/llm"
	"qingqiu-world-server/internal/service/workspace"

	applogger "qingqiu-world-server/internal/logger"
	servicetools "qingqiu-world-server/internal/service/tools"
)

// sessionContextMessageLimit bounds the number of recent messages embedded in
// a jinshu description so long conversations don't bloat the record.
const sessionContextMessageLimit = 5

// SendJinshuTool sends files from the agent's output/ directory to another
// person as a jinshu (锦书). A single send copies the source files twice: one
// copy into the sender's sent/{id}/ directory and one into the recipient's
// received/{id}/ directory, both under the jinshu root keyed by person id. The
// jinshu record's auto-increment id doubles as the directory name, so the file
// location is derivable without storing paths.
type SendJinshuTool struct {
	personID      int64
	sessionID     int64
	CycleDetector // Embedded: cycle detection on (args, result) pairs
}

// NewSendJinshuTool creates a SendJinshuTool for the given person and session.
// sessionID is used only to locate the agent's session-scoped output/ source
// directory; the delivery target is person-level and independent of the session.
func NewSendJinshuTool(personID, sessionID int64) *SendJinshuTool {
	return &SendJinshuTool{
		personID:  personID,
		sessionID: sessionID,
	}
}

// Name returns the tool name.
func (s *SendJinshuTool) Name() ToolName { return ToolNameSendJinshu }

// Description returns a brief description of the tool.
func (s *SendJinshuTool) Description() string {
	return "Send files from your output directory to another person as a jinshu (锦书)"
}

// Schema returns the LLM function definition for the tool.
func (s *SendJinshuTool) Schema() llm.FunctionDefinition {
	return llm.FunctionDefinition{
		Name: s.Name().String(),
		Description: "Send files from your output/ directory to another person as a jinshu (锦书). " +
			"The files are copied to the recipient's jinshu/received/ directory, and a copy is kept " +
			"in your jinshu/sent/ directory. Use this to deliver completed work or send something " +
			"to another person.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"receiver": map[string]interface{}{
					"type":        "string",
					"description": "The name of the recipient person.",
				},
				"topic": map[string]interface{}{
					"type":        "string",
					"description": "A short subject/topic for this jinshu.",
				},
				"description": map[string]interface{}{
					"type":        "string",
					"description": "Optional note describing what is being sent and why.",
				},
				"paths": map[string]interface{}{
					"type":        "array",
					"description": "List of file or directory paths to send (relative to your output/ directory)",
					"items": map[string]interface{}{
						"type": "string",
					},
				},
			},
			"required": []string{"receiver", "topic", "paths"},
		},
	}
}

// resolveReceiver looks up the receiver name in the persons table and returns the person_id.
func resolveReceiver(name string) (personID int64, err error) {
	var person model.Person
	if err := database.DB.Where("name = ?", name).First(&person).Error; err != nil {
		return 0, fmt.Errorf("recipient '%s' not found", name)
	}
	return person.ID, nil
}

// Execute copies files from the agent's output directory into both the sender's
// sent/ and the recipient's received/ jinshu directories, and records a Jinshu
// row whose id names both target directories.
func (s *SendJinshuTool) Execute(args map[string]interface{}) (string, error) {
	receiverName, ok := args["receiver"].(string)
	if !ok || receiverName == "" {
		return "", fmt.Errorf("receiver must be a non-empty string")
	}

	targetPersonID, err := resolveReceiver(receiverName)
	if err != nil {
		return "", err
	}

	topic, ok := args["topic"].(string)
	if !ok || topic == "" {
		return "", fmt.Errorf("topic must be a non-empty string")
	}

	description := ""
	if d, ok := args["description"].(string); ok {
		description = d
	}

	paths, err := s.parsePaths(args["paths"])
	if err != nil {
		return "", err
	}

	// Validate each source path and resolve it relative to output/.
	outputDir := workspace.GetOutputDir(s.personID, s.sessionID)
	sessionRoot := workspace.GetWorkspacePath(s.personID, s.sessionID)
	var relPaths []string
	for _, p := range paths {
		resolved, err := servicetools.ResolvePath(p, sessionRoot, outputDir)
		if err != nil {
			return "", fmt.Errorf("invalid path '%s': %w", p, err)
		}
		if _, err := os.Stat(resolved); os.IsNotExist(err) {
			return "", fmt.Errorf("path '%s' does not exist in your output/ directory", p)
		}
		relPath, err := filepath.Rel(outputDir, resolved)
		if err != nil {
			return "", fmt.Errorf("failed to resolve relative path for '%s': %w", p, err)
		}
		relPaths = append(relPaths, relPath)
	}

	// Merge the session context into the description as plain text so the
	// record remains self-describing after the source session is deleted.
	description = mergeDescription(description, buildSessionContext(s.sessionID))

	// Write the record first to obtain the auto-increment id used as the
	// directory name under both target directories.
	record := model.Jinshu{
		FromPersonID: s.personID,
		ToPersonID:   targetPersonID,
		Topic:        topic,
		Description:  description,
	}
	if err := database.DB.Create(&record).Error; err != nil {
		return "", fmt.Errorf("failed to create jinshu record: %w", err)
	}

	// Copy source files twice: sender's sent/ and recipient's received/.
	id := strconv.FormatInt(record.ID, 10)
	targetDirs := []string{
		filepath.Join(jinshu.SentDir(s.personID), id),
		filepath.Join(jinshu.ReceivedDir(targetPersonID), id),
	}
	for _, dir := range targetDirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", fmt.Errorf("failed to create jinshu directory: %w", err)
		}
	}

	for _, relPath := range relPaths {
		src := filepath.Join(outputDir, relPath)
		for _, dir := range targetDirs {
			dst := filepath.Join(dir, relPath)
			if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
				return "", fmt.Errorf("failed to create target directory: %w", err)
			}
			if err := copyPath(src, dst); err != nil {
				return "", fmt.Errorf("failed to copy '%s': %w", relPath, err)
			}
		}
	}

	result := fmt.Sprintf("Sent %d file(s) to %s (jinshu #%d, topic: %s): %s",
		len(relPaths), receiverName, record.ID, topic, strings.Join(relPaths, ", "))

	return result, nil
}

// parsePaths extracts and validates the "paths" argument as a non-empty list of
// strings.
func (s *SendJinshuTool) parsePaths(raw interface{}) ([]string, error) {
	pathsRaw, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("paths must be an array of strings")
	}
	if len(pathsRaw) == 0 {
		return nil, fmt.Errorf("paths must not be empty")
	}

	paths := make([]string, 0, len(pathsRaw))
	for _, p := range pathsRaw {
		pathStr, ok := p.(string)
		if !ok {
			return nil, fmt.Errorf("each path must be a string")
		}
		paths = append(paths, pathStr)
	}
	return paths, nil
}

// mergeDescription appends the session context block to the sender-provided
// description, separated by a blank line when both are non-empty.
func mergeDescription(description, context string) string {
	description = strings.TrimSpace(description)
	if description == "" {
		return context
	}
	if context == "" {
		return description
	}
	return description + "\n\n" + context
}

// buildSessionContext returns a plain-text summary of the session in which the
// jinshu is being sent (session id, title, participants, recent messages). It is
// embedded into the description so the record stays readable after the session
// is gone. The session id is always recorded so agents can later trace the
// jinshu back to its originating session.
func buildSessionContext(sessionID int64) string {
	var b strings.Builder

	// Always record the session id; the rest is best-effort.
	fmt.Fprintf(&b, "[session]\nsession_id: %d\n", sessionID)

	var session model.Session
	if err := database.DB.First(&session, sessionID).Error; err != nil {
		applogger.Error("send_jinshu: failed to load session for context", "session_id", sessionID, "error", err)
	} else if session.Title != "" {
		fmt.Fprintf(&b, "title: %s\n", session.Title)
	}

	// Participant names.
	var participantIDs []int64
	if err := database.DB.Model(&model.ParticipantSession{}).
		Where("session_id = ?", sessionID).
		Pluck("participant_id", &participantIDs).Error; err != nil {
		applogger.Error("send_jinshu: failed to load session participants", "session_id", sessionID, "error", err)
	}
	if names := loadPersonNames(participantIDs); len(names) > 0 {
		nameList := make([]string, 0, len(names))
		for _, name := range names {
			nameList = append(nameList, name)
		}
		sort.Strings(nameList)
		fmt.Fprintf(&b, "participants: %s\n", strings.Join(nameList, ", "))
	}

	// Recent messages, newest-last for readability.
	var messages []model.Message
	if err := database.DB.Where("session_id = ?", sessionID).
		Order("id DESC").
		Limit(sessionContextMessageLimit).
		Find(&messages).Error; err != nil {
		applogger.Error("send_jinshu: failed to load recent messages", "session_id", sessionID, "error", err)
	} else if len(messages) > 0 {
		senderIDs := make([]int64, 0, len(messages))
		for _, m := range messages {
			senderIDs = append(senderIDs, m.PersonID)
		}
		senderNames := loadPersonNames(senderIDs)

		b.WriteString("recent_messages:\n")
		for i := len(messages) - 1; i >= 0; i-- {
			m := messages[i]
			name := senderNames[m.PersonID]
			if name == "" {
				name = fmt.Sprintf("person_%d", m.PersonID)
			}
			content := m.Content
			if len(content) > 200 {
				content = content[:200] + "..."
			}
			fmt.Fprintf(&b, "- %s: %s\n", name, content)
		}
	}

	return b.String()
}

// loadPersonNames returns a map of personID → name for the given IDs.
func loadPersonNames(ids []int64) map[int64]string {
	names := make(map[int64]string, len(ids))
	if len(ids) == 0 {
		return names
	}
	var persons []model.Person
	if err := database.DB.Where("id IN ?", ids).Find(&persons).Error; err != nil {
		applogger.Error("send_jinshu: failed to load person names", "error", err)
		return names
	}
	for _, p := range persons {
		names[p.ID] = p.Name
	}
	return names
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
