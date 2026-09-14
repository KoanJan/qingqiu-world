package privatespace

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"

	applogger "qingqiu-world-server/internal/logger"
)

// PrivateLogSource identifies the system component that wrote a private log entry.
type PrivateLogSource int

// Private log sources are system-assigned; agents cannot choose them through
// a file operation or tool argument.
const (
	PrivateLogSourceAgent PrivateLogSource = iota
	PrivateLogSourceRuntime
)

// PrivateLogType identifies the runtime-controlled kind of a private log entry.
type PrivateLogType int

// Private log types classify system-controlled records using persisted int enums.
const (
	PrivateLogTypeAgentNote PrivateLogType = iota
	PrivateLogTypeToolAccess
	PrivateLogTypeRuntimeAudit
)

// LogEntry is a system-controlled record in the private activity log.
// Content can be agent-authored, but Source and Type are assigned by code.
type LogEntry struct {
	Timestamp string           `json:"timestamp"` // ISO 8601, written when the entry is created
	Source    PrivateLogSource `json:"source"`    // PrivateLogSource*
	Type      PrivateLogType   `json:"type"`      // PrivateLogType*
	Content   string           `json:"content"`   // Free-form agent note or system message
}

// AppendLog appends a log entry to the agent's log file.
// The log is stored as JSONL at {privateSpaceDir}/log.jsonl.
func AppendLog(personID int64, content string) error {
	return appendLogRecord(personID, PrivateLogSourceAgent, PrivateLogTypeAgentNote, content)
}

// AppendRuntimeLog appends a runtime-owned private-space audit record.
func AppendRuntimeLog(personID int64, recordType PrivateLogType, content string) error {
	if recordType != PrivateLogTypeToolAccess && recordType != PrivateLogTypeRuntimeAudit {
		return fmt.Errorf("unsupported private runtime log type %d", recordType)
	}
	return appendLogRecord(personID, PrivateLogSourceRuntime, recordType, content)
}

// appendLogRecord persists one private audit entry with system-assigned enum values.
func appendLogRecord(personID int64, source PrivateLogSource, recordType PrivateLogType, content string) error {
	path := GetLogPath(personID)
	entry := LogEntry{
		Timestamp: time.Now().Format(time.RFC3339),
		Source:    source,
		Type:      recordType,
		Content:   content,
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal log entry: %w", err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	defer f.Close()

	if _, err := fmt.Fprintln(f, string(data)); err != nil {
		return fmt.Errorf("write log entry: %w", err)
	}
	return nil
}

// ReadRecentLog reads the last n entries from the agent's log.
// If the log file doesn't exist, returns an empty slice and nil error.
// Returns fewer than n entries if the log has fewer entries.
func ReadRecentLog(personID int64, n int) ([]LogEntry, error) {
	path := GetLogPath(personID)

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open log file: %w", err)
	}
	defer f.Close()

	var entries []LogEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var entry LogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			applogger.Error("failed to unmarshal log entry, skipping", "line", line, "error", err)
			continue
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read log file: %w", err)
	}

	if len(entries) > n {
		entries = entries[len(entries)-n:]
	}
	return entries, nil
}

// BuildRecentLogContext returns the last n log entries formatted
// as a string for inclusion in the LLM context. Returns empty string if
// no entries exist.
func BuildRecentLogContext(personID int64, n int) string {
	entries, err := ReadRecentLog(personID, n)
	if err != nil {
		applogger.Error("failed to read log for context", "person_id", personID, "error", err)
		return ""
	}
	if len(entries) == 0 {
		return ""
	}

	result := "Your recent private-space activity log:\n"
	for _, e := range entries {
		result += fmt.Sprintf("[%s] (%s) %s\n", e.Timestamp, privateLogTypeLabel(e.Type), e.Content)
	}
	return result
}

// privateLogTypeLabel renders a stored private log type for LLM context.
func privateLogTypeLabel(recordType PrivateLogType) string {
	switch recordType {
	case PrivateLogTypeToolAccess:
		return "tool"
	case PrivateLogTypeRuntimeAudit:
		return "runtime"
	default:
		return "note"
	}
}
