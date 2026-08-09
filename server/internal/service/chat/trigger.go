package chat

import "qingqiu-world-server/internal/model"

// TriggerType identifies the trigger source that caused the chat pipeline to run.
type TriggerType int

const (
	// TriggerNone indicates no specific trigger — the agent is initiating
	// a conversation autonomously (e.g., heartbeat-triggered Chat in a new session).
	TriggerNone TriggerType = iota
	// TriggerMessage indicates the pipeline was triggered by a normal chat message.
	TriggerMessage
	// TriggerAlarm indicates the pipeline was triggered by a scheduled alarm firing.
	TriggerAlarm
)

// Trigger unifies the trigger source for the chat pipeline, replacing the
// former split between triggerMessage (a raw DB row) and triggerOverride
// (a supplementary struct). The Type field determines which sub-struct is
// populated; callers build the Trigger with the information they have, and
// loadMessages fills in DB-loaded data (original message content, etc.).
type Trigger struct {
	Type    TriggerType
	Message *model.Message    // non-nil for TriggerMessage
	Alarm   *TriggerAlarmData // non-nil for TriggerAlarm
}

// TriggerAlarmData carries the alarm notification information.
// The alarm notification is injected into the prompt as an independent
// system-level section, NOT attributed to any dialog participant.
type TriggerAlarmData struct {
	SelfReminder    string // Agent's self-reminder text (set by the alarm)
	OriginalMessage string // Original user message text for reference (loaded by loadMessages)
}
