// Package task implements the autonomous task execution system for world-interaction requests.
//
// This package provides the task execution pipeline that handles agent-based
// task execution when the chat system determines that a user request requires
// world interaction (e.g., file operations, web searches, code execution).
//
// The main entry point is Execute, which:
//  1. Initializes the session workspace structure
//  2. Builds the system prompt and tool list
//  3. Creates the context manager with iteration window
//  4. Runs the ReAct task loop to completion
//  5. Returns a TaskResult with success/failure status
//
// Design principles:
//   - Input: task requirement (structured, not raw user message)
//   - Output: final result (success result or failure with reason)
//   - Internal isolation: all process info is hidden from the outside
//   - No pollution of the chat system
package task

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/llm"
	taskcontext "qingqiu-world-server/internal/service/task/context"
	"qingqiu-world-server/internal/service/task/tools"
	"qingqiu-world-server/internal/service/workspace"

	applogger "qingqiu-world-server/internal/logger"
)

// TaskResult represents the outcome of a task execution.
// On success, Output contains the final content. On failure, Error contains the reason.
// Notes and Workspace are always populated for observability.
type TaskResult struct {
	Status      string `json:"status"`
	Output      string `json:"output,omitempty"`
	Error       string `json:"error,omitempty"`
	Notes       string `json:"notes,omitempty"`
	Workspace   string `json:"workspace,omitempty"`
	NotesLength int    `json:"notes_length,omitempty"`
}

// GuidanceDirective is a structured guidance message sent from the Runtime
// to the TaskLoop during execution. It carries both the executable directive
// (Guidance) and the cognitive context explaining why (Reason).
//
// This struct is passed through the guidance channel instead of a bare string,
// so the TaskLoop's LLM can understand the full context of a route or cancel
// decision — not just the "what" but also the "why".
type GuidanceDirective struct {
	Guidance string // What to do: the executable directive
	Reason   string // Why: user's original message, inferred intent, and Decide's reasoning
}

// RunTaskParams contains all parameters needed for the full task pipeline.
// After the cognitive order refactoring, Guidance from the Decide phase
// replaces the old Rewrite step — comprehension and decision have already
// produced a clear execution intent, so rewriting is unnecessary.
type RunTaskParams struct {
	LLMConfig  *model.LLMConfig
	SessionID  int64
	PersonID   int64 // Person ID of the executing agent
	WorkID     int64
	Guidance   string    // Execution intent from Decide phase (replaces Rewrite)
	Background string    // Full context from Decide phase: trigger event, participants, comprehension
	Metadata   *Metadata // System-generated traceability info from work creation
	Ctx        context.Context
	OnNotify   func(data string)        // Optional callback for frontend notifications
	GuidanceCh <-chan GuidanceDirective // Channel for receiving new guidance during execution
}

// RunTask executes the full task pipeline using Guidance as the task requirement.
//
// After the cognitive order refactoring, the Comprehend-Decide pipeline has
// already produced a clear execution intent (Guidance). This replaces the
// old Rewrite step — there is no need to rewrite the user message because
// all cognitive work (understanding intent, resolving references, determining
// what to do) was completed before this point.
//
// New guidance can arrive via GuidanceCh during execution. The TaskLoop
// observes the channel at each iteration boundary and injects new directives
// as environment events in the ReAct cycle.
func RunTask(params RunTaskParams) *TaskResult {
	// Notify frontend that agent is processing
	if params.OnNotify != nil {
		notifyData, _ := json.Marshal(map[string]string{
			"type":    "agent_processing",
			"message": "Agent is processing your request...",
		})
		params.OnNotify(string(notifyData))
	}

	applogger.Info("RunTask: starting with Guidance",
		"session_id", params.SessionID,
		"guidance", params.Guidance,
	)

	// Load search config for web search tool
	var searchConfig model.SearchConfig
	if err := database.DB.Where("is_active = ?", true).First(&searchConfig).Error; err != nil {
		applogger.Error("failed to load active search config, proceeding without search", "error", err)
	}

	return Execute(TaskParams{
		TaskRequirement: params.Guidance, // Guidance IS the task requirement
		Guidance:        params.Guidance,
		Background:      params.Background,
		Metadata:        params.Metadata,
		LLMConfig:       params.LLMConfig,
		MaxIterations:   0,
		SessionID:       params.SessionID,
		PersonID:        params.PersonID,
		WorkID:          params.WorkID,
		SearchConfig:    &searchConfig,
		Ctx:             params.Ctx,
		GuidanceCh:      params.GuidanceCh,
	})
}

// TaskParams contains all parameters needed for task execution.
type TaskParams struct {
	TaskRequirement string                   // The task description to execute (from Decide phase Guidance)
	Guidance        string                   // Execution intent from Decide phase, injected into system prompt
	Background      string                   // Full context from Decide phase: trigger event, participants, comprehension
	Metadata        *Metadata                // System-generated traceability info from work creation
	LLMConfig       *model.LLMConfig         // LLM configuration for the task
	MaxIterations   int                      // Override for max loop iterations (0 = use default)
	SessionID       int64                    // Session ID for interaction records and workspace
	PersonID        int64                    // Person ID for tools that need person context (e.g., wake_me_when)
	WorkID          int64                    // Work ID for interaction record association
	SearchConfig    *model.SearchConfig      // Search configuration for web search tool
	Ctx             context.Context          // Cancellation context from the caller
	GuidanceCh      <-chan GuidanceDirective // Channel for receiving new guidance during execution
}

// Execute runs a task and returns the result.
//
// This is the single entry point for task execution.
// It creates all necessary components internally and runs
// the task loop to completion.
func Execute(params TaskParams) *TaskResult {
	maxIterations := params.MaxIterations
	if maxIterations <= 0 {
		maxIterations = config.Get().TaskMaxIterations
	}

	applogger.Info("TaskExecutor starting",
		"session_id", params.SessionID,
		"max_iterations", maxIterations,
	)

	ws := workspace.InitWorkspace(params.PersonID, params.SessionID)

	settings := config.Get()
	iterationWindow := settings.MinIterationWindow
	maxIterationWindow := settings.MaxIterationWindow
	notesMaxChars := settings.NotesMaxChars

	writeNotesTool := tools.NewWriteNotesTool(params.PersonID, params.SessionID, notesMaxChars)
	notesContent := writeNotesTool.ReadNotes()

	toolList := buildToolList(params.SessionID, params.PersonID, params.SearchConfig, notesMaxChars)

	// Build tool descriptions string (moved to last user message for cache optimization).
	toolDescLines := []string{"Available tools:"}
	for _, t := range toolList {
		toolDescLines = append(toolDescLines, fmt.Sprintf("- %s: %s", t.Name(), t.Description()))
	}
	toolDescStr := strings.Join(toolDescLines, "\n")

	systemPrompt := buildSystemPrompt(params.Background, params.Metadata)

	workspaceDir := workspace.GetWorkspacePath(params.PersonID, params.SessionID)
	outputDir := workspace.GetOutputDir(params.PersonID, params.SessionID)

	contextManager := taskcontext.NewContextManager(
		systemPrompt,
		iterationWindow,
		maxIterationWindow,
		notesContent,
		workspaceDir,
		outputDir,
		toolDescStr,
	)

	// Initial task directive from Decide phase — recorded in guidance history
	// (last user message) instead of the static system prompt for cache optimization.
	if params.Guidance != "" {
		contextManager.AddGuidance(params.Guidance, "")
	}

	llmClient := llm.NewChatModelWithTemperature(
		params.LLMConfig.BaseURL,
		params.LLMConfig.APIKey,
		params.LLMConfig.ModelID,
		llm.TemperatureCreative,
	)

	taskLoop := NewTaskLoop(
		llmClient,
		params.LLMConfig,
		toolList,
		contextManager,
		maxIterations,
		params.SessionID,
		0,
		params.WorkID,
		writeNotesTool,
		params.GuidanceCh,
	)

	loopResult := taskLoop.Run(params.Ctx)

	finalNotes := writeNotesTool.ReadNotes()

	// Note: <workspace>/.meta/fingerprint.txt is no longer written here.
	// Its responsibility moved to the reflection pipeline (reflectSession),
	// which writes it at the end of each reflection to mark "this is what
	// notes.jsonl looked like when I last processed it". The heartbeat then
	// compares the current notes.jsonl hash against this file to decide whether
	// to re-trigger reflection.

	result := &TaskResult{
		Workspace: ws,
		Notes:     finalNotes,
	}

	if finalNotes != "" {
		result.NotesLength = len(finalNotes)
	}

	if loopResult.Status == "success" && loopResult.Result != "" {
		result.Status = "success"
		result.Output = loopResult.Result
		applogger.Info("TaskExecutor completed successfully",
			"session_id", params.SessionID,
			"output_len", len(result.Output),
		)
	} else {
		result.Status = "failure"
		if loopResult.Reason != "" {
			result.Error = loopResult.Reason
		} else {
			result.Error = "Unknown error"
		}
		applogger.Error("TaskExecutor failed",
			"session_id", params.SessionID,
			"error", result.Error,
		)
	}

	return result
}

// buildSystemPrompt constructs the static system prompt for the task loop.
// Built once at task start; includes background context, basic rules, and
// static instruction blocks. Directives (from Decide phase and routeWork)
// are managed separately by ContextManager and injected into the last user
// message to preserve LLM prefix caching on the system prompt.
func buildSystemPrompt(background string, metadata *Metadata) string {
	parts := []string{
		"[Background]",
		background,
	}

	// Inject Metadata as a [Metadata] section if available.
	if metadata != nil {
		parts = append(parts,
			"",
			"[Metadata]",
			metadata.String(),
		)
	}

	parts = append(parts,
		"",
		"CRITICAL: Before calling any tool, you MUST first explain your reasoning",
		"in the content field. Describe what you plan to do and why.",
		"Only after explaining your thought process, make the tool call.",
		"",
		"Always verify your actions by checking the results.",
		"",
		"WORKSPACE ORGANIZATION:",
		"- Before creating files, consider whether this task relates to an existing project:",
		"  - If starting a new project (e.g., building an app, writing a report), create a dedicated subdirectory for it",
		"  - If continuing or modifying existing work, first check what subdirectories exist and work within the appropriate one",
		"- This keeps your workspace organized but is not enforced — use your judgment",
		"",
		"COMPLETION OUTPUT:",
		"Remember: the recipient cannot see your output/ directory. If they need any of your output files, you must use deliver_to to send them before summarizing.",
		"- Deliver whole directories (e.g., paths: [\"my-project\"]) rather than individual files.",
		"- Verify what you produced with `ls output/` or `find output/ -type f` first.",
		"",
		"- Accomplishments: what was achieved, with specific details",
		"- Verification: how correctness was confirmed (test results, checks, etc.)",
		"- Status: if partially completed, state exactly what's done and what remains",
		"",
		"- Do NOT list raw file paths from your output/ directory — delivered files are already in the recipient's received/ area",
		"- Never fabricate or guess file paths — verify with `pwd` or `ls` if needed",
		"",
		"[Understanding Current State]",
		"To understand the current project state:",
		"- Use read_text_file to read file contents",
		"- Use 'ls -la' to list files in your working directory",
		"- Use 'find . -type f' to discover all files",
		"- Check your NOTES (provided above) for previous progress",
		"",
		"FILE OPERATIONS:",
		"- read_text_file: Read file contents with line offset/limit. Preferred over bash cat.",
		"- write_text_file: Create, overwrite, or append to files. Preferred over bash echo/heredoc.",
		"- edit_text_file: Make precise text replacements in existing files. Preferred for modifying files.",
		"  - Copy old_str EXACTLY from read_text_file output, preserving indentation and special characters",
		"  - Keep old_str concise but unique enough to match exactly one location",
		"- bash: Use for system commands (mkdir, find, git, build, etc.) and directory operations",
		"",
		"[NOTES Usage Guide]",
		"The write_notes tool appends structured entries to your notes.",
		"",
		"Entry types:",
		"- observation: Something you discovered",
		"- decision: A choice you made (explain why)",
		"- finding: A key result or conclusion",
		"- correction: A fix to a previous entry (use conflicts_with)",
		"- progress: Current status and next steps",
		"",
		"Best practices:",
		"- Each entry is APPENDED, not overwritten",
		"- Write CONCISE entries — notes have a size limit",
		"- Only write IMPORTANT information — skip trivial or obvious facts",
		"- Ask: would losing this information hurt the task? If not, skip it",
		"- Include file references when relevant",
		"- Use conflicts_with when correcting earlier decisions",
		"- Write self-contained entries (future LLM calls have no memory)",
		"- When you repeatedly attempt the same action (e.g., same tool call, same approach),",
		"  record the attempt count and the fact that it keeps failing.",
		"  Example: \"[Attempt #12] npm run dev — failed again with same empty stdout.",
		"  This approach is not working.\"",
		"",
		"[Critical Identifiers]",
		"- If you encounter an identifier that cannot be recovered through filesystem inspection (ls, find, git log, etc.), record it explicitly in your notes.",
		"- File paths are recoverable — you don't need to record them.",
		"- External API response IDs, user-provided tokens, and unique session identifiers should be preserved.",
		"",
		"[Past Experience]",
		"You have past experiences (lessons learned from prior tasks). Use scan_my_experience to search for relevant experiences by keyword, then recall_my_experience to read the full content of a specific one.",
	)

	return strings.Join(parts, "\n")
}

// buildToolList creates the list of available tools for the task loop.
// Always includes read_text_file, write_text_file, edit_text_file, bash,
// write_notes, wake_me_when, scan_my_experience, and recall_my_experience;
// adds web_search if search config is available.
func buildToolList(sessionID, personID int64, searchConfig *model.SearchConfig, notesMaxChars int) []tools.Tool {
	toolList := []tools.Tool{
		tools.NewReadTextFileTool(personID, sessionID),
		tools.NewWriteTextFileTool(personID, sessionID),
		tools.NewEditTextFileTool(personID, sessionID),
		tools.NewBashTool(personID, sessionID),
		tools.NewWriteNotesTool(personID, sessionID, notesMaxChars),
		tools.NewWakeMeWhenTool(personID, sessionID),
		tools.NewScanExperienceTool(personID),
		tools.NewRecallExperienceTool(personID),
		tools.NewDeliverToTool(personID, sessionID),
		tools.NewSearchChatHistoriesTool(personID),
	}

	if searchConfig != nil && searchConfig.IsAvailable() {
		toolList = append(toolList, tools.NewWebSearchTool(searchConfig))
	}

	return toolList
}
