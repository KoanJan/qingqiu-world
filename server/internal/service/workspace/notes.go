package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	applogger "qingqiu-world-server/internal/logger"
)

// NoteType represents the category of a note entry.
// Per project rules, enums must be int — not string.
type NoteType int

const (
	// NoteTypeObservation records what was observed.
	NoteTypeObservation NoteType = 1
	// NoteTypeDecision records a decision made.
	NoteTypeDecision NoteType = 2
	// NoteTypeFinding records a finding or discovery.
	NoteTypeFinding NoteType = 3
	// NoteTypeCorrection records a correction to previous information.
	NoteTypeCorrection NoteType = 4
	// NoteTypeProgress records progress on a task.
	NoteTypeProgress NoteType = 5
)

// String returns the uppercase display name for the note type.
func (t NoteType) String() string {
	switch t {
	case NoteTypeObservation:
		return "OBSERVATION"
	case NoteTypeDecision:
		return "DECISION"
	case NoteTypeFinding:
		return "FINDING"
	case NoteTypeCorrection:
		return "CORRECTION"
	case NoteTypeProgress:
		return "PROGRESS"
	default:
		return "UNKNOWN"
	}
}

// ParseNoteType converts a string type name (e.g., "progress") to NoteType.
// Used at the LLM tool boundary where types are provided as strings.
func ParseNoteType(s string) (NoteType, error) {
	switch strings.ToLower(s) {
	case "observation":
		return NoteTypeObservation, nil
	case "decision":
		return NoteTypeDecision, nil
	case "finding":
		return NoteTypeFinding, nil
	case "correction":
		return NoteTypeCorrection, nil
	case "progress":
		return NoteTypeProgress, nil
	default:
		return 0, fmt.Errorf("unknown note type: %s", s)
	}
}

// NoteEntry represents a single structured note entry stored as one JSONL line.
//
// Notes are stored in .meta/notes.jsonl within the session workspace.
// Each entry is a self-contained JSON object, append-only.
type NoteEntry struct {
	Timestamp     string   `json:"ts"`
	Type          NoteType `json:"type"`
	Content       string   `json:"content"`
	References    []string `json:"refs,omitempty"`
	ConflictsWith string   `json:"conflicts_with,omitempty"`
}

// DisplayTimestamp parses the RFC3339 timestamp and formats it as
// "2006-01-02 15:04:05" for human-readable display. If parsing fails,
// the raw Timestamp string is returned and the error is logged —
// per the no-silent-handling rule, malformed timestamps are anomalies
// that must be visible, not silently swallowed.
func (e NoteEntry) DisplayTimestamp() string {
	t, err := time.Parse(time.RFC3339, e.Timestamp)
	if err != nil {
		applogger.Warn("NoteEntry: timestamp not in RFC3339 format, using raw value",
			"timestamp", e.Timestamp,
			"error", err,
		)
		return e.Timestamp
	}
	return t.Format("2006-01-02 15:04:05")
}

// AppendNote writes a new note entry as a single JSONL line to the session's
// notes.jsonl. The file is created if it doesn't exist.
func AppendNote(personID, sessionID int64, entry NoteEntry) error {
	if entry.Timestamp == "" {
		entry.Timestamp = time.Now().Format(time.RFC3339)
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal note entry: %w", err)
	}
	data = append(data, '\n')

	f, err := os.OpenFile(notesPath(personID, sessionID), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return fmt.Errorf("open notes file: %w", err)
	}
	defer f.Close()

	_, err = f.Write(data)
	return err
}

// ReadAllNotes reads all note entries from the session's notes.jsonl.
// Returns nil if the file doesn't exist (work just started); other read
// errors are logged — silent skipping violates the no-silent-handling rule.
func ReadAllNotes(personID, sessionID int64) []NoteEntry {
	data, err := os.ReadFile(notesPath(personID, sessionID))
	if err != nil {
		// File-not-exist is legitimate: work just started, no notes yet.
		if !os.IsNotExist(err) {
			applogger.Error("ReadAllNotes: failed to read notes file",
				"person_id", personID,
				"session_id", sessionID,
				"error", err,
			)
		}
		return nil
	}

	var entries []NoteEntry
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry NoteEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			applogger.Warn("ReadAllNotes: skipping malformed JSONL line",
				"person_id", personID,
				"session_id", sessionID,
				"line", line,
				"error", err,
			)
			continue
		}
		entries = append(entries, entry)
	}
	return entries
}

// ReadLastNote returns the most recent note entry, or nil if no notes exist.
func ReadLastNote(personID, sessionID int64) *NoteEntry {
	entries := ReadAllNotes(personID, sessionID)
	if len(entries) == 0 {
		return nil
	}
	return &entries[len(entries)-1]
}

// RewriteNotes replaces the entire notes file with the given entries.
// Used by callers that need to trim old entries — the caller decides
// which entries to keep based on their own size/format requirements.
func RewriteNotes(personID, sessionID int64, entries []NoteEntry) error {
	f, err := os.Create(notesPath(personID, sessionID))
	if err != nil {
		return fmt.Errorf("create notes file: %w", err)
	}
	defer f.Close()

	for _, e := range entries {
		data, err := json.Marshal(e)
		if err != nil {
			// Marshal failure on NoteEntry is effectively impossible with
			// its current field types, but if it happens we log and skip
			// the entry rather than silently dropping it.
			applogger.Error("RewriteNotes: failed to marshal note entry, skipping",
				"person_id", personID,
				"session_id", sessionID,
				"entry_ts", e.Timestamp,
				"entry_type", e.Type.String(),
				"error", err,
			)
			continue
		}
		if _, err := f.Write(data); err != nil {
			return fmt.Errorf("write note entry: %w", err)
		}
		if _, err := f.Write([]byte("\n")); err != nil {
			return fmt.Errorf("write newline: %w", err)
		}
	}
	return nil
}

// NotesFingerprint returns a SHA256 hash of the raw notes.jsonl file content.
// Used by the reflection pipeline to detect whether notes have changed
// since the last reflection. Returns empty string if the file doesn't exist.
func NotesFingerprint(personID, sessionID int64) (string, error) {
	data, err := os.ReadFile(notesPath(personID, sessionID))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return sha256Hex(string(data)), nil
}

// notesPath returns the full path to notes.jsonl for the given session.
func notesPath(personID, sessionID int64) string {
	return filepath.Join(GetMetaDir(personID, sessionID), "notes.jsonl")
}

// sha256Hex computes a SHA256 hex digest of the input string.
func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
