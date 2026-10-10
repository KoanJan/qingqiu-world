package tools

import (
	"fmt"
	"strings"
	"time"

	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/service/aos"
	"qingqiu-world-server/internal/service/llm"
)

// WriteNotesTool implements an append-only, structured notes system for persisting agent's working memory.
//
// Notes are stored as JSONL (notes.jsonl) in Workspace AOS metadata.
// The tool is a thin adapter: it parses LLM tool arguments, constructs a
// aos.NoteEntry, and delegates storage to aos.AppendNote.
//
// Rendering (markdown format for LLM consumption) is handled by this tool,
// not by the storage layer — different callers may want different formats.
type WriteNotesTool struct {
	personID      int64
	metaDir       string // Runtime-owned metadata paired with the selected Workspace.
	notesMaxChars int
	CycleDetector // Embedded: cycle detection on (args, result) pairs
}

// NewWriteNotesTool creates a WriteNotesTool bound to one Workspace's metadata.
func NewWriteNotesTool(personID int64, notesMaxChars int, metaDir string) *WriteNotesTool {
	return &WriteNotesTool{
		personID:      personID,
		metaDir:       metaDir,
		notesMaxChars: notesMaxChars,
	}
}

// Name returns the tool name.
func (w *WriteNotesTool) Name() ToolName { return ToolNameWriteNotes }

// Description returns a brief description of the tool.
func (w *WriteNotesTool) Description() string { return "Append structured entries to your notes" }

// Schema returns the LLM function definition for the tool.
func (w *WriteNotesTool) Schema() llm.FunctionDefinition {
	return llm.FunctionDefinition{
		Name: w.Name().String(),
		Description: "Append a structured entry to your NOTES. " +
			"This ADDS a new entry, it does NOT overwrite. " +
			"Use this to persist important information for future steps. " +
			"\n\n" +
			"IMPORTANT: Notes have a size limit. Only write IMPORTANT entries. " +
			"Skip trivial or obvious information. " +
			"Focus on key facts that future steps MUST know — " +
			"critical discoveries, important decisions, and essential state. " +
			"When in doubt, ask: would losing this information make later work in this Workspace harder? " +
			"If not, skip it." +
			"\n\n" +
			"Entry types:\n" +
			"- observation: Something you discovered or noticed\n" +
			"- decision: A choice you made and why\n" +
			"- finding: A key result or conclusion\n" +
			"- correction: A fix or change to a previous entry (use conflicts_with)\n" +
			"- progress: Current status and next steps\n" +
			"\n" +
			"When you repeatedly attempt the same action (e.g., same tool call, same approach), " +
			"record the attempt count and the fact that it keeps failing. " +
			"Example: \"[Attempt #12] npm run dev — failed again with same empty stdout. " +
			"This approach is not working.\"\n\n" +
			"Always include:\n" +
			"- Concise, self-contained content\n" +
			"- File references when relevant (paths relative to your working directory)\n" +
			"- Conflict markers when correcting earlier decisions" +
			"\n\n" +
			"CRITICAL — Identifier Preservation:\n" +
			"If you have introduced, discovered, or referenced any externally-assigned " +
			"identifiers that cannot be recovered from the filesystem (API response IDs, " +
			"UUIDs, tokens, hostnames, IP addresses, URLs that are not reachable from " +
			"your workspace), you MUST record them in your notes. File paths and git " +
			"commit hashes are recoverable through filesystem inspection and do NOT need " +
			"explicit recording. Record identifiers concisely under a references entry.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"entry_type": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"observation", "decision", "finding", "correction", "progress"},
					"description": "The type of this note entry",
				},
				"content": map[string]interface{}{
					"type":        "string",
					"description": "The main content of this note. Be CONCISE — only include information that is IMPORTANT to preserve for future steps.",
				},
				"references": map[string]interface{}{
					"type":        "array",
					"items":       map[string]interface{}{"type": "string"},
					"description": "Optional list of file paths this note relates to. Use paths relative to your working directory. Example: ['result.json', 'src/main.py']",
				},
				"conflicts_with": map[string]interface{}{
					"type":        "string",
					"description": "Optional timestamp or description of a previous entry that this entry corrects or supersedes. Example: '2024-05-20 14:30:00' or 'the decision about X'",
				},
			},
			"required": []string{"entry_type", "content"},
		},
	}
}

// Execute appends a structured entry to the agent's notes.
func (w *WriteNotesTool) Execute(args map[string]interface{}) (string, error) {
	entryTypeStr, _ := args["entry_type"].(string)
	content, _ := args["content"].(string)

	var references []string
	if refs, ok := args["references"].([]interface{}); ok {
		for _, r := range refs {
			if s, ok := r.(string); ok {
				references = append(references, s)
			}
		}
	}

	conflictsWith, _ := args["conflicts_with"].(string)

	if entryTypeStr == "" || content == "" {
		return "", fmt.Errorf("entry_type and content are required")
	}

	// Convert LLM-provided string type to NoteType at the API boundary.
	noteType, err := aos.ParseNoteType(entryTypeStr)
	if err != nil {
		return "", fmt.Errorf("invalid entry_type: %w", err)
	}

	entry := aos.NoteEntry{
		Timestamp:     time.Now().Format(time.RFC3339),
		Type:          noteType,
		Content:       content,
		References:    references,
		ConflictsWith: conflictsWith,
	}

	if err := aos.AppendNote(w.metaDir, entry); err != nil {
		return "", fmt.Errorf("failed to write note: %w", err)
	}

	refCount := len(references)
	conflictMarker := ""
	if conflictsWith != "" {
		conflictMarker = " (with conflict marker)"
	}

	return fmt.Sprintf("Successfully appended %s entry to your NOTES. Content: %d chars, References: %d%s",
		noteType.String(), len(content), refCount, conflictMarker), nil
}

// ReadNotes returns the full notes content rendered as markdown for LLM consumption.
// The FocusedLoop calls this to include notes in the system prompt.
func (w *WriteNotesTool) ReadNotes() string {
	entries := aos.ReadAllNotes(w.metaDir)
	if len(entries) == 0 {
		return ""
	}

	result := renderNoteEntries(entries)
	if len(result) <= w.notesMaxChars {
		return result
	}

	// Enforce max size: trim from the beginning, preserving recent entries
	trimmed := result[len(result)-w.notesMaxChars:]
	if idx := strings.Index(trimmed, "\n## ["); idx >= 0 {
		trimmed = trimmed[idx+1:]
	}
	return "[notes trimmed: older entries discarded]\n\n" + trimmed
}

// TrimNotes truncates the notes file if the rendered content exceeds the limit.
// Removes oldest entries until under the limit.
func (w *WriteNotesTool) TrimNotes() {
	entries := aos.ReadAllNotes(w.metaDir)
	if len(entries) == 0 {
		return
	}

	rendered := renderNoteEntries(entries)
	if len(rendered) <= w.notesMaxChars {
		return
	}

	// Remove oldest entries until under limit
	for len(entries) > 1 {
		entries = entries[1:]
		rendered = renderNoteEntries(entries)
		if len(rendered) <= w.notesMaxChars {
			break
		}
	}

	if err := aos.RewriteNotes(w.metaDir, entries); err != nil {
		applogger.Error("write_notes: failed to trim notes", "person_id", w.personID, "meta_dir", w.metaDir, "error", err)
		return
	}
}

// renderNoteEntry renders a single NoteEntry as a markdown section.
func renderNoteEntry(e aos.NoteEntry) string {
	ts := e.DisplayTimestamp()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## [%s] %s\n\n", ts, e.Type.String()))
	sb.WriteString(e.Content)
	sb.WriteString("\n")

	if len(e.References) > 0 {
		sb.WriteString("\n**References:**\n")
		for _, ref := range e.References {
			sb.WriteString(fmt.Sprintf("- `%s`\n", ref))
		}
	}

	if e.ConflictsWith != "" {
		sb.WriteString(fmt.Sprintf("\n⚠️ **Conflicts with:** %s\n", e.ConflictsWith))
		sb.WriteString("_See above for the previous entry that this corrects or supersedes._\n")
	}

	return sb.String()
}

// renderNoteEntries converts a slice of NoteEntry to markdown.
func renderNoteEntries(entries []aos.NoteEntry) string {
	parts := make([]string, len(entries))
	for i, e := range entries {
		parts[i] = renderNoteEntry(e)
	}
	return strings.Join(parts, "\n---\n\n")
}
