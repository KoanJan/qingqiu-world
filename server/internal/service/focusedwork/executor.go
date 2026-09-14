// Package focusedwork implements sustained, multi-step execution for world-interaction requests.
//
// This package provides the FocusedWork execution pipeline when the runtime
// determines that a request requires a sustained course of action with
// world interaction (e.g., file operations, web searches, code execution).
//
// The main entry point is Execute, which:
//  1. Initializes the session workspace structure
//  2. Builds the system prompt and tool list
//  3. Creates the context manager with iteration window
//  4. Runs the ReAct FocusedLoop to completion
//  5. Returns a FocusedWorkResult with success/failure status
//
// Design principles:
//   - Input: focused-work guidance (structured, not raw user message)
//   - Output: final result (success result or failure with reason)
//   - Internal isolation: all process info is hidden from the outside
//   - No pollution of the chat system
package focusedwork

import (
	"context"
	"fmt"
	"strings"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	focusedworkcontext "qingqiu-world-server/internal/service/focusedwork/context"
	"qingqiu-world-server/internal/service/focusedwork/tools"
	"qingqiu-world-server/internal/service/llm"
	"qingqiu-world-server/internal/service/workspace"
)

// FocusedWorkResult represents the outcome of focused-work execution.
// On success, Output contains the final content. On failure, Error contains the reason.
// Notes and Workspace are always populated for observability.
type FocusedWorkResult struct {
	Status      string `json:"status"`
	Output      string `json:"output,omitempty"`
	Error       string `json:"error,omitempty"`
	Notes       string `json:"notes,omitempty"`
	Workspace   string `json:"workspace,omitempty"`
	NotesLength int    `json:"notes_length,omitempty"`
}

// GuidanceDirective is a structured guidance message sent from the Runtime
// to the FocusedLoop during execution. It carries both the executable directive
// (Guidance) and the cognitive context explaining why (Reason).
//
// This struct is passed through the guidance channel instead of a bare string,
// so the FocusedLoop's LLM can understand the full context of a route or cancel
// decision — not just the "what" but also the "why".
type GuidanceDirective struct {
	Guidance string // What to do: the executable directive
	Reason   string // Why: user's original message, inferred intent, and Decide's reasoning
}

// RunFocusedWorkParams contains all parameters needed for the full FocusedWork pipeline.
// After the cognitive order refactoring, Guidance from the Decide phase
// replaces the old Rewrite step — comprehension and decision have already
// produced a clear execution intent, so rewriting is unnecessary.
type RunFocusedWorkParams struct {
	LLMConfig    *model.LLMConfig
	SessionID    int64
	PersonID     int64 // Person ID of the executing agent
	WorkID       int64
	Guidance     string           // Execution intent from Decide phase (replaces Rewrite)
	Background   string           // Full context from Decide phase: trigger event, participants, comprehension
	FocusContext string           // Runtime-selected related Focus handoffs and shared notes
	FocusPhase   model.FocusPhase // Runtime-owned phase at FocusedLoop entry
	Checkpoint   string           // Compact runtime-owned checkpoint at FocusedLoop entry
	Metadata     *Metadata        // System-generated traceability info from work creation
	Ctx          context.Context
	GuidanceCh   <-chan GuidanceDirective // Channel for receiving new guidance during execution
}

// RunFocusedWork executes the full FocusedWork pipeline using Guidance as its requirement.
//
// After the cognitive order refactoring, the Comprehend-Decide pipeline has
// already produced a clear execution intent (Guidance). This replaces the
// old Rewrite step — there is no need to rewrite the user message because
// all cognitive work (understanding intent, resolving references, determining
// what to do) was completed before this point.
//
// New guidance can arrive via GuidanceCh during execution. The FocusedLoop
// observes the channel at each iteration boundary and injects new directives
// as environment events in the ReAct cycle.
func RunFocusedWork(params RunFocusedWorkParams) *FocusedWorkResult {
	applogger.Info("RunFocusedWork: starting with Guidance",
		"session_id", params.SessionID,
		"guidance", params.Guidance,
	)

	// Load search config for web search tool
	var searchConfig model.SearchConfig
	if err := database.DB.Where("is_active = ?", true).First(&searchConfig).Error; err != nil {
		applogger.Error("failed to load active search config, proceeding without search", "error", err)
	}

	return ExecuteFocusedWork(FocusedWorkParams{
		FocusedWorkRequirement: params.Guidance, // Guidance IS the focused-work requirement
		Guidance:               params.Guidance,
		Background:             params.Background,
		FocusContext:           params.FocusContext,
		FocusPhase:             params.FocusPhase,
		Checkpoint:             params.Checkpoint,
		Metadata:               params.Metadata,
		LLMConfig:              params.LLMConfig,
		MaxIterations:          0,
		SessionID:              params.SessionID,
		PersonID:               params.PersonID,
		WorkID:                 params.WorkID,
		SearchConfig:           &searchConfig,
		Ctx:                    params.Ctx,
		GuidanceCh:             params.GuidanceCh,
	})
}

// FocusedWorkParams contains all parameters needed for focused-work execution.
type FocusedWorkParams struct {
	FocusedWorkRequirement string                   // The focused-work requirement from Decide guidance
	Guidance               string                   // Execution intent from Decide phase, injected into system prompt
	Background             string                   // Full context from Decide phase: trigger event, participants, comprehension
	FocusContext           string                   // Runtime-selected related Focus handoffs and shared notes
	FocusPhase             model.FocusPhase         // Runtime-owned phase at FocusedLoop entry
	Checkpoint             string                   // Compact runtime-owned checkpoint at FocusedLoop entry
	Metadata               *Metadata                // System-generated traceability info from work creation
	LLMConfig              *model.LLMConfig         // LLM configuration for focused work
	MaxIterations          int                      // Override for max loop iterations (0 = use default)
	SessionID              int64                    // Session ID for interaction records and workspace
	PersonID               int64                    // Person ID for tools that need person context (e.g., wake_me_when)
	WorkID                 int64                    // Work ID for interaction record association
	SearchConfig           *model.SearchConfig      // Search configuration for web search tool
	Ctx                    context.Context          // Cancellation context from the caller
	GuidanceCh             <-chan GuidanceDirective // Channel for receiving new guidance during execution
}

// ExecuteFocusedWork runs focused work and returns the result.
//
// This is the single entry point for focused-work execution.
// It creates all necessary components internally and runs
// the FocusedLoop to completion.
func ExecuteFocusedWork(params FocusedWorkParams) *FocusedWorkResult {
	maxIterations := params.MaxIterations
	if maxIterations <= 0 {
		maxIterations = config.Get().FocusedWorkMaxIterations
	}

	applogger.Info("FocusedWorkExecutor starting",
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

	systemPrompt := buildSystemPrompt(
		params.Background,
		params.FocusContext,
		params.Metadata,
		buildKBSection(params.PersonID),
		workspace.GetAgentOwnedSpacePath(params.PersonID),
		workspace.GetOutputDir(params.PersonID, params.SessionID),
		params.WorkID,
		params.FocusPhase,
		params.Checkpoint,
	)

	workspaceDir := workspace.GetWorkspacePath(params.PersonID, params.SessionID)
	outputDir := workspace.GetOutputDir(params.PersonID, params.SessionID)

	contextManager := focusedworkcontext.NewContextManager(
		systemPrompt,
		iterationWindow,
		maxIterationWindow,
		notesContent,
		workspaceDir,
		outputDir,
		toolDescStr,
	)

	// Initial focused-work directive from Decide — recorded in guidance history
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

	focusedLoop := NewFocusedLoop(
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

	loopResult := focusedLoop.Run(params.Ctx)

	finalNotes := writeNotesTool.ReadNotes()

	// Note: <workspace>/.meta/fingerprint.txt is no longer written here.
	// Its responsibility moved to the reflection pipeline (reflectSession),
	// which writes it at the end of each reflection to mark "this is what
	// notes.jsonl looked like when I last processed it". The heartbeat then
	// compares the current notes.jsonl hash against this file to decide whether
	// to re-trigger reflection.

	result := &FocusedWorkResult{
		Workspace: ws,
		Notes:     finalNotes,
	}

	if finalNotes != "" {
		result.NotesLength = len(finalNotes)
	}

	if loopResult.Status == "success" && loopResult.Result != "" {
		result.Status = "success"
		result.Output = loopResult.Result
		applogger.Info("FocusedWorkExecutor completed successfully",
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
		applogger.Error("FocusedWorkExecutor failed",
			"session_id", params.SessionID,
			"error", result.Error,
		)
	}

	return result
}

// buildSystemPrompt constructs the static system prompt for the FocusedLoop.
// Built once at FocusedWork start; includes background context, basic rules, and
// static instruction blocks. Directives (from Decide phase and routeWork)
// are managed separately by ContextManager and injected into the last user
// message to preserve LLM prefix caching on the system prompt.
func buildSystemPrompt(background, focusContext string, metadata *Metadata, kbSection, aosRoot, defaultDir string, focusID int64, focusPhase model.FocusPhase, checkpoint string) string {
	parts := []string{
		"[Focus Brief]",
		fmt.Sprintf("FocusedWork ID: %d", focusID),
		fmt.Sprintf("Runtime phase: %s", focusPhaseLabel(focusPhase)),
		"Runtime checkpoint: " + checkpoint,
		"",
		"[Focused Mode]",
		"You are already inside a FocusedLoop: a sustained course of work whose next steps may depend on what you observe and do.",
		"Own the course from the current guidance through verification, notes, and a clear handoff. Do not reduce it to a one-shot reply when further investigation or action is needed.",
		"Use tools iteratively when their results affect your next decision. Keep durable progress in notes so a later Focus can understand what happened without relying on this conversation window.",
		"Finish when the guidance is genuinely satisfied, or when you have recorded the blocker, unresolved work, and a concrete next step.",
		"Treat notes and handoffs as source material. Confirm facts before presenting them as current results.",
		"When finishing, use explicit sections named Confirmed Findings, Artifacts, Unresolved, and Next Step. List only Jinshu IDs or AOS resource references under Artifacts; use 'none' when there are no artifacts.",
		"",
		"[Background]",
		background,
	}
	if focusContext != "" {
		parts = append(parts, "[Related Focus Context]", focusContext)
	}

	// Inject Metadata as a [Metadata] section if available.
	if metadata != nil {
		parts = append(parts,
			"",
			"[Metadata]",
			metadata.String(),
		)
	}

	// Inject the authorized knowledge base inventory if the agent has any.
	if kbSection != "" {
		parts = append(parts,
			"",
			kbSection,
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
		fmt.Sprintf("- Your Agent Owned Space resource root is %s. You may read and write any resource beneath it.", aosRoot),
		fmt.Sprintf("- This FocusedWork defaults to %s. Other work/<session_id>/ directories and private/ remain available when useful.", defaultDir),
		"- Runtime metadata is outside Agent Owned Space and is not a file resource you can access.",
		"- Before creating files, consider whether this Focus relates to an existing project:",
		"  - If starting a new project (e.g., building an app, writing a report), create a dedicated subdirectory for it",
		"  - If continuing or modifying existing work, first check what subdirectories exist and work within the appropriate one",
		"- This keeps your workspace organized but is not enforced — use your judgment",
		"",
		"COMPLETION OUTPUT:",
		"Remember: the recipient cannot see your output/ directory. If they need any of your output files, you must use send_jinshu to send them before summarizing.",
		"- Deliver whole directories (e.g., paths: [\"my-project\"]) rather than individual files.",
		"- Verify what you produced with `ls output/` or `find output/ -type f` first.",
		"",
		"- Accomplishments: what was achieved, with specific details",
		"- Verification: how correctness was confirmed (test results, checks, etc.)",
		"- Status: if partially completed, state exactly what's done and what remains",
		"",
		"- Do NOT list raw file paths from your output/ directory — delivered files are already in the recipient's jinshu received area",
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
		"- Ask: would losing this information hurt a later continuation of this FocusedWork? If not, skip it",
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
		"You have past experiences (lessons learned from prior FocusedWork runs and other work). Use scan_my_experience to search for relevant experiences by keyword, then recall_my_experience to read the full content of a specific one.",
	)

	return strings.Join(parts, "\n")
}

// focusPhaseLabel renders a persisted Focus phase for the execution prompt.
func focusPhaseLabel(phase model.FocusPhase) string {
	switch phase {
	case model.FocusPhaseExecuting:
		return "executing"
	case model.FocusPhasePaused:
		return "paused"
	case model.FocusPhaseCompleted:
		return "completed"
	case model.FocusPhaseFailed:
		return "failed"
	case model.FocusPhaseCancelled:
		return "cancelled"
	default:
		applogger.Error("focused work: unknown focus phase", "focus_phase", phase)
		return "unknown"
	}
}

// buildKBSection renders the authorized knowledge base inventory for the FocusedWork
// system prompt. KB contents are reachable only through scan_kb /
// list_kb_documents (retrieval, never raw file reads). Returns an empty string
// when the agent has no authorized KBs so the section is omitted entirely.
func buildKBSection(personID int64) string {
	kbs, err := dops.ListAuthorizedKBs(personID)
	if err != nil {
		applogger.Error("failed to load authorized KBs for focused-work prompt", "person_id", personID, "error", err)
		return ""
	}
	if len(kbs) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("[Knowledge Bases]\n")
	b.WriteString("Authorized knowledge bases (use scan_kb to search them, list_kb_documents to list their documents):\n")
	for _, k := range kbs {
		if k.Description != "" {
			b.WriteString(fmt.Sprintf("- KB #%d %q: %s\n", k.ID, k.Name, k.Description))
		} else {
			b.WriteString(fmt.Sprintf("- KB #%d %q\n", k.ID, k.Name))
		}
	}
	return b.String()
}

// buildToolList creates the list of available tools for the FocusedLoop.
// Always includes read_text_file, write_text_file, edit_text_file, bash,
// write_notes, scan_my_experience, recall_my_experience, scan_kb and
// list_kb_documents; adds web_search if search config is available.
//
// Note: wake_me_when was promoted to a top-level Action (ActionCreateAlarm)
// in 0.1.3 — setting an alarm is a world action, not a workspace operation.
// It is no longer registered as a FocusedLoop tool.
func buildToolList(sessionID, personID int64, searchConfig *model.SearchConfig, notesMaxChars int) []tools.Tool {
	toolList := []tools.Tool{
		tools.NewReadTextFileTool(personID, sessionID),
		tools.NewWriteTextFileTool(personID, sessionID),
		tools.NewEditTextFileTool(personID, sessionID),
		tools.NewBashTool(personID, sessionID),
		tools.NewWriteNotesTool(personID, sessionID, notesMaxChars),
		tools.NewScanExperienceTool(personID),
		tools.NewRecallExperienceTool(personID),
		tools.NewSendJinshuTool(personID, sessionID),
		tools.NewSearchChatHistoriesTool(personID),
		tools.NewScanJinshuTool(personID),
		tools.NewReadJinshuTool(personID),
		tools.NewCopyFromJinshuTool(personID, sessionID),
		tools.NewScanKBTool(personID),
		tools.NewListKBDocumentsTool(personID),
	}

	if searchConfig != nil && searchConfig.IsAvailable() {
		toolList = append(toolList, tools.NewWebSearchTool(searchConfig))
	}

	return toolList
}
