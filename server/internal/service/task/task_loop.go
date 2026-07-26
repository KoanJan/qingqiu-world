package task

import (
	"context"
	"encoding/json"
	"fmt"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/llm"
	taskcontext "qingqiu-world-server/internal/service/task/context"
	"qingqiu-world-server/internal/service/task/tools"

	applogger "qingqiu-world-server/internal/logger"
)

// hardOutputLimit is the system-level byte fallback threshold for tool outputs.
// If a tool's total output exceeds this, the content is discarded and replaced
// with a notice. This is a safety net only — tools are expected to self-truncate
// at the semantic level (see tools.TruncateHead / TruncateTail) before marshalling.
// Must be greater than tools.DefaultTruncateBytes + JSON overhead buffer.
const hardOutputLimit = 50 * 1024 // 50KB

// TaskLoop implements the ReAct-style task loop for autonomous task execution.
//
// The loop iterates:
//   - Call LLM with current context (window-controlled by ContextManager)
//   - If LLM returns tool_calls: execute tools, append results, continue
//   - If LLM returns stop: deliver the content
//   - If max_iterations reached: deliver failure with reason
//
// Every iteration is recorded to the interactions table with:
//   - type=1 (request): the messages sent to the LLM
//   - type=2 (response): the LLM output (content, tool_calls, finish_reason)
//   - type=3 (guidance): external guidance directive received at iteration boundary
//
// Notes checkpoint strategy:
//   - Agent can voluntarily call write_notes at any time
//   - Forced checkpoint only when distance from last voluntary write >= window
//   - This respects agent's autonomy while ensuring memory persistence
//   - Final iteration always writes notes if task not completed
type TaskLoop struct {
	llmClient        *llm.ChatModel              // Main LLM client with tool binding
	llmConfig        *model.LLMConfig            // LLM config for creating checkpoint client
	toolRegistry     map[string]tools.Tool       // Tool name -> Tool mapping
	contextManager   *taskcontext.ContextManager // Context manager with window control
	maxIterations    int                         // Maximum number of loop iterations
	sessionID        int64                       // Session ID for interaction records
	userMsgID        int64                       // User message ID that triggered execution
	workID           int64                       // Work ID for interaction record association
	writeNotesTool   *tools.WriteNotesTool       // Write notes tool for checkpoint iterations
	checkpointClient *llm.ChatModel              // Lazy-initialized LLM client for checkpoint iterations
	lastNotesIter    int                         // Last iteration where write_notes was called (voluntary or forced)
	guidanceCh       <-chan GuidanceDirective    // Channel for observing new guidance during execution
	forceCheckpoint  bool                        // Set by cycle detection to force a checkpoint
	blockReason      string                      // Reason for forced checkpoint (from cycle detection)
}

// NewTaskLoop creates a new TaskLoop instance.
// The tool list is converted to a name-keyed registry for efficient lookup during execution.
func NewTaskLoop(
	llmClient *llm.ChatModel,
	llmConfig *model.LLMConfig,
	toolList []tools.Tool,
	contextManager *taskcontext.ContextManager,
	maxIterations int,
	sessionID, userMsgID, workID int64,
	writeNotesTool *tools.WriteNotesTool,
	guidanceCh <-chan GuidanceDirective,
) *TaskLoop {
	registry := make(map[string]tools.Tool)
	for _, t := range toolList {
		registry[t.Name().String()] = t
	}

	return &TaskLoop{
		llmClient:      llmClient,
		llmConfig:      llmConfig,
		toolRegistry:   registry,
		contextManager: contextManager,
		maxIterations:  maxIterations,
		sessionID:      sessionID,
		userMsgID:      userMsgID,
		workID:         workID,
		writeNotesTool: writeNotesTool,
		guidanceCh:     guidanceCh,
	}
}

// LoopResult represents the outcome of the task loop execution.
type LoopResult struct {
	Status string `json:"status"`           // "success" or "failure"
	Result string `json:"result,omitempty"` // Final content on success
	Reason string `json:"reason,omitempty"` // Failure reason on failure
}

// Run executes the agent loop.
//
// This is the main entry point. It runs the ReAct loop until:
//   - LLM returns a stop response (success)
//   - Max iterations reached (failure, after writing notes)
//
// The task requirement is already injected via ContextManager
// (as part of the system prompt with Guidance), so it is not passed
// as a parameter here.
func (tl *TaskLoop) Run(ctx context.Context) *LoopResult {
	applogger.Info("TaskLoop starting",
		"max_iterations", tl.maxIterations,
		"session_id", tl.sessionID,
		"work_id", tl.workID,
	)

	for iteration := 1; iteration <= tl.maxIterations; iteration++ {
		// Check if the task has been cancelled (e.g., session deleted)
		if ctx != nil && ctx.Err() != nil {
			applogger.Info("TaskLoop cancelled, stopping execution",
				"session_id", tl.sessionID,
				"iteration", iteration,
			)
			return &LoopResult{Status: "failure", Reason: "task cancelled"}
		}

		// If cycle detection triggered a forced checkpoint, run it now.
		// The agent must reflect on the cycle and write notes before the work terminates.
		if tl.forceCheckpoint {
			result := tl.runCycleBlockedCheckpoint(ctx, iteration, tl.blockReason)
			return result
		}

		// Observe new guidance from the channel at each iteration boundary.
		// New guidance is an environment event that the agent must observe
		// in the ReAct cycle — it represents a change in execution intent
		// (e.g., user correction, approach change, cancellation).
		// Drain all pending guidance to handle multiple updates.
		tl.observeNewGuidance(iteration)

		applogger.Info("TaskLoop iteration", "iteration", iteration, "max", tl.maxIterations)

		if tl.writeNotesTool != nil {
			tl.writeNotesTool.TrimNotes()
			tl.contextManager.RefreshNotes(tl.writeNotesTool.ReadNotes())
		}

		messages := tl.contextManager.BuildMessages()

		isCheckpoint := tl.isCheckpointIteration(iteration)
		isFinal := iteration == tl.maxIterations

		if isCheckpoint || isFinal {
			result := tl.runNotesIteration(ctx, iteration, messages, isFinal)
			if result.Status == "failure" {
				return result
			}
			continue
		}

		tl.weakWriteInteraction(iteration, model.InteractionTypeRequest, map[string]interface{}{
			"messages": messages,
		})

		response, err := tl.invokeLLM(ctx, messages)
		if err != nil {
			applogger.Error("TaskLoop LLM error", "iteration", iteration, "error", err)
			return &LoopResult{Status: "failure", Reason: fmt.Sprintf("LLM invocation failed at iteration %d: %s", iteration, err.Error())}
		}

		finishReason := response.FinishReason
		content := response.Content
		toolCalls := response.ToolCalls

		switch finishReason {
		case "stop":
			applogger.Debug("TaskLoop LLM response",
				"finish_reason", "stop",
				"content", content,
			)
		case "tool_calls":
			tcSummary := make([]map[string]interface{}, 0, len(toolCalls))
			for _, tc := range toolCalls {
				tcSummary = append(tcSummary, map[string]interface{}{
					"id":   tc.ID,
					"name": tc.Function.Name,
					"args": tc.Function.Arguments,
				})
			}
			applogger.Debug("TaskLoop LLM response",
				"finish_reason", "tool_calls",
				"content", content,
				"tool_calls", fmt.Sprintf("%v", tcSummary),
			)
		case "length":
			applogger.Debug("TaskLoop LLM response",
				"finish_reason", "length",
				"content", content,
			)
		}

		tl.weakWriteInteraction(iteration, model.InteractionTypeResponse, map[string]interface{}{
			"content":       content,
			"tool_calls":    toolCalls,
			"finish_reason": finishReason,
		})

		switch finishReason {
		case "stop":
			applogger.Info("TaskLoop completed", "iteration", iteration)
			tl.updateNotesOnStop(ctx, iteration, content, messages)
			return &LoopResult{Status: "success", Result: content}

		case "tool_calls":
			if content != "" {
				applogger.Info("TaskLoop thoughts", "iteration", iteration, "thoughts", content[:min(500, len(content))])
			}

			// Discard reasoning content from tool_calls to establish an information
			// boundary between TaskLoop internals and the chat layer.
			//
			// When tool_calls are accompanied by reasoning content, that content
			// propagates into subsequent iterations and eventually leaks into
			// LoopResult.Result via the final stop response. The chat LLM then
			// misinterprets internal reasoning (e.g., "the command is correct")
			// as accomplished facts (e.g., "the service is running"), causing
			// hallucination in the final user-facing response.
			//
			// By discarding reasoning content here, we cut off the hallucination
			// at its source: internal process information stays inside TaskLoop,
			// and only tool calls and their results are carried forward. The LLM
			// can still reason about next steps from the task description and
			// tool results alone — the reasoning content is redundant signal.
			assistantMsg := llm.Message{
				Role:      "assistant",
				Content:   "",
				ToolCalls: toolCalls,
			}

			var toolResults []llm.Message
			hasWriteNotes := false
			for _, tc := range toolCalls {
				if tc.Function.Name == tools.ToolNameWriteNotes.String() {
					hasWriteNotes = true
				}
				toolResult := tl.executeToolCall(tc)
				toolResults = append(toolResults, toolResult)
			}

			if hasWriteNotes {
				tl.lastNotesIter = iteration
				applogger.Info("Agent voluntarily called write_notes", "iteration", iteration)
			}

			tl.contextManager.AddIteration(assistantMsg, toolResults)

		case "length":
			applogger.Error("TaskLoop finish_reason=length", "iteration", iteration)

			assistantMsg := llm.Message{
				Role:    "assistant",
				Content: content,
			}
			if len(toolCalls) > 0 {
				assistantMsg.ToolCalls = toolCalls
			}

			tl.contextManager.AddIteration(assistantMsg, nil)

			tl.contextManager.AddIteration(
				llm.Message{
					Role:    "user",
					Content: "[System] Your previous response was truncated due to length limits. Your tool calls were NOT executed. Please continue with a more concise response.",
				},
				nil,
			)

		default:
			applogger.Error("TaskLoop unexpected finish_reason", "finish_reason", finishReason, "iteration", iteration)
		}
	}

	reason := fmt.Sprintf("Task did not complete within %d iterations", tl.maxIterations)
	return &LoopResult{Status: "failure", Reason: reason}
}

// observeNewGuidance drains all pending guidance from the channel and injects
// each as an observation (user message) into the ReAct cycle.
//
// In the ReAct paradigm, new guidance is an environment event — the agent
// must observe it, think about how it affects the current plan, and act
// accordingly. This is semantically different from a callback check: the
// guidance arrives as a channel event, modeling it as something that happens
// in the agent's environment that the agent must perceive.
//
// Each directive is written to two places to ensure full traceability:
// 1. Interactions table (type=3): audit trail and transition judgment record
// 2. Context manager: LLM sees it as a [New Directive] observation
//
// The iteration parameter is the current loop iteration number, so the
// guidance interaction record is grouped with the iteration that consumes it.
func (tl *TaskLoop) observeNewGuidance(iteration int) {
	if tl.guidanceCh == nil {
		return
	}
	for {
		select {
		case directive := <-tl.guidanceCh:
			applogger.Info("TaskLoop: observed new guidance",
				"session_id", tl.sessionID,
				"work_id", tl.workID,
				"iteration", iteration,
				"guidance", directive.Guidance,
				"reason", directive.Reason,
			)

			// 1. Write to interactions: this is a cognitive event that changes
			// the execution direction. Must be visible in the audit trail.
			tl.weakWriteInteraction(iteration, model.InteractionTypeGuidance, map[string]interface{}{
				"guidance": directive.Guidance,
				"reason":   directive.Reason,
			})

			// 2. Record in guidance history — persists in the last user message
			// across all iterations, never dropped by the dynamic window.
			tl.contextManager.AddGuidance(directive.Guidance, directive.Reason)

			// 3. Inject as an observation in the ReAct cycle.
			// Both guidance (what to do) and reason (why) are included so the
			// LLM has the full cognitive context, not just the bare directive.
			tl.contextManager.AddIteration(llm.Message{
				Role:    "user",
				Content: fmt.Sprintf("[New Directive]\nGuidance: %s\nReason: %s", directive.Guidance, directive.Reason),
			}, nil)
		default:
			return
		}
	}
}

// isCheckpointIteration checks if this iteration should be a forced notes checkpoint.
//
// Checkpoint is triggered when:
//   - Distance from last voluntary write_notes >= window
//   - This respects agent's autonomy while ensuring memory persistence
//
// Final iteration is handled separately.
func (tl *TaskLoop) isCheckpointIteration(iteration int) bool {
	if iteration == tl.maxIterations {
		return false
	}
	window := tl.contextManager.IterationWindow()
	distance := iteration - tl.lastNotesIter
	return distance >= window
}

// runNotesIteration runs a notes checkpoint or final notes iteration.
//
// During this iteration, only write_notes tool is available.
// The agent must use it to persist information before older iterations
// are discarded from the context window.
//
// On final iteration (isFinal=true), returns failure result after saving notes.
// On checkpoint iteration, returns success to continue the loop.
func (tl *TaskLoop) runNotesIteration(ctx context.Context, iteration int, messages []llm.Message, isFinal bool) *LoopResult {
	if tl.writeNotesTool == nil {
		applogger.Error("Cannot run notes iteration: write_notes_tool not initialized")
		if isFinal {
			return &LoopResult{Status: "failure", Reason: "Task did not complete within max iterations"}
		}
		return &LoopResult{Status: "success"}
	}

	if tl.checkpointClient == nil {
		tl.checkpointClient = llm.NewChatModelWithTemperature(tl.llmConfig.BaseURL, tl.llmConfig.APIKey, tl.llmConfig.ModelID, llm.TemperatureCreative)
	}

	iterType := "checkpoint"
	if isFinal {
		iterType = "final"
	}
	applogger.Info("Running notes iteration", "type", iterType, "iteration", iteration)

	var checkpointMsg string
	if isFinal {
		checkpointMsg = `[Final Iteration - Save Your Progress]
You have reached the maximum number of iterations.
The task could not be completed in time.

MANDATORY: You must save your progress now using the write_notes tool.
This is the ONLY tool available to you.

Use write_notes to APPEND entries to your NOTES:
- entry_type: "progress" for current status
- entry_type: "finding" for key discoveries
- entry_type: "decision" for choices made

Example:
{
  "entry_type": "progress",
  "content": "Completed X, Y. Still need to do Z.",
  "references": ["result.json"]
}

Your notes will help the next execution continue from where you left off.`
	} else {
		checkpointMsg = `[Memory Checkpoint Required]
You have reached the limit of your working memory.
The oldest iterations are now invisible to you.

MANDATORY: You must write your notes now using the write_notes tool.
This is the ONLY tool available to you in this iteration.

Use write_notes to APPEND entries to your NOTES:
- entry_type: "progress" for current status and next steps
- entry_type: "finding" for key discoveries
- entry_type: "decision" for choices made and why
- entry_type: "observation" for important things noticed

Each entry is APPENDED, not overwritten. Include file references when relevant.

After writing notes, you will regain access to all tools.`
	}

	messagesWithCheckpoint := append(messages, llm.Message{
		Role:    "user",
		Content: checkpointMsg,
	})

	tl.weakWriteInteraction(iteration, model.InteractionTypeRequest, map[string]interface{}{
		"messages":      messagesWithCheckpoint,
		"is_checkpoint": true,
	})

	toolDefs := []llm.FunctionDefinition{tl.writeNotesTool.Schema()}
	response, err := tl.checkpointClient.ChatWithTools(ctx, messagesWithCheckpoint, toolDefs)
	if err != nil {
		applogger.Error("Notes iteration LLM error", "error", err)
		if isFinal {
			return &LoopResult{Status: "failure", Reason: "Task did not complete within max iterations"}
		}
		return &LoopResult{Status: "failure", Reason: fmt.Sprintf("Notes iteration LLM invocation failed: %s", err.Error())}
	}

	finishReason := response.FinishReason
	content := response.Content
	toolCalls := response.ToolCalls

	tl.weakWriteInteraction(iteration, model.InteractionTypeResponse, map[string]interface{}{
		"content":       content,
		"tool_calls":    toolCalls,
		"finish_reason": finishReason,
		"is_checkpoint": true,
	})

	if finishReason == "tool_calls" {
		var toolResults []llm.Message
		for _, tc := range toolCalls {
			toolCallID := tc.ID

			if tc.Function.Name != tools.ToolNameWriteNotes.String() {
				applogger.Error("Notes iteration: unexpected tool call", "tool", tc.Function.Name)
				toolResults = append(toolResults, llm.Message{
					Role:       "tool",
					ToolCallID: toolCallID,
					Content:    fmt.Sprintf("Error: tool '%s' is not available during notes iteration", tc.Function.Name),
				})
				continue
			}

			var args map[string]interface{}
			json.Unmarshal([]byte(tc.Function.Arguments), &args)

			applogger.Info("Notes iteration: executing write_notes")
			result, _ := tl.writeNotesTool.Execute(args)

			toolResults = append(toolResults, llm.Message{
				Role:       "tool",
				ToolCallID: toolCallID,
				Content:    result,
			})
		}

		tl.lastNotesIter = iteration
		tl.contextManager.RefreshNotes(tl.writeNotesTool.ReadNotes())

		assistantMsg := llm.Message{
			Role:      "assistant",
			Content:   content,
			ToolCalls: toolCalls,
		}

		tl.contextManager.AddIteration(assistantMsg, toolResults)
	}

	applogger.Info("Notes iteration completed", "iteration", iteration)

	if isFinal {
		return &LoopResult{Status: "failure", Reason: "Task did not complete within max iterations. Notes have been saved for next execution."}
	}

	return &LoopResult{Status: "success"}
}

// updateNotesOnStop updates notes when the agent decides to end the task
// (finish_reason=stop). The outcome may be completion, partial progress, or
// abandonment — the function does not prejudge. Uses the checkpoint client
// (lazy-initialized) with only write_notes tool available.
func (tl *TaskLoop) updateNotesOnStop(ctx context.Context, iteration int, finalContent string, messages []llm.Message) {
	if tl.writeNotesTool == nil {
		return
	}

	if tl.checkpointClient == nil {
		tl.checkpointClient = llm.NewChatModelWithTemperature(tl.llmConfig.BaseURL, tl.llmConfig.APIKey, tl.llmConfig.ModelID, llm.TemperatureCreative)
	}

	applogger.Info("Updating notes on task stop", "iteration", iteration)

	endMsg := `[Task Ended - Update Your Notes]
The task has ended. Record the final outcome in your notes.

Use write_notes to APPEND a summary entry reflecting what actually happened:

{
  "entry_type": "progress",
  "content": "Summary of what was accomplished, or why the task could not proceed further...",
  "references": ["file1.py", "file2.json"]
}

This will help you continue work if changes are requested later.`

	messagesWithUpdate := append(messages, llm.Message{
		Role:    "user",
		Content: endMsg,
	})

	tl.weakWriteInteraction(iteration, model.InteractionTypeRequest, map[string]interface{}{
		"messages": messagesWithUpdate,
		"is_final": true,
	})

	toolDefs := []llm.FunctionDefinition{tl.writeNotesTool.Schema()}
	response, err := tl.checkpointClient.ChatWithTools(ctx, messagesWithUpdate, toolDefs)
	if err != nil {
		applogger.Error("Notes update on stop failed", "error", err)
		return
	}

	finishReason := response.FinishReason
	content := response.Content
	toolCalls := response.ToolCalls

	tl.weakWriteInteraction(iteration, model.InteractionTypeResponse, map[string]interface{}{
		"content":       content,
		"tool_calls":    toolCalls,
		"finish_reason": finishReason,
		"is_final":      true,
	})

	if finishReason == "tool_calls" {
		for _, tc := range toolCalls {
			if tc.Function.Name != tools.ToolNameWriteNotes.String() {
				continue
			}
			var args map[string]interface{}
			json.Unmarshal([]byte(tc.Function.Arguments), &args)
			tl.writeNotesTool.Execute(args)
		}

		tl.contextManager.RefreshNotes(tl.writeNotesTool.ReadNotes())
	}

	applogger.Info("Notes updated on task stop")
}

// runCycleBlockedCheckpoint handles the forced checkpoint triggered by cycle detection.
//
// When a tool's CycleDetect returns Blocked=true, the task loop enters this
// special checkpoint mode instead of continuing normal execution. Only
// write_notes is available. The agent is asked to reflect on why it was
// blocked and record its findings, then the work terminates with failure.
//
// Unlike regular checkpoints (which continue the loop), a cycle-blocked
// checkpoint always ends the work — the agent cannot retry the same
// approach that caused the cycle.
func (tl *TaskLoop) runCycleBlockedCheckpoint(ctx context.Context, iteration int, reason string) *LoopResult {
	applogger.Info("Running cycle-blocked checkpoint",
		"iteration", iteration,
		"reason", reason,
	)

	if tl.writeNotesTool == nil {
		return &LoopResult{Status: "failure", Reason: reason}
	}

	if tl.checkpointClient == nil {
		tl.checkpointClient = llm.NewChatModelWithTemperature(tl.llmConfig.BaseURL, tl.llmConfig.APIKey, tl.llmConfig.ModelID, llm.TemperatureCreative)
	}

	messages := tl.contextManager.BuildMessages()

	checkpointMsg := fmt.Sprintf(`[Cycle Detection - Forced Checkpoint]
You have been repeating the same actions without making progress.
The system detected a cycle: %s

You must stop and reflect on what happened:
1. What were you trying to do?
2. Why did it keep failing or returning the same result?
3. What alternative approaches could work?

Use write_notes to record your reflection. This is the ONLY tool available.
The task will end after you save your notes.`, reason)

	messagesWithCheckpoint := append(messages, llm.Message{
		Role:    "user",
		Content: checkpointMsg,
	})

	tl.weakWriteInteraction(iteration, model.InteractionTypeRequest, map[string]interface{}{
		"messages":      messagesWithCheckpoint,
		"is_checkpoint": true,
		"cycle_blocked": true,
	})

	toolDefs := []llm.FunctionDefinition{tl.writeNotesTool.Schema()}
	response, err := tl.checkpointClient.ChatWithTools(ctx, messagesWithCheckpoint, toolDefs)
	if err != nil {
		applogger.Error("Cycle-blocked checkpoint LLM error", "error", err)
		return &LoopResult{Status: "failure", Reason: reason}
	}

	finishReason := response.FinishReason
	content := response.Content
	toolCalls := response.ToolCalls

	tl.weakWriteInteraction(iteration, model.InteractionTypeResponse, map[string]interface{}{
		"content":       content,
		"tool_calls":    toolCalls,
		"finish_reason": finishReason,
		"is_checkpoint": true,
		"cycle_blocked": true,
	})

	if finishReason == "tool_calls" {
		for _, tc := range toolCalls {
			if tc.Function.Name != tools.ToolNameWriteNotes.String() {
				continue
			}
			var args map[string]interface{}
			json.Unmarshal([]byte(tc.Function.Arguments), &args)
			tl.writeNotesTool.Execute(args)
		}
		tl.contextManager.RefreshNotes(tl.writeNotesTool.ReadNotes())
	}

	applogger.Info("Cycle-blocked checkpoint completed", "iteration", iteration)

	return &LoopResult{
		Status: "failure",
		Reason: fmt.Sprintf("Task terminated due to detected cycle: %s. Notes have been saved for next execution.", reason),
	}
}

// invokeLLM calls the LLM with the current messages and all registered tools.
// Converts internal message format and binds tool schemas.
func (tl *TaskLoop) invokeLLM(ctx context.Context, messages []llm.Message) (llm.ToolResponse, error) {
	applogger.Debug("TaskLoop invoking LLM",
		"message_count", len(messages),
	)

	toolDefs := make([]llm.FunctionDefinition, 0, len(tl.toolRegistry))
	for _, t := range tl.toolRegistry {
		toolDefs = append(toolDefs, t.Schema())
	}
	return tl.llmClient.ChatWithTools(ctx, messages, toolDefs)
}

// executeToolCall executes a single tool call and returns the result.
// Looks up the tool in the registry, parses arguments, and calls Execute.
// Returns error messages for unknown tools or invalid arguments.
func (tl *TaskLoop) executeToolCall(tc llm.ToolCall) llm.Message {
	toolCallID := tc.ID
	toolName := tc.Function.Name
	argsStr := tc.Function.Arguments

	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsStr), &args); err != nil {
		return llm.Message{
			Role:       "tool",
			ToolCallID: toolCallID,
			Content:    fmt.Sprintf("Error: invalid arguments format - %s", err.Error()),
		}
	}

	tool, ok := tl.toolRegistry[toolName]
	if !ok {
		return llm.Message{
			Role:       "tool",
			ToolCallID: toolCallID,
			Content:    fmt.Sprintf("Error: unknown tool '%s'", toolName),
		}
	}

	applogger.Info("Executing tool", "tool", toolName)

	result, err := tool.Execute(args)
	if err != nil {
		applogger.Error("Tool execution error", "tool", toolName, "error", err)
		result = fmt.Sprintf("Error executing tool '%s': %s", toolName, err.Error())
	}

	// Byte-level fallback: if a tool forgot to self-truncate and its output is
	// abnormally large, discard the content entirely and notify. Do not attempt
	// semantic truncation here — the tool is responsible for that before marshalling.
	if len(result) > hardOutputLimit {
		applogger.Error("Tool output exceeds hard limit, discarded",
			"tool", toolName, "bytes", len(result))
		result = fmt.Sprintf(
			"[OUTPUT TOO LARGE: %d bytes. This tool did not truncate its own output. "+
				"Use a more targeted command or smaller scope.]", len(result))
	}

	// Cycle detection: check for cyclical patterns after each tool call.
	// The tool manages its own detection state and defines its own "sameness" semantics.
	cs := tool.CycleDetect(args, result)
	if cs.Warning != "" {
		result += "\n" + cs.Warning
	}
	if cs.Blocked {
		tl.forceCheckpoint = true
		tl.blockReason = cs.Reason
		applogger.Info("Cycle detection: forced checkpoint triggered",
			"tool", toolName,
			"reason", cs.Reason,
		)
	}

	return llm.Message{
		Role:       "tool",
		ToolCallID: toolCallID,
		Content:    result,
	}
}

// weakWriteInteraction writes an interaction record to the database.
// Silently skips if session is not configured.
// Records are grouped by (session_id, work_id, iteration)
// to support both frontend display and debugging.
func (tl *TaskLoop) weakWriteInteraction(iteration, interactionType int, data map[string]interface{}) {
	if tl.sessionID == 0 {
		return
	}

	dataJSON, _ := json.Marshal(data)
	record := model.Interaction{
		SessionID: tl.sessionID,
		WorkID:    tl.workID,
		Iteration: iteration,
		Type:      interactionType,
		Data:      string(dataJSON),
	}
	if err := database.DB.Create(&record).Error; err != nil {
		applogger.Error("Failed to write interaction record", "error", err)
	}
}
