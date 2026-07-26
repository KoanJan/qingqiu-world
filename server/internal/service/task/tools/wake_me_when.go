package tools

import (
	"fmt"
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/eventqueue"
	"qingqiu-world-server/internal/service/llm"

	applogger "qingqiu-world-server/internal/logger"
)

// triggerAtFormat is the only accepted time format for trigger_at.
// Uses server local time without timezone — the agent and server share
// the same timezone context.
const triggerAtFormat = "2006-01-02 15:04:05"

// WakeMeWhenTool allows the agent to set a future alarm that will wake it up
// at a specified time. This is NOT an OS-level cron/scheduled task — it is
// the agent's self-wake mechanism.
//
// The tool's sole responsibility is:
//  1. Create a ScheduledEvent DB record (status=Pending)
//  2. Send an EventTypeAlarmCreated event through eventqueue
//
// Goroutine management (waiting until trigger_at, firing) is handled by the
// runtime package, which receives the EventTypeAlarmCreated event and registers
// a goroutine. This separation keeps the tool layer thin and avoids circular
// dependencies (runtime → task → tools).
type WakeMeWhenTool struct {
	personID         int64
	sessionID        int64
	triggerMessageID int64 // The user message that triggered this tool call
	CycleDetector          // Embedded: cycle detection on (args, result) pairs
}

// NewWakeMeWhenTool creates a WakeMeWhenTool for the given person, session,
// and the user message that triggered this tool call.
func NewWakeMeWhenTool(personID, sessionID, triggerMessageID int64) *WakeMeWhenTool {
	return &WakeMeWhenTool{
		personID:         personID,
		sessionID:        sessionID,
		triggerMessageID: triggerMessageID,
	}
}

// Name returns the tool name.
func (w *WakeMeWhenTool) Name() ToolName { return ToolNameWakeMeWhen }

// Description returns a brief description of the tool.
func (w *WakeMeWhenTool) Description() string {
	return "Set an alarm to wake yourself at a future time (e.g., reminders, follow-ups)"
}

// Schema returns the LLM function definition for the tool.
func (w *WakeMeWhenTool) Schema() llm.FunctionDefinition {
	return llm.FunctionDefinition{
		Name: w.Name().String(),
		Description: "Set an alarm to wake yourself up at a future time. " +
			"When the alarm fires, you will receive a notification with the context you provide. " +
			"This is YOUR self-wake mechanism — it does NOT create OS-level notifications or system alerts. " +
			"Use this when someone asks you to remind them or follow up at a specific time. " +
			"\n\n" +
			"CRITICAL: The 'message' parameter is an ACTION INSTRUCTION to your future self, " +
			"NOT a description of what happened. When the alarm fires, you will see this message " +
			"as your primary directive — so write it as a command telling yourself what to DO, " +
			"not as a memo describing why the alarm was set. " +
			"\n\n" +
			"BAD: 'Someone asked to be reminded about the 3pm meeting.' (descriptive — sounds like they are asking again) " +
			"\n" +
			"GOOD: 'Tell the person: it is 3pm, time for the meeting with the design team in Conference Room B.' (actionable — tells you what to say)" +
			"\n\n" +
			"FAST PATH: If the alarm only needs to send a simple message (like a reminder), " +
			"set action='send_message' and provide the exact message in action_content. This skips LLM " +
			"processing entirely and delivers the message instantly when the alarm fires. " +
			"Only use action='full_pipeline' when you need to perform complex actions (web search, " +
			"tool usage, multi-step tasks) when the alarm fires.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"trigger_at": map[string]interface{}{
					"type":        "string",
					"description": "Absolute time to wake you, in the exact format 'YYYY-MM-DD HH:MM:SS' (server local time). Must be a future time. Example: '2026-06-09 23:10:00'. Compute the exact future time based on the current time shown in recent messages.",
				},
				"message": map[string]interface{}{
					"type":        "string",
					"description": "Action instruction for your future self when the alarm fires. Write as a COMMAND telling yourself exactly what to DO and SAY. Example: 'Tell the person: you asked me to remind you about the 3pm meeting. It is now time — the meeting is in Conference Room B.' DO NOT write descriptive memos like 'Someone requested a reminder.' This field is always required as a fallback, even when using send_message action.",
				},
				"action": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"send_message", "full_pipeline"},
					"description": "How to handle the alarm when it fires. 'send_message': instantly send action_content without any LLM processing (fast path, best for simple reminders). 'full_pipeline': go through the full LLM pipeline (needed for complex actions like web searches or tool usage). Default is 'full_pipeline' if omitted.",
				},
				"action_content": map[string]interface{}{
					"type":        "string",
					"description": "The exact message to send when the alarm fires. Only used when action='send_message'. This message is delivered instantly without any LLM processing, so write it as the final message that will be seen. Example: '⏰ Reminder: it is 3pm, time for the meeting with the design team in Conference Room B.'",
				},
			},
			"required": []string{"trigger_at", "message"},
		},
	}
}

// Execute creates a ScheduledEvent DB record and sends an EventTypeAlarmCreated
// event through eventqueue. The runtime receives the event and registers a
// goroutine to wait until trigger_at.
func (w *WakeMeWhenTool) Execute(args map[string]interface{}) (string, error) {
	triggerAtStr, _ := args["trigger_at"].(string)
	message, _ := args["message"].(string)

	if triggerAtStr == "" || message == "" {
		return "", fmt.Errorf("trigger_at and message are required")
	}

	// Parse trigger_at
	triggerAt, err := parseTriggerAt(triggerAtStr)
	if err != nil {
		return "", fmt.Errorf("invalid trigger_at format '%s': %v", triggerAtStr, err)
	}

	// Validate: trigger time must be in the future
	if triggerAt.Before(time.Now()) {
		return "", fmt.Errorf("trigger_at '%s' is in the past", triggerAtStr)
	}

	// Parse action and action_content
	action := model.ScheduledEventActionFullPipeline
	actionStr, _ := args["action"].(string)
	if actionStr == "send_message" {
		action = model.ScheduledEventActionSendMessage
	}
	actionContent, _ := args["action_content"].(string)

	// Validate: send_message action requires action_content
	if action == model.ScheduledEventActionSendMessage && actionContent == "" {
		return "", fmt.Errorf("action_content is required when action is 'send_message'")
	}

	// Create a DB record for persistence and debugging
	event := model.ScheduledEvent{
		PersonID:         w.personID,
		SessionID:        w.sessionID,
		TriggerMessageID: w.triggerMessageID,
		TriggerAt:        triggerAt,
		Message:          message,
		Action:           action,
		ActionContent:    actionContent,
		Status:           model.ScheduledEventStatusPending,
	}
	if err := database.DB.Create(&event).Error; err != nil {
		applogger.Error("Failed to create scheduled event record",
			"person_id", w.personID,
			"session_id", w.sessionID,
			"error", err,
		)
		return "", fmt.Errorf("failed to create alarm")
	}

	// Bridge: resolve agentConfigID from personID for eventqueue routing.
	// The event queue is keyed by agentConfigID, but the tool only has personID.
	var ac model.AgentConfig
	if err := database.DB.Where("person_id = ?", w.personID).First(&ac).Error; err != nil {
		applogger.Error("Failed to resolve agent config for eventqueue routing",
			"person_id", w.personID, "error", err,
		)
		return "", fmt.Errorf("failed to notify runtime of alarm")
	}

	eventqueue.SendEvent(ac.ID, &eventqueue.AgentEvent{
		Type:      eventqueue.EventTypeAlarmCreated,
		SessionID: w.sessionID,
		Payload: &eventqueue.AlarmCreatedPayload{
			ScheduledEventID: event.ID,
		},
	})

	until := time.Until(triggerAt).Round(time.Minute)
	if action == model.ScheduledEventActionSendMessage {
		return fmt.Sprintf("Alarm set (fast path: will send message directly). I will be woken at %s (in %s).",
			triggerAt.Format("2006-01-02 15:04 MST"), until), nil
	}
	return fmt.Sprintf("Alarm set. I will be woken at %s (in %s).",
		triggerAt.Format("2006-01-02 15:04 MST"), until), nil
}

// parseTriggerAt parses the trigger_at string into a time.Time.
// Only one format is accepted: "YYYY-MM-DD HH:MM:SS" (server local time).
func parseTriggerAt(s string) (time.Time, error) {
	t, err := time.ParseInLocation(triggerAtFormat, s, time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid time format (expected 'YYYY-MM-DD HH:MM:SS', e.g. '2026-06-09 23:10:00'): %s", s)
	}
	return t, nil
}
