package privatespace

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"

	applogger "qingqiu-world-server/internal/logger"
)

// LogEntry is a single line in the agent's private activity log.
// Free format — no type system, no structural constraints.
// The agent writes whatever it considers worth remembering.
type LogEntry struct {
	Timestamp string `json:"timestamp"` // ISO 8601, written when the entry is created
	Content   string `json:"content"`   // Free-form text written by the agent
}

// AppendLog appends a log entry to the agent's log file.
// The log is stored as JSONL at {privateSpaceDir}/log.jsonl.
func AppendLog(personID int64, content string) error {
	path := GetLogPath(personID)
	entry := LogEntry{
		Timestamp: time.Now().Format(time.RFC3339),
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
		result += fmt.Sprintf("[%s] %s\n", e.Timestamp, e.Content)
	}
	return result
}
