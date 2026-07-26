// Package taskcontext manages the agent's internal message history within a task execution.
//
// This package implements a dynamic expanding window architecture for the task
// loop's context management, optimized for LLM prefix caching:
//
//  1. system: pure static rules (stable across all iterations and task sessions)
//  2. dynamic iterations: expanding window with bulk shrink — new iterations
//     append to the end, anchor stays fixed across expansions, only shrinks
//     in large batches when the window exceeds 2x the base size
//  3. last user message: directive history + context info (workspace paths,
//     iteration stats) + tool list + notes — all dynamic content at the end
//
// This ordering means system + most iterations hit the prefix cache between
// bulk shrinks (only the new iteration appended each round), and all
// per-iteration dynamic content is at the tail where it cannot break the
// cached prefix.
package taskcontext

import (
	"fmt"
	"runtime"
	"strings"
	"time"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/service/llm"
)

// ContextManager manages the internal message history for a single task execution.
type ContextManager struct {
	systemPrompt       string // Pure static system prompt (rules only, no dynamic info)
	minIterationWindow int    // Minimum visible iterations (anchor size after shrink)
	maxIterationWindow int    // Maximum visible iterations (triggers bulk-shrink when exceeded)
	workspaceDir       string // Session workspace directory
	outputDir          string // Default output directory
	toolList           string // Tool descriptions string
	notesContent       string // Full content of agent's notes
	totalIterations    int    // Total iterations accumulated
	guidanceHistory    string // Accumulated directive history with timestamps

	// dynamicMessages stores groups of (assistant_msg + tool_results) per iteration.
	// Each group is a unit — always kept together or dropped together.
	dynamicMessages [][]llm.Message
}

// NewContextManager creates a new ContextManager.
//
// systemPrompt is the pure static rules block (no directives, no context info,
// no workspace paths, no tool list — those are injected at the tail by BuildMessages).
// toolList is the formatted tool descriptions string.
func NewContextManager(systemPrompt string, minWindow, maxWindow int, notesContent string, workspaceDir, outputDir, toolList string) *ContextManager {
	return &ContextManager{
		systemPrompt:       systemPrompt,
		minIterationWindow: minWindow,
		maxIterationWindow: maxWindow,
		notesContent:       notesContent,
		workspaceDir:       workspaceDir,
		outputDir:          outputDir,
		toolList:           toolList,
	}
}

// IterationWindow returns the min iteration window size (used for checkpoint frequency).
func (cm *ContextManager) IterationWindow() int {
	return cm.minIterationWindow
}

// RefreshNotes updates notes content (agent may have appended via write_notes tool).
func (cm *ContextManager) RefreshNotes(newNotesContent string) {
	cm.notesContent = newNotesContent
}

// AddGuidance records a directive change with timestamp into the guidance history.
// The history persists in the last user message across all iterations — unlike
// dynamic iteration messages, it is never dropped by the window.
func (cm *ContextManager) AddGuidance(guidance, reason string) {
	entry := fmt.Sprintf("[%s] Directive: %s", time.Now().Format("2006-01-02 15:04:05"), guidance)
	if reason != "" {
		entry += fmt.Sprintf("\n           Reason: %s", reason)
	}
	if cm.guidanceHistory != "" {
		cm.guidanceHistory += "\n\n"
	}
	cm.guidanceHistory += entry
}

// AddIteration adds a complete iteration (assistant message + tool results).
//
// An iteration is a group of messages that must be kept together
// to maintain conversation coherence. The assistant message and
// its associated tool results are always included or excluded as a unit.
func (cm *ContextManager) AddIteration(assistantMsg llm.Message, toolResults []llm.Message) {
	group := []llm.Message{assistantMsg}
	group = append(group, toolResults...)
	cm.dynamicMessages = append(cm.dynamicMessages, group)
	cm.totalIterations++
}

// BuildMessages assembles the final message list for LLM call.
//
// Order (optimized for prefix caching):
//  1. system: pure static rules — stable across all iterations
//  2. dynamic iterations: expanding window with bulk-shrink
//  3. user: [Directive History] + [Context Information] + [Tool List] + [Your Notes]
//
// The window expands from 0 up to maxIterationWindow. When it exceeds that,
// the oldest iterations are dropped in bulk down to minIterationWindow, forming
// a new anchor. Between bulk-shrinks, only the last iteration changes — all
// previous iterations hit the prefix cache.
func (cm *ContextManager) BuildMessages() []llm.Message {
	total := len(cm.dynamicMessages)

	var visible [][]llm.Message
	if total > cm.maxIterationWindow {
		// Bulk shrink: drop the oldest iterations, keeping only the
		// last minIterationWindow. This forms a new anchor that stays
		// fixed across the next minIterationWindow expansions.
		dropCount := total - cm.minIterationWindow
		visible = cm.dynamicMessages[dropCount:]
	} else {
		// Expanding phase: keep everything, anchor stays fixed.
		visible = cm.dynamicMessages
	}

	invisibleIterations := cm.totalIterations - len(visible)

	// 1. Pure static system prompt
	messages := []llm.Message{
		{Role: "system", Content: cm.systemPrompt},
	}

	// 2. Dynamic iterations (cache hits between bulk shrinks)
	for _, group := range visible {
		for _, msg := range group {
			messages = append(messages, msg)
		}
	}

	// 3. Last user message: everything dynamic goes here
	messages = append(messages, llm.Message{
		Role:    "user",
		Content: cm.buildLastMessage(len(visible), invisibleIterations),
	})

	return messages
}

// buildLastMessage assembles the trailing user message containing all
// per-iteration dynamic content: directive history, context info, tool list,
// and notes. Placed at the end so it cannot break the prefix cache.
func (cm *ContextManager) buildLastMessage(visibleCount, invisibleIterations int) string {
	settings := config.Get()
	notesMaxChars := settings.NotesMaxChars
	notesLength := len(cm.notesContent)

	var parts []string

	// Directive History — accumulated across the entire task, never dropped
	if cm.guidanceHistory != "" {
		parts = append(parts,
			"[Directive History]",
			cm.guidanceHistory,
			"",
		)
	}

	parts = append(parts,
		"[Context Information]",
		fmt.Sprintf("Your session directory is: %s", cm.workspaceDir),
		fmt.Sprintf("Your default working directory is: %s", cm.outputDir),
		fmt.Sprintf("Operating system: %s", runtime.GOOS),
		"",
		fmt.Sprintf("This task has produced %d iterations total. Only the last %d are visible to you, %d earlier iterations are outside your visible range.", cm.totalIterations, visibleCount, invisibleIterations),
		fmt.Sprintf("Your NOTES are currently %d chars (max: %d chars).", notesLength, notesMaxChars),
	)

	if notesLength > int(float64(notesMaxChars)*0.8) {
		parts = append(parts, "WARNING: Your NOTES are approaching the size limit. Consider consolidating older entries.")
	}

	parts = append(parts,
		"",
		cm.toolList,
		"",
		"[Your Notes]",
		cm.notesContent,
	)

	return strings.Join(parts, "\n")
}
