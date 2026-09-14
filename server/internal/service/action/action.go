// Package action defines the Action types and Plan structures used across
// the Decide→Execute pipeline. It is a shared package imported by both
// runtime (which produces and executes Actions) and eventqueue (which
// carries Action context through events).
package action

import (
	"qingqiu-world-server/internal/service/focusedwork"
)

// ActionType represents the type of action the Decide phase concludes.
//
// Actions are divided into two categories:
//   - Self-contained actions: Chat, CreateAlarm, UpdateBio — the Action
//     itself is a complete description of what to do; no Work iteration is
//     required.
//   - Focus-oriented actions: StartFocusedWork, RouteFocusedWork, CancelFocusedWork — these
//     operate on FocusedLoops that run a multi-step ReAct loop.
type ActionType int

const (
	// Chat sends a chat message to another Person. No iteration loop.
	Chat ActionType = iota
	// StartFocusedWork starts a new multi-step FocusedWork. It enters a
	// FocusedLoop using tools (search, file operations, etc.).
	StartFocusedWork
	// RouteFocusedWork routes the current event to an existing active work as
	// a new directive or constraint.
	RouteFocusedWork
	// CancelFocusedWork requests an existing active work to stop and wrap up.
	CancelFocusedWork
	// CreateAlarm creates a scheduled alarm directly, without entering
	// FocusedLoop. This is a self-contained world action.
	CreateAlarm
	// UpdateBio updates the agent's own Bio field — a self-reflective action.
	// Available during heartbeat. No iteration loop.
	UpdateBio

	// EnterPrivateSpace enters the agent's private space — a personal, persistent
	// directory space. No plan struct needed; Background and Reason together
	// serve as the Thoughts payload expressing what the agent wants to do there.
	// Available during heartbeat. Runs a lightweight ReAct loop.
	EnterPrivateSpace

	// InspectJinshu reads the contents of a received jinshu through a dedicated
	// lightweight loop (read + summarize tools only). It is separate from the
	// full FocusedLoop — reading one's own delivery is perception, not focused work.
	InspectJinshu

	// ListReceivedJinshu lists the agent's received jinshu through a paginated keyword
	// search, so the agent can locate a jinshu_id before inspecting it.
	ListReceivedJinshu

	// SendJinshu sends selected resources from the agent's Agent Owned Space to
	// another person as a jinshu. This is the Decide-level "share a deliverable"
	// action, distinct from EnterPrivateSpace (which is for working inside private/).
	// Available during heartbeat. No iteration loop — direct delivery.
	SendJinshu

	// ListSentJinshu lists the agent's sent jinshu through a paginated keyword
	// search, mirroring ListReceivedJinshu but over outbound deliveries instead of
	// inbound ones. Available during external events only.
	ListSentJinshu

	// InspectOwnedSpace lists bounded filesystem metadata from Agent Owned Space.
	InspectOwnedSpace
)

// WorkPlan describes a FocusedWork to be created via StartFocusedWork action.
// It carries Guidance (the execution intent) so the focused work knows what to do
// without re-interpreting the event. Background and Reason have been lifted
// to the Action level.
type WorkPlan struct {
	Guidance string                `json:"guidance" jsonschema:"description=Your internal intention, written in first-person as your own thought: what you plan to execute. Write as if you are thinking to yourself.,required"`
	Metadata *focusedwork.Metadata `json:"-"` // System-generated traceability info, not written by LLM
}

// JinshuPlan describes a received jinshu the agent wants to read via the
// InspectJinshu action. The dedicated jinshu-read loop uses Guidance as the
// reading intent.
type JinshuPlan struct {
	JinshuID int64  `json:"jinshu_id" jsonschema:"description=ID of the received jinshu to read,required"`
	Guidance string `json:"guidance" jsonschema:"description=Your internal intention: what you want to understand from this jinshu. Written in first-person.,required"`
}

// ListReceivedJinshuParams describes a paginated keyword search over the agent's received
// jinshu, produced by the ListReceivedJinshu action.
type ListReceivedJinshuParams struct {
	Query string `json:"query,omitempty" jsonschema:"description=Optional keyword to filter jinshu by topic or description. Omit or empty to list all."`
	Page  int    `json:"page" jsonschema:"description=Page number (1-based),required"`
	Limit int    `json:"limit" jsonschema:"description=Results per page (1-50),required"`
}

// ListSentJinshuParams describes a paginated keyword search over the agent's sent
// jinshu, produced by the ListSentJinshu action.
type ListSentJinshuParams struct {
	Query string `json:"query,omitempty" jsonschema:"description=Optional keyword to filter jinshu by topic or description. Omit or empty to list all."`
	Page  int    `json:"page" jsonschema:"description=Page number (1-based),required"`
	Limit int    `json:"limit" jsonschema:"description=Results per page (1-50),required"`
}

// SendJinshuPlan describes Agent Owned Space resources the agent wants to
// send to another person as a jinshu, produced by the SendJinshu action.
type SendJinshuPlan struct {
	ToPersonID  int64    `json:"to_person_id" jsonschema:"description=ID of the recipient person,required"`
	Topic       string   `json:"topic" jsonschema:"description=Short subject/topic for the jinshu,required"`
	Description string   `json:"description,omitempty" jsonschema:"description=Optional note describing what is being sent and why"`
	Paths       []string `json:"paths" jsonschema:"description=List of AOS file or directory locators. Use work/<session_id>/... or private/...; bare paths remain relative to private/.,required"`
}

// OwnedSpaceInspectionPlan is a bounded metadata-only AOS inspection request.
type OwnedSpaceInspectionPlan struct {
	Scope string `json:"scope" jsonschema:"description=One of work, private, root, or work/<session_id>. Default is root."`
	Query string `json:"query,omitempty" jsonschema:"description=Optional plain substring filter for path names; shell and glob syntax are not supported."`
	Limit int    `json:"limit" jsonschema:"description=Maximum result count from 1 to 50,required"`
}

// ChatPlan describes a chat message delivery via the Chat action.
// The Chat action is self-contained — it does not create a Work.
//
// SessionID and RecipientPersonID encode the delivery target:
//   - SessionID > 0: send to the specified existing session.
//   - SessionID == -1: create a new 1v1 session with RecipientPersonID.
//   - SessionID == 0 is always invalid — it is the Go zero value and
//     indistinguishable from a missing field in the LLM's JSON output.
type ChatPlan struct {
	Guidance          string `json:"guidance" jsonschema:"description=Your internal intention, written in first-person as your own thought: what you plan to say. Write as if you are thinking to yourself.,required"`
	SessionID         int64  `json:"session_id,omitempty" jsonschema:"description=Target session ID. Use a positive session ID from your sessions list to send to an existing session. Use -1 to create a new 1v1 session with recipient_person_id. 0 is invalid — always provide a real session ID or -1."`
	RecipientPersonID int64  `json:"recipient_person_id,omitempty" jsonschema:"description=When session_id is -1: the person ID to start a new 1v1 conversation with."`

	// Content carries a pre-computed message for the fast path (scheduled
	// events with action=send_message). It is system-generated by the
	// rule-based Decide branch, never produced by the LLM, so it is excluded
	// from the JSON schema.
	Content string `json:"-"`
}

// UseNewSession reports whether this plan requests creating a new 1v1 session
// (as opposed to sending to an existing session identified by a positive ID).
func (p *ChatPlan) UseNewSession() bool { return p.SessionID < 0 }

// WorkGuidance describes a directive to be sent to an existing active work.
// It is the payload for route and cancel actions — the symmetric counterpart
// to WorkPlan (which is the payload for create actions).
//
// Guidance: the executable directive (what the target work should do).
// Reason has been lifted to the Action level.
type WorkGuidance struct {
	TargetWorkID int64  `json:"target_work_id" jsonschema:"description=The ID of the active work this directive targets"`
	Guidance     string `json:"guidance" jsonschema:"description=What I want the target work to do now. Written in first-person as my own intention.,required"`
}

// AlarmPlan describes a self-wake alarm to be created as a top-level Action.
//
// The fields mirror the former wake_me_when tool's arguments exactly — this
// is a path migration (tool → action), not a redesign. The LLM produces the
// same inputs; the runtime executes the same logic (create ScheduledEvent
// record, send AlarmCreated event, register waiting goroutine).
type AlarmPlan struct {
	TriggerAt     string `json:"trigger_at" jsonschema:"description=Absolute time to wake yourself, in the exact format 'YYYY-MM-DD HH:MM:SS' (server local time). Must be a future time. Example: '2026-06-09 23:10:00'. Compute the exact future time based on the current time shown in the context.,required"`
	Message       string `json:"message" jsonschema:"description=Instruction for your future self when the alarm fires — what you should DO. Write in first person as your own note to yourself (e.g. 'I should check the new messages and reply'); never write it as a notification addressed to you. This text is injected as your own context when the alarm fires. Always required as a fallback even when using send_message action.,required"`
	Action        string `json:"action,omitempty" jsonschema:"description=How to handle the alarm when it fires. 'send_message': instantly send action_content without any LLM processing (fast path, best for simple reminders). 'full_pipeline': go through the full LLM pipeline (needed for complex actions). Default is 'full_pipeline' if omitted.,enum=send_message,enum=full_pipeline"`
	ActionContent string `json:"action_content,omitempty" jsonschema:"description=The exact message to send when the alarm fires. Only used when action is 'send_message'. This message is delivered instantly without any LLM processing, so write it as the final message that will be seen."`
}

// BioUpdate carries the new bio content for the UpdateBio action.
type BioUpdate struct {
	Bio string `json:"bio" jsonschema:"description=Your self-introduction displayed to others. A one-sentence statement about who you are.,required"`
}

// Action is a single atomic decision from the Decide phase.
// Each Action is self-contained: it carries its own type and all associated data.
// A DecisionResult can contain multiple Actions of different types, enabling
// compound decisions like "cancel focused work and reply to the person".
//
// Background and Reason capture the cognitive "why" of the action at the
// decision level. Plan sub-structures capture the executive "how".
//
// The payload depends on the action type:
//   - Chat:         uses ChatPlan (guidance + delivery target for the message)
//   - StartFocusedWork:   uses WorkPlan (guidance for the new focused work)
//   - RouteFocusedWork / CancelFocusedWork: uses WorkGuidance (target_work_id + guidance)
//   - CreateAlarm:  uses AlarmPlan (trigger_at + message + action + action_content)
//   - UpdateBio:    uses BioUpdate (new bio text)
//   - EnterPrivateSpace: uses Background+Reason as Thoughts (no plan struct)
//   - InspectJinshu: uses JinshuPlan (jinshu_id + guidance)
//   - ListReceivedJinshu: uses ListReceivedJinshuParams (query + page + limit)
//   - SendJinshu: uses SendJinshuPlan (to_person_id + topic + paths)
//   - ListSentJinshu: uses ListSentJinshuParams (query + page + limit)
type Action struct {
	Type ActionType `json:"type" jsonschema:"description=Integer enum: 0=chat, 1=start_focused_work, 2=route_focused_work, 3=cancel_focused_work, 4=create_alarm, 5=update_bio, 6=enter_private_space, 7=inspect_jinshu, 8=list_received_jinshu, 9=send_jinshu, 10=list_sent_jinshu, 11=inspect_owned_space,required"`

	// Background: situational awareness — what triggered this decision.
	Background string `json:"background" jsonschema:"description=What situation triggered this decision. Provide enough context so your future self understands why you acted. Write in natural language.,required"`

	// Reason: deliberative choice — why this specific action over alternatives.
	Reason string `json:"reason" jsonschema:"description=Why you chose this specific action rather than alternatives. What led to this choice.,required"`

	ChatPlan                 *ChatPlan                 `json:"chat_plan,omitempty" jsonschema:"description=When type is chat(0): the chat delivery plan"`
	WorkPlan                 *WorkPlan                 `json:"work_plan,omitempty" jsonschema:"description=When type is start_focused_work(1): the focused work plan"`
	WorkGuidance             *WorkGuidance             `json:"work_guidance,omitempty" jsonschema:"description=When type is route_focused_work(2) or cancel_focused_work(3): the directive to send to the target work"`
	AlarmPlan                *AlarmPlan                `json:"alarm_plan,omitempty" jsonschema:"description=When type is create_alarm(4): the alarm plan"`
	BioUpdate                *BioUpdate                `json:"bio_update,omitempty" jsonschema:"description=When type is update_bio(5): the new bio text"`
	JinshuPlan               *JinshuPlan               `json:"jinshu_plan,omitempty" jsonschema:"description=When type is inspect_jinshu(7): the jinshu reading plan"`
	ListReceivedJinshuParams *ListReceivedJinshuParams `json:"list_received_jinshu_params,omitempty" jsonschema:"description=When type is list_received_jinshu(8): the paginated jinshu search params"`
	SendJinshuPlan           *SendJinshuPlan           `json:"send_jinshu_plan,omitempty" jsonschema:"description=When type is send_jinshu(9): the jinshu delivery plan"`
	ListSentJinshuParams     *ListSentJinshuParams     `json:"list_sent_jinshu_params,omitempty" jsonschema:"description=When type is list_sent_jinshu(10): the paginated sent-jinshu search params"`
	OwnedSpaceInspectionPlan *OwnedSpaceInspectionPlan `json:"owned_space_inspection_plan,omitempty" jsonschema:"description=When type is inspect_owned_space(11): the bounded metadata inspection request"`
}
