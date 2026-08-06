// Package tools provides the tool abstractions and implementations for the task agent system.
//
// All tools must implement the Tool interface, which requires a unique name,
// a short description for the system prompt, a function definition schema,
// and an execute method. Tools are registered by name and provide their
// schema for LLM tool calling.
//
// Available tools:
//   - ReadTextFileTool: Read text file contents with line offset/limit
//   - WriteTextFileTool: Create, overwrite, or append to text files
//   - EditTextFileTool: Make precise text replacements in existing files
//   - BashTool: Execute shell commands within a workspace
//   - WriteNotesTool: Append structured entries to agent's notes
//   - WebSearchTool: Search the web for information (Tavily provider)
//   - ScanExperienceTool: Search private experiences by keyword (progressive disclosure step 1)
//   - RecallExperienceTool: Read the full content of a specific experience (progressive disclosure step 2)
package tools

import "qingqiu-world-server/internal/service/llm"

// ToolName is the type-safe identifier for a tool using int enum values.
// Each tool implementation returns its corresponding constant from Name().
type ToolName int

// ToolName constants define all known tool identifiers using int enum values.
const (
	ToolNameBash               ToolName = iota // bash
	ToolNameReadTextFile                       // read_text_file
	ToolNameWriteTextFile                      // write_text_file
	ToolNameEditTextFile                       // edit_text_file
	ToolNameWriteNotes                         // write_notes
	ToolNameWebSearch                          // web_search
	ToolNameDeliverTo                          // deliver_to
	ToolNameScanMyExperience                   // scan_my_experience
	ToolNameRecallMyExperience                 // recall_my_experience
	ToolNameSearchChatHistories                // search_chat_histories
)

// nameStrings maps ToolName values to their string representation for LLM function calling.
var nameStrings = map[ToolName]string{
	ToolNameBash:                "bash",
	ToolNameReadTextFile:        "read_text_file",
	ToolNameWriteTextFile:       "write_text_file",
	ToolNameEditTextFile:        "edit_text_file",
	ToolNameWriteNotes:          "write_notes",
	ToolNameWebSearch:           "web_search",
	ToolNameDeliverTo:           "deliver_to",
	ToolNameScanMyExperience:    "scan_my_experience",
	ToolNameRecallMyExperience:  "recall_my_experience",
	ToolNameSearchChatHistories: "search_chat_histories",
}

// String returns the string representation of the ToolName for use in LLM function definitions.
func (t ToolName) String() string {
	if s, ok := nameStrings[t]; ok {
		return s
	}
	return "unknown"
}

// FromString converts a string tool name to its ToolName enum value.
// Returns the zero value (ToolNameBash) if the name is not recognized.
func FromString(s string) ToolName {
	for k, v := range nameStrings {
		if v == s {
			return k
		}
	}
	return ToolNameBash
}

// Tool is the interface that all agent tools must implement.
// Each tool has a unique name, a short description for the system prompt's
// tool list, a function definition schema, and an execute method that
// performs the actual work.
type Tool interface {
	// Name returns the unique identifier for this tool.
	Name() ToolName
	// Description returns a short one-line summary used in the system prompt's
	// "Available tools" section. This is separate from Schema().Description
	// which is the detailed description passed to the LLM's function calling.
	Description() string
	// Schema returns the function definition schema for this tool.
	Schema() llm.FunctionDefinition
	// Execute runs the tool with the given arguments and returns the result as a string.
	Execute(args map[string]interface{}) (string, error)
	// CycleDetect checks for cyclical patterns after each call.
	// Each tool defines its own "sameness" semantics and manages its own
	// detection state. Returns a warning to append to the result, and
	// whether the tool should be blocked (triggering a forced checkpoint).
	CycleDetect(args map[string]interface{}, result string) CycleStatus
}
