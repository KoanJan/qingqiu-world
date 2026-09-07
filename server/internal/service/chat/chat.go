// Package chat implements the core chat processing pipeline.
//
// This package is designed as a package-level service: use the Process()
// function directly. No struct instances need to be created or passed around.
//
// The pipeline includes:
//   - User state inference (including needs_world_interaction detection)
//   - Query preprocessing (routing, clarification, RAG optimization)
//   - Agent execution for world-interaction requests
//   - Context engineering (summary, retrieval, assembly)
//   - LLM streaming responses
//   - Summary generation triggers
//
// Draft-based architecture:
// The pipeline does NOT write to the messages table directly. It returns all
// results through ChatResult, and the caller (Work) commits them to a draft
// and then to messages atomically. This eliminates the placeholder message pattern.
package chat

import (
	"context"
	"fmt"

	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/agent"
	comprehendTypes "qingqiu-world-server/internal/service/comprehend/types"
	"qingqiu-world-server/internal/service/task"
)

// User-friendly error message for unexpected failures
const userFriendlyErrorMessage = "Sorry, something went wrong on the server. Please try again later."

// ChatResult holds the output of the chat processing pipeline.
// In the draft-based architecture, the pipeline does not write to the messages
// table directly. Instead, it returns all results through this struct, and the
// caller (Work) commits them to a draft and then to messages atomically.
type ChatResult struct {
	Content string // The generated response content
}

// ExecuteChat handles the chat execution path.
//
// After the cognitive order refactoring (0.1.4), background context from the
// Comprehend phase is passed in via ChatContext. All ChatContext fields are
// optional; guidance is the only required parameter for the agent's intent.
//
// Parameters:
//   - aiPersonID: the agent's person ID, used to fetch agent config via the
//     agent cache at point of use.
//   - guidance: execution intent from ChatPlan.Guidance — always present.
//   - trigger: trigger source that caused this pipeline run.
//     TriggerNone for autonomous (heartbeat) Chat actions.
//   - chatCtx: optional background context (nil when neither Comprehend nor
//     task result provides it).
func ExecuteChat(
	ctx context.Context,
	session *model.Session,
	aiPersonID int64,
	readMessageRange [2]int64,
	trigger *Trigger,
	guidance string,
	chatCtx *ChatContext,
) (*ChatResult, error) {

	p := &pipeline{
		session:          session,
		aiPersonID:       aiPersonID,
		readMessageRange: readMessageRange,
		trigger:          trigger,
		guidance:         guidance,
	}

	// Resolve agent name for identity anchoring in chat generation.
	if a, err := agent.GetAgent(aiPersonID); err == nil {
		p.selfName = a.Name
	}

	if chatCtx != nil {
		p.personStateResult = chatCtx.PersonState
		p.historySegments = chatCtx.HistorySegments
		p.kbSegments = chatCtx.KBSegments
		p.needsClarification = chatCtx.NeedsClarification
		p.clarification = chatCtx.Clarification

		if chatCtx.TaskResult != nil {
			p.taskResult = &TaskResultForAssembly{
				Status: chatCtx.TaskResult.Status,
			}
			if chatCtx.TaskResult.Output != "" {
				p.taskResult.Result = chatCtx.TaskResult.Output
			}
			if chatCtx.TaskResult.Error != "" {
				p.taskResult.Reason = chatCtx.TaskResult.Error
			}
			if chatCtx.TaskResult.Notes != "" {
				p.taskResult.Notes = chatCtx.TaskResult.Notes
			}
		}
	}

	if err := p.loadMessages(); err != nil {
		return &ChatResult{Content: userFriendlyErrorMessage}, err
	}

	// Skip preprocessing, inference, KB retrieval, and agent execution —
	// all of these were done in the Comprehend phase.
	// Go directly to context assembly and response.

	messages, earlyContent, earlyReturn := p.assembleContext(ctx)
	if earlyReturn {
		return &ChatResult{Content: earlyContent}, nil
	}

	fullContent, err := p.streamResponse(ctx, messages)
	if err != nil {
		return &ChatResult{Content: fullContent}, err
	}

	p.postProcess(ctx)

	return &ChatResult{
		Content: fullContent,
	}, nil
}

// ChatContext carries optional background information for the chat pipeline.
// All fields are optional — only guidance (passed as a separate parameter) is
// semantically required. The caller populates different subsets depending on
// the chat trigger path:
//   - External events: PersonState, HistorySegments, KBSegments, NeedsClarification, Clarification
//   - Task completion: TaskResult set alongside external event fields
//   - Heartbeat (autonomous): nil (guidance alone drives the chat)
type ChatContext struct {
	// PersonState is the inferred state of the other participant.
	PersonState *comprehendTypes.PersonState
	// HistorySegments are RAG-retrieved chat history fragments.
	HistorySegments []comprehendTypes.Segment
	// KBSegments are RAG-retrieved knowledge base fragments.
	KBSegments []comprehendTypes.Segment
	// NeedsClarification indicates the agent needs to ask a clarifying question.
	NeedsClarification bool
	// Clarification is the clarifying question text.
	Clarification string
	// TaskResult carries the result of a completed TaskWork.
	TaskResult *task.TaskResult
}

// formatAlarmNotification builds the alarm notification prompt section from
// the Trigger data. Returns empty string when the trigger is not TriggerAlarm
// or the Alarm sub-struct is nil.
func formatAlarmNotification(t *Trigger) string {
	if t == nil || t.Type != TriggerAlarm || t.Alarm == nil {
		return ""
	}
	ref := ""
	if t.Alarm.OriginalMessage != "" {
		ref = fmt.Sprintf("\n\nOriginal message for reference: %s", t.Alarm.OriginalMessage)
	}
	return fmt.Sprintf(
		"[ALARM NOTIFICATION] An alarm you set has just triggered. This is NOT a new request — you set this alarm yourself earlier. Take action now based on your self-reminder below.\n\nYour self-reminder: %s%s",
		t.Alarm.SelfReminder,
		ref,
	)
}
