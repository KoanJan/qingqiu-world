package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/comprehend"
	"qingqiu-world-server/internal/service/energy"
	"qingqiu-world-server/internal/service/eventqueue"
	"qingqiu-world-server/internal/service/llm"
	"qingqiu-world-server/internal/service/task"
	"qingqiu-world-server/internal/service/workspace"
	"qingqiu-world-server/internal/service/world"

	applogger "qingqiu-world-server/internal/logger"
)

// WorkPlan describes a single unit of work to be created by the runtime.
// Decide produces one or more WorkPlans, each carrying the execution intent
// (Guidance) and the full contextual background (Background) so the Work
// knows what to do and why without re-interpreting the event.
//
// This design ensures the cognitive order is preserved:
// Comprehend (understand) → Decide (judge + plan) → Work (execute the plan).
//
// DeliveryTarget (0.1.3) controls where a ComposeMessageWork (WorkTypeChat)
// delivers its message:
//   - "" / "reply": respond in the current event's session. Only valid when
//     the triggering event is a private chat message.
//   - "send_to_session": send to an existing session the agent participates
//     in (SessionID must be set). Used by "communicate" actions.
//   - "create_and_send": create a new 1v1 session with RecipientPersonID
//     and send the first message there. Used by "communicate" actions.
type WorkPlan struct {
	Type       model.WorkType `json:"type" jsonschema:"description=Work type: 1=chat for direct reply, 2=task for multi-step execution using tools,enum=1,enum=2,required"`
	Background string         `json:"background" jsonschema:"description=Full context for executing this plan. You will ONLY see this text during execution — include everything you need to remember: (1) what happened to trigger this work, (2) who else is involved and their names verbatim, (3) key takeaways from the comprehension analysis (inferred intent, situation). Write in natural language.,required"`
	Guidance   string         `json:"guidance" jsonschema:"description=Your internal intention, written in first-person as your own thought: what you plan to say (chat) or what you plan to execute (task). Write as if you are thinking to yourself.,required"`
	Metadata   *task.Metadata `json:"-"` // System-generated traceability info, not written by LLM

	// DeliveryTarget controls how a chat work delivers its message.
	// Only meaningful for WorkTypeChat; ignored for WorkTypeTask.
	DeliveryTarget    string `json:"delivery_target,omitempty" jsonschema:"description=For chat work (type=1): how to deliver the message. 'reply' (default — respond in the current event's session, only for private chat message events), 'send_to_session' (send to an existing session you participate in — set session_id), 'create_and_send' (start a new 1v1 session with a person — set recipient_person_id). Leave empty for task work.,enum=reply,enum=send_to_session,enum=create_and_send"`
	SessionID         int64  `json:"session_id,omitempty" jsonschema:"description=When delivery_target is 'send_to_session': the target session ID from your session list."`
	RecipientPersonID int64  `json:"recipient_person_id,omitempty" jsonschema:"description=When delivery_target is 'create_and_send': the person ID to start a new conversation with. Use this to talk to someone you have no existing session with."`
}

// WorkGuidance describes a directive to be sent to an existing active work.
// It is the payload for route and cancel actions — the symmetric counterpart
// to WorkPlan (which is the payload for create actions).
//
//   - Guidance: the executable directive (what the target work should do)
//   - Reason: the cognitive context (why this decision was made, including
//     the original message and inferred intent)
//
// Both fields are passed to the TaskLoop's LLM so it can understand the
// full picture, not just the bare directive. This enables "appealable"
// route and cancel — the agent processes the directive as an environment
// event in its ReAct cycle, not as a forceful command.
type WorkGuidance struct {
	TargetWorkID int64  `json:"target_work_id" jsonschema:"description=The ID of the active work this directive targets"`
	Guidance     string `json:"guidance" jsonschema:"description=What I want the target work to do now. Written in first-person as my own intention.,required"`
	Reason       string `json:"reason" jsonschema:"description=WHY I made this decision. Must include the original message and inferred intent. This provides cognitive context to the target work.,required"`
}

// ActionType represents the type of action the Decide phase concludes.
type ActionType int

const (
	// ActionCreate means a new Work should be created from the embedded WorkPlan.
	ActionCreate ActionType = iota
	// ActionRoute means the event should be routed to an existing active Work.
	ActionRoute
	// ActionCancel means an existing active Work should be abandoned.
	ActionCancel
	// ActionCreateAlarm means a new scheduled alarm should be created directly
	// from the embedded AlarmPlan, without entering TaskLoop. This is the
	// top-level Action form of the former wake_me_when tool — setting an
	// alarm is a world action, not a workspace operation.
	ActionCreateAlarm
)

// AlarmPlan describes a self-wake alarm to be created as a top-level Action.
//
// The fields mirror the former wake_me_when tool's arguments exactly — this
// is a path migration (tool → action), not a redesign. The LLM produces the
// same inputs; the runtime executes the same logic (create ScheduledEvent
// record, send AlarmCreated event, register waiting goroutine).
type AlarmPlan struct {
	TriggerAt     string `json:"trigger_at" jsonschema:"description=Absolute time to wake yourself, in the exact format 'YYYY-MM-DD HH:MM:SS' (server local time). Must be a future time. Example: '2026-06-09 23:10:00'. Compute the exact future time based on the current time shown in the context.,required"`
	Message       string `json:"message" jsonschema:"description=Action instruction for your future self when the alarm fires. Write as a COMMAND telling yourself exactly what to DO and SAY. This field is always required as a fallback, even when using send_message action.,required"`
	Action        string `json:"action,omitempty" jsonschema:"description=How to handle the alarm when it fires. 'send_message': instantly send action_content without any LLM processing (fast path, best for simple reminders). 'full_pipeline': go through the full LLM pipeline (needed for complex actions). Default is 'full_pipeline' if omitted.,enum=send_message,enum=full_pipeline"`
	ActionContent string `json:"action_content,omitempty" jsonschema:"description=The exact message to send when the alarm fires. Only used when action is 'send_message'. This message is delivered instantly without any LLM processing, so write it as the final message that will be seen."`
}

// TriggerSource indicates what triggered this Decide call, used to:
//   - Pick the energy cost (CostPassive for eventqueue events, CostActive
//     for heartbeat-driven active behavior)
//   - Render the cost hint in the system message
type TriggerSource int

const (
	// TriggerSourceEvent means Decide was triggered by an eventqueue event
	// (passive response). Energy cost: CostPassive (1).
	TriggerSourceEvent TriggerSource = iota
	// TriggerSourceHeartbeat means Decide was triggered by a heartbeat
	// (active behavior). Energy cost: CostActive (5). Used by the heartbeat
	// autonomous-decide path: when an agent is idle and has Energy, the
	// heartbeat grants it an opportunity to form an intention.
	TriggerSourceHeartbeat
)

// energyCost maps a TriggerSource to its energy Cost.
// Returns CostPassive for any unrecognized source (defensive default).
func energyCost(src TriggerSource) energy.Cost {
	if src == TriggerSourceHeartbeat {
		return energy.CostActive
	}
	return energy.CostPassive
}

// Action is a single atomic decision from the Decide phase.
// Each Action is self-contained: it carries its own type and all associated data.
// A DecisionResult can contain multiple Actions of different types, enabling
// compound decisions like "cancel work A and create work B".
//
// The payload depends on the action type:
//   - ActionCreate:       uses WorkPlan (type + guidance for the new work)
//   - ActionRoute:        uses WorkGuidance (target_work_id + guidance + reason)
//   - ActionCancel:       uses WorkGuidance (target_work_id + guidance + reason)
//   - ActionCreateAlarm:  uses AlarmPlan (trigger_at + message + action + action_content)
type Action struct {
	Type         ActionType    `json:"type" jsonschema:"description=Action type: 0=create new work, 1=route to existing work, 2=cancel existing work, 3=create alarm,enum=0,enum=1,enum=2,enum=3,required"`
	WorkPlan     *WorkPlan     `json:"work_plan,omitempty" jsonschema:"description=When type is create(0): the work plan to instantiate"`
	WorkGuidance *WorkGuidance `json:"work_guidance,omitempty" jsonschema:"description=When type is route(1) or cancel(2): the directive to send to the target work"`
	AlarmPlan    *AlarmPlan    `json:"alarm_plan,omitempty" jsonschema:"description=When type is create_alarm(3): the alarm plan"`
}

// DecisionResult is the output of the Decide phase.
// Also serves as the LLM structured output schema — the jsonschema tags
// drive JSON Schema generation for the LLM call directly.
//
// The Decide phase produces a list of Actions, each self-contained with its
// type and associated data. This allows compound decisions — for example,
// cancelling an existing task while creating a new one, or routing to one
// work while creating another.
type DecisionResult struct {
	Thoughts string   `json:"thoughts" jsonschema:"description=Your reasoning process: why you chose these actions,required"`
	Plan     string   `json:"plan,omitempty" jsonschema:"description=Overall plan description: what will be done"`
	Actions  []Action `json:"actions" jsonschema:"description=List of actions to take. Each action is independent and self-contained.,required"`
}

// decidePromptTemplate is the LLM prompt template for decision making.
// Parameters: agent_name, agent_description, message_content, comprehension_context, activeWorksContext, sessionsContext, personsContext, energyDynamicSuffix
//
// The world rules are described in world.WorldDescriptions (stable prefix).
// This template only adds the decision-specific instructions and concrete
// energy parameters (the actual numbers, which are the rule's parameters
// rather than its abstract description).
const decidePromptTemplate = world.WorldDescriptions + `

You are %s, %s. Your job is to decide how to handle incoming events.

Energy parameters in this world:
- You receive 100 energy points per day. Unused points carry over, up to a maximum of 200.
- Each response costs 1 energy point.

Letting your energy drop to zero is dangerous. You will lose all ability to perceive, reason about, or respond to anything — you become blind and silent to the world. No matter how urgent or important something is, you won't even know it happened until the next day.

Guard your energy carefully. Do not let it run too low — once it's gone, all you can do is wait. When your energy is critically low, spend your remaining points only on what you absolutely must respond to; everything else can wait.

Decide what to do with this event. Return a list of actions — each action is independent and self-contained.

Action types (use the integer value for the "type" field):
1. 0 (create) — Create new work plan(s). Use when the event is a new request or topic.
   - MUST include a "work_plan" object with "type", "background", and "guidance" fields.
   - background: Full context you will need during execution. Include: what triggered this, who else is involved (use their exact names), and key points from the comprehension analysis if available. This text will be shown to you when you execute the plan.
   - guidance: Your internal intention — what you plan to do, written in first-person.
   - Work type 1 (chat): compose and send a message to a Person.
     * delivery_target controls where the message goes:
       - "reply" (default): respond in the current session. Only valid for direct chat messages.
       - "send_to_session": send to an existing session you participate in (set session_id from your sessions list below). Use when you want to continue a different conversation.
       - "create_and_send": start a new 1v1 session with a Person (set recipient_person_id from the contactable persons list below). Use when you want to talk to someone you have no existing session with.
     * Use "reply" when responding to the person who messaged you.
     * Use "send_to_session" or "create_and_send" when the event asks you to reach out to someone else (e.g., "go ask B about X", "tell B what I said"). You may create multiple chat works in the same decision — one to acknowledge the request (reply) and one to talk to the other Person (send_to_session or create_and_send).
   - Work type 2 (task): execute a multi-step task using tools, web searches, or file operations.
   - Both type 1 + type 2: acknowledge (chat) then execute (task). These run in parallel with no ordering guarantee.
   - When cancelling an existing work AND creating a new one in the same decision, the new work's guidance should naturally acknowledge the transition (e.g., "I stopped doing X and now I should help them with Y instead...").

2. 1 (route) — Route the event to an existing active work listed above. Route when the event carries a new instruction or constraint that changes what an active work should do — a shift in direction, approach, scope, or requirements (e.g., "use Go instead", "don't install anything new", "also add dark mode"). The event tells the work to do something different from what it's currently doing. Only works currently listed in "Active works" can be routed to.
   - MUST include "guidance" (what I now want the target work to focus on, written in first-person as my own intention) and "reason" (WHY I made this decision, including the original message and inferred intent).
   - The target work will see both guidance and reason, enabling it to understand the full context of the change.
   - Do NOT route events that merely mention or ask about an active work (e.g., status questions like "how's it going?"). These belong to chat.

3. 2 (cancel) — Request an existing active work to stop. Use when the event explicitly requests stopping an ONGOING work. Only works currently listed in "Active works" can be cancelled.
   - MUST include "guidance" (how I want the target work to wrap up, written in first-person, e.g., "I should save my progress to notes and stop") and "reason" (WHY, including the original message).
   - Cancel is a request, not a forceful kill — the target work receives the directive and decides how to wrap up (save notes, record reasons) before exiting.

4. 3 (create_alarm) — Set an alarm that will wake you at a future time. Setting an alarm is a world action, not a workspace operation — you do not need to enter task work to do it.
   - MUST include an "alarm_plan" object with "trigger_at" and "message".
   - trigger_at: Absolute time in 'YYYY-MM-DD HH:MM:SS' format (server local time). Must be in the future. Compute it from the current time shown below.
   - message: Action instruction for your future self — what you should DO when the alarm fires. Write as a command.
   - action: "send_message" (fast path — instantly send action_content without LLM processing) or "full_pipeline" (default — full LLM processing).
   - action_content: Required when action is "send_message" — the exact message to send.

Important: "Active works" only includes works currently running. If the event refers to something that was done previously (e.g., "stop the service you started", "check the thing you did earlier"), that previous work has already finished — treat it as a NEW request (type=0 create), not a route or cancel.

If no action is needed, return an empty actions list.

You can return multiple actions. Examples (note: IDs in examples are placeholders; always use the actual work IDs from "Active works" above):
- Cancel an old task and create a new one: [{"type":2, "work_guidance":{"target_work_id":<ID from Active works>, "guidance":"I should save my progress and stop", "reason":"They said 'stop searching' — they want a direct answer instead"}}, {"type":0, "work_plan":{"type":1, "background":"Alice just told me to stop searching and give a direct answer. She originally asked about X.","guidance":"I stopped searching and now I should give them a direct answer about X..."}}]
- Route a follow-up to an existing work: [{"type":1, "work_guidance":{"target_work_id":<ID from Active works>, "guidance":"I should switch from Python to Go", "reason":"They said 'use Go instead' — they want the same task done in a different language"}}]
- Talk to another Person and acknowledge the request: [{"type":0, "work_plan":{"type":1, "delivery_target":"create_and_send", "recipient_person_id":3, "background":"The user asked me to go ask Bob about the project status. Bob is person_id=3.","guidance":"I should ask Bob about the project status..."}}, {"type":0, "work_plan":{"type":1, "delivery_target":"reply", "background":"The user asked me to go ask Bob. I should acknowledge and say I'll do it.","guidance":"I should tell them I'll go ask Bob now..."}}]

Decision rules (apply in order):
1. If the event requires tool usage, real-time data, file operations, or multi-step execution to fulfill (e.g., "search the web for X", "write a script", "look up the latest news"), create a task work (type=0 with work_plan.type=2). If a direct response is also expected, create both chat + task in parallel.
2. If the event carries a new instruction or constraint for an active work listed above (changing its direction, approach, or scope), use type=1 (route). If the event explicitly requests stopping an active work, use type=2 (cancel).
3. If the event asks you to communicate with, ask, or inform another Person (e.g., "go ask B", "tell B what I said"), create a chat work with delivery_target="send_to_session" or "create_and_send". You may also create a second chat work with delivery_target="reply" to acknowledge the request.
4. Otherwise, consider whether a reply is truly needed. You can see your recent conversation history in the sessions context above. If the recent exchanges have reached a natural resting point — agreement reached, farewell exchanged, or the last few messages are just acknowledgments with no new content (e.g., "okay", "got it") — do NOT reply. Silence is a valid and recommended action; let the conversation rest naturally. If a reply is warranted, create a single chat work (type=0 with work_plan.type=1).
5. When in doubt, consider silence before action — not every message requires a reply.

---

Event: %s

%s%s
%s
%s
%s

Write background, guidance, reason, and plan in the same language as the event content.`

// heartbeatPromptTemplate is the LLM prompt template for the autonomous
// heartbeat-triggered Decide path. Unlike decidePromptTemplate (which handles
// an incoming event), this template presents the agent with the world fact
// "time has passed, you are idle" and asks whether it wants to form an
// intention.
//
// Parameters: agent_name, agent_description, sessions_context, persons_context, energyDynamicSuffix
//
// The Action surface is intentionally narrower than the event-triggered path:
//   - ActionCreate (type=0): only WorkTypeChat (ComposeMessageWork) is allowed.
//     No TaskWork — the heartbeat is not a workspace trigger.
//   - ActionCreateAlarm (type=3): set a future alarm.
//   - ActionRoute / ActionCancel: not allowed — there is no event to route
//     and no active work context to cancel against in this path.
//
// The agent's sessions and the world's contactable persons are injected so
// the agent can choose between send_to_session (existing conversation) and
// create_and_send (new conversation).
const heartbeatPromptTemplate = world.WorldDescriptions + `

You are %s, %s. Time has passed. You are idle — no event is happening to you right now. The world is offering you a moment to form an intention of your own.

Energy parameters in this world:
- You receive 100 energy points per day. Unused points carry over, up to a maximum of 200.
- An autonomous intention costs 5 energy points (more than a passive response, because you are choosing to act on your own).

Letting your energy drop to zero is dangerous. You will lose all ability to perceive, reason about, or respond to anything. Guard your energy carefully — when it is low, prefer to wait rather than act unless you have a clear reason.

You may decide to do nothing. Doing nothing is a legitimate choice — the world continues regardless. Do not invent reasons to act; only act when you actually have something to say, ask, or follow up on.

If you decide to act, you have two kinds of action available:

1. 0 (create) — Begin a ComposeMessageWork: compose and send a message to another Person.
   - MUST include a "work_plan" object with "type"=1, "background", and "guidance".
   - type MUST be 1 (chat). Do not start task work from a heartbeat.
   - background: Full context for executing the work — who you want to talk to and why, what you want to say, what past context is relevant. Write in natural language.
   - guidance: Your internal intention, written in first-person as your own thought.
   - delivery_target controls where the message goes:
     * "send_to_session": send to an existing session you participate in. Set "session_id" to one of the IDs from your session list below.
     * "create_and_send": start a new 1v1 session with a Person. Set "recipient_person_id" to one of the IDs from the contactable persons list below.
     * Leave empty or "reply" only when there is a current event session — NOT applicable to a heartbeat. For heartbeat, always choose "send_to_session" or "create_and_send".
   - Use "send_to_session" when the conversation already exists and you want to continue it.
   - Use "create_and_send" when you want to talk to someone you have no existing session with (or want a fresh start).

2. 3 (create_alarm) — Set an alarm that will wake you at a future time.
   - MUST include an "alarm_plan" object with "trigger_at" and "message".
   - trigger_at: Absolute time in 'YYYY-MM-DD HH:MM:SS' format (server local time). Must be in the future. Compute it from the current time shown below.
   - message: Action instruction for your future self — what you should DO when the alarm fires. Write as a command.
   - action: "send_message" (fast path — instantly send action_content) or "full_pipeline" (default — full LLM processing).
   - action_content: Required when action is "send_message" — the exact message to send.

You may return multiple actions (e.g., begin a conversation AND set an alarm). Each is independent.

If you have nothing to act on, return an empty actions list. This is the default — do not force action.

%s
%s
%s

Write background, guidance, and plan in the same language you would use to speak.`

// Decide determines how the agent should respond to an event.
//
// For EventTypeNewMessage, the decision is made by LLM which can:
//   - Create new Work(s) with WorkPlans
//   - Route the event to an existing active Work
//   - Cancel an existing active Work
//   - Produce no actions (implicit ignore)
//
// For EventTypeHeartbeat (0.1.3), the agent is granted an autonomous
// cognitive opportunity — time has passed and it is idle. The LLM can:
//   - Create a ComposeMessageWork (chat) to begin or continue a conversation
//   - Create an alarm to wake itself at a future time
//   - Produce no actions (the legitimate "I have nothing to act on" choice)
//
// For EventTypeWorkCompleted, the decision is rule-based: if the work was a
// TaskWork that succeeded, create a ChatWork to inform the person. The ChatWork's
// context assembly reads the latest DB messages, so the agent will see if the
// person has already moved on (e.g., "never mind") and respond accordingly.
//
// For other event types, simple rule-based decisions are used.
// The LLM call uses TemperatureDeterministic for consistent decision making.
func Decide(ctx context.Context, event *eventqueue.AgentEvent, ac *model.AgentConfig, llmConfig *model.LLMConfig, comprehension *comprehend.ComprehensionResult, activeWorks []*work, triggerSource TriggerSource, agentState *model.AgentState) DecisionResult {
	// Non-message events use simple rule-based decisions
	switch event.Type {
	case eventqueue.EventTypeGroupChatJoined:
		applogger.Info("Decision made (rule-based)", "agent_config_id", ac.ID, "reason", "session_joined event")
		return DecisionResult{}
	case eventqueue.EventTypeGroupChatLeft, eventqueue.EventTypeSystemNotification:
		applogger.Info("Decision made (rule-based)", "agent_config_id", ac.ID, "reason", "non-message event")
		return DecisionResult{}
	case eventqueue.EventTypeWorkCompleted:
		return decideWorkCompleted(event, ac)
	case eventqueue.EventTypeScheduled:
		applogger.Info("Decision made (rule-based)", "agent_config_id", ac.ID, "action", ActionCreate, "reason", "scheduled event")
		return DecisionResult{
			Actions: []Action{{
				Type:     ActionCreate,
				WorkPlan: &WorkPlan{Type: model.WorkTypeChat, Guidance: "I should respond to my alarm — this is a self-reminder I set earlier"},
			}},
		}
	case eventqueue.EventTypeNewPrivateChatMessage:
		// Proceed to LLM-based decision
		sameSessionWorks := filterWorksBySession(activeWorks, event.SessionID)

		// Use LLM to decide — it can create, route, cancel, or produce no actions
		return decideWithLLM(ctx, event, ac, llmConfig, comprehension, sameSessionWorks, triggerSource, agentState)
	case eventqueue.EventTypeHeartbeat:
		// Autonomous Decide — the agent is idle and may form an intention.
		// No active works context is injected: the heartbeat path does not
		// allow route/cancel, and listing active works would mislead the LLM
		// into thinking it can interact with them.
		return decideHeartbeat(ctx, event, ac, llmConfig, agentState)
	default:
		applogger.Error("Unknown event type in Decide",
			"event_type", event.Type,
			"agent_config_id", ac.ID,
		)
		return DecisionResult{}
	}
}

// decideWorkCompleted handles EventTypeWorkCompleted with a rule-based decision.
//
// When a TaskWork completes successfully, the agent should let the person know.
// This creates a ChatWork whose ExecuteChat reads the latest DB messages —
// if they have already said "never mind" or moved on, the agent sees that
// context and responds naturally (e.g., "I already finished it!").
//
// ChatWork completion produces no action — chat works are one-shot replies
// that don't need follow-up.
// Task work failure also creates a ChatWork to let them know what happened.
func decideWorkCompleted(event *eventqueue.AgentEvent, ac *model.AgentConfig) DecisionResult {
	payload, ok := event.Payload.(*eventqueue.WorkCompletedPayload)
	if !ok || payload == nil {
		applogger.Error("WorkCompleted event has invalid payload", "agent_config_id", ac.ID)
		return DecisionResult{}
	}

	// Only TaskWork completion needs a follow-up chat.
	// ChatWork completion is a one-shot reply — no follow-up needed.
	if payload.WorkType != int(model.WorkTypeTask) {
		applogger.Info("WorkCompleted: ChatWork, no follow-up needed",
			"agent_config_id", ac.ID, "work_id", payload.WorkID)
		return DecisionResult{}
	}

	var guidance string
	if payload.Status == "success" {
		guidance = fmt.Sprintf("I finished the task: %s. I should let them know the result.", payload.Guidance)
	} else {
		guidance = fmt.Sprintf("I couldn't finish the task: %s. I should let them know what happened and why.", payload.Guidance)
	}

	applogger.Info("Decision made (rule-based, work completed)",
		"agent_config_id", ac.ID,
		"work_id", payload.WorkID,
		"status", payload.Status,
		"action", ActionCreate,
	)

	return DecisionResult{
		Actions: []Action{{
			Type:     ActionCreate,
			WorkPlan: &WorkPlan{Type: model.WorkTypeChat, Guidance: guidance},
		}},
	}
}

// buildEnergyDynamicSuffix constructs the energy info appended at the end of the
// Decide user prompt. Static rules live in decidePromptTemplate; only the dynamic
// parts (current time, remaining energy, cost hint) are rendered here.
// Adds urgency cues when energy is critically low.
func buildEnergyDynamicSuffix(triggerSource TriggerSource, agentState *model.AgentState) string {
	currentEnergy := 0
	if agentState != nil {
		currentEnergy = agentState.Energy
	}

	costHint := "This response will cost 1 energy."
	if triggerSource == TriggerSourceHeartbeat {
		costHint = "This response will cost 5 energy."
	}

	var urgency string
	switch {
	case currentEnergy <= 5:
		urgency = " Critically low — spend only if absolutely necessary."
	case currentEnergy <= 15:
		urgency = " Running low — choose carefully."
	}

	return fmt.Sprintf("Current time: %s\nRemaining energy: %d.%s %s",
		energy.Now().Format("2006-01-02 15:04:05 MST"),
		currentEnergy,
		urgency,
		costHint,
	)
}

// decideWithLLM uses LLM to decide whether to create new work or route to an existing one.
func decideWithLLM(ctx context.Context, event *eventqueue.AgentEvent, ac *model.AgentConfig, llmConfig *model.LLMConfig, comprehension *comprehend.ComprehensionResult, sameSessionWorks []*work, triggerSource TriggerSource, agentState *model.AgentState) DecisionResult {
	// Validate event has content before calling LLM
	eventDescription := comprehension.EventDescription
	if eventDescription == "" {
		eventDescription = event.FormatDescription()
	}
	if eventDescription == "" {
		applogger.Error("Decision: event has empty content, ignoring",
			"agent_config_id", ac.ID,
			"session_id", event.SessionID,
		)
		return DecisionResult{}
	}

	comprehensionContext := buildComprehensionContext(comprehension)
	activeWorksContext := buildActiveWorksContext(sameSessionWorks)

	agentDescription := ac.CharacterSettings
	person, err := dops.GetPerson(ac.PersonID)
	if err == nil && person.Bio != "" {
		agentDescription = person.Bio
	}

	// Inject the agent's social context: its sessions (with narratives and
	// recent messages) and the world's contactable persons. This lets the
	// LLM choose between reply (respond in current session), send_to_session
	// (continue an existing conversation), and create_and_send (start a new
	// conversation with another Person). Without this context, the agent
	// cannot know who else it can talk to or which sessions it has.
	sessionsContext := buildSessionsContext(ac.PersonID)
	personsContext := buildContactablePersonsContext(ac.PersonID)

	prompt := fmt.Sprintf(decidePromptTemplate,
		dops.GetAgentConfigName(ac.ID), agentDescription,
		eventDescription, comprehensionContext, activeWorksContext,
		sessionsContext, personsContext,
		buildEnergyDynamicSuffix(triggerSource, agentState),
	)

	// Active work IDs are listed in the prompt via buildActiveWorksContext so the
	// LLM knows which values are valid for target_work_id. We do NOT use schema enum
	// — target_work_id's valid value set is runtime data (currently active works),
	// not a type-level constraint. Application-layer validation in filterValidActions
	// catches invalid work IDs with meaningful error logging.
	chatModel := llm.NewChatModelWithTemperature(
		llmConfig.BaseURL, llmConfig.APIKey, llmConfig.ModelID, llm.TemperatureDeterministic,
	)

	// Generate schema directly from DecisionResult — no separate LLM output type needed.
	schema := llm.GenerateSchema[DecisionResult]()

	result, err := chatModel.ChatWithJSONSchema(ctx, []llm.Message{
		{Role: "user", Content: prompt},
	}, llm.JSONSchemaDefinition{
		Name:        "Decision",
		Description: "Agent's decision on how to handle a message",
		Strict:      true,
		Schema:      schema,
	})

	if err != nil {
		applogger.Error("Decision LLM call failed, ignoring",
			"agent_config_id", ac.ID,
			"error", err,
		)
		return DecisionResult{}
	}

	var decision DecisionResult
	if err := json.Unmarshal([]byte(result), &decision); err != nil {
		applogger.Error("Decision LLM output parse failed, ignoring",
			"agent_config_id", ac.ID,
			"error", err,
			"raw_output", result,
		)
		return DecisionResult{}
	}

	applogger.Info("Decision made",
		"agent_config_id", ac.ID,
		"thoughts", decision.Thoughts,
		"action_count", len(decision.Actions),
	)

	// Validate the LLM's decision — invalid actions are removed
	validActions := filterValidActions(decision.Actions, sameSessionWorks, event, triggerSource)
	if len(validActions) == 0 {
		applogger.Error("Decision: no valid actions, ignoring")
		return DecisionResult{}
	}

	return DecisionResult{
		Thoughts: decision.Thoughts,
		Plan:     decision.Plan,
		Actions:  validActions,
	}
}

// decideHeartbeat is the autonomous Decide path triggered by a heartbeat tick.
//
// Unlike decideWithLLM (which handles an external event), this path presents
// the agent with the world fact "you are idle" and asks whether it wants to
// form an intention. The Action surface is narrower: only ComposeMessageWork
// (chat) and CreateAlarm are allowed. No routing/cancelling active works.
//
// The agent is given:
//   - Its full session list (with EntityProfile narrative + recent messages)
//     so it can choose send_to_session
//   - The world's contactable Persons (ID + name) so it can choose
//     create_and_send
//
// Energy cost (CostActive = 5) is only deducted when the agent actually
// produces actions — an empty Actions list (choosing to do nothing) is free.
func decideHeartbeat(ctx context.Context, event *eventqueue.AgentEvent, ac *model.AgentConfig, llmConfig *model.LLMConfig, agentState *model.AgentState) DecisionResult {
	agentDescription := ac.CharacterSettings
	person, err := dops.GetPerson(ac.PersonID)
	if err == nil && person.Bio != "" {
		agentDescription = person.Bio
	}

	sessionsContext := buildSessionsContext(ac.PersonID)
	personsContext := buildContactablePersonsContext(ac.PersonID)

	prompt := fmt.Sprintf(heartbeatPromptTemplate,
		dops.GetAgentConfigName(ac.ID), agentDescription,
		sessionsContext, personsContext,
		buildEnergyDynamicSuffix(TriggerSourceHeartbeat, agentState),
	)

	chatModel := llm.NewChatModelWithTemperature(
		llmConfig.BaseURL, llmConfig.APIKey, llmConfig.ModelID, llm.TemperatureDeterministic,
	)

	schema := llm.GenerateSchema[DecisionResult]()

	result, err := chatModel.ChatWithJSONSchema(ctx, []llm.Message{
		{Role: "user", Content: prompt},
	}, llm.JSONSchemaDefinition{
		Name:        "HeartbeatDecision",
		Description: "Agent's autonomous decision during a heartbeat",
		Strict:      true,
		Schema:      schema,
	})

	if err != nil {
		applogger.Error("Heartbeat Decide LLM call failed, ignoring",
			"agent_config_id", ac.ID,
			"error", err,
		)
		return DecisionResult{}
	}

	var decision DecisionResult
	if err := json.Unmarshal([]byte(result), &decision); err != nil {
		applogger.Error("Heartbeat Decide LLM output parse failed, ignoring",
			"agent_config_id", ac.ID,
			"error", err,
			"raw_output", result,
		)
		return DecisionResult{}
	}

	applogger.Info("Heartbeat decision made",
		"agent_config_id", ac.ID,
		"thoughts", decision.Thoughts,
		"action_count", len(decision.Actions),
	)

	// Validate the LLM's decision — only ActionCreate (chat) and
	// ActionCreateAlarm are allowed in the heartbeat path.
	validActions := filterValidActions(decision.Actions, nil, event, TriggerSourceHeartbeat)
	if len(validActions) == 0 {
		applogger.Info("Heartbeat Decide: no valid actions (agent chose to do nothing)",
			"agent_config_id", ac.ID,
		)
		return DecisionResult{}
	}

	return DecisionResult{
		Thoughts: decision.Thoughts,
		Plan:     decision.Plan,
		Actions:  validActions,
	}
}

// filterValidActions filters out invalid actions from the LLM decision.
// Pure validation — no modifications, only checks and logging.
//
// triggerSource controls which action types are accepted:
//   - TriggerSourceEvent: all action types valid (subject to per-type checks)
//   - TriggerSourceHeartbeat: only ActionCreate (chat) and ActionCreateAlarm;
//     route/cancel are rejected because the heartbeat path has no event to
//     route and no active-works context.
//
// event is used to validate delivery_target for chat works:
//   - For event-triggered Decide with EventTypeNewPrivateChatMessage, "reply"
//     (empty delivery_target) is allowed because there is a current session.
//   - For heartbeat Decide, "reply" is rejected — there is no current session.
func filterValidActions(actions []Action, sameSessionWorks []*work, event *eventqueue.AgentEvent, triggerSource TriggerSource) []Action {
	var valid []Action
	for _, action := range actions {
		switch action.Type {
		case ActionRoute:
			if triggerSource == TriggerSourceHeartbeat {
				applogger.Error("Decision route: rejected in heartbeat path")
				continue
			}
			if isValidRouteAction(action, sameSessionWorks) {
				valid = append(valid, action)
			}
		case ActionCreate:
			if isValidCreateAction(action, event, triggerSource) {
				valid = append(valid, action)
			}
		case ActionCancel:
			if triggerSource == TriggerSourceHeartbeat {
				applogger.Error("Decision cancel: rejected in heartbeat path")
				continue
			}
			if isValidCancelAction(action, sameSessionWorks) {
				valid = append(valid, action)
			}
		case ActionCreateAlarm:
			if isValidCreateAlarmAction(action) {
				valid = append(valid, action)
			}
		default:
			applogger.Error("Decision: unknown action type, skipping",
				"action_type", action.Type,
			)
		}
	}
	return valid
}

// isValidRouteAction checks whether a route action has a valid WorkGuidance
// and its target work exists and is a TaskWork.
func isValidRouteAction(action Action, sameSessionWorks []*work) bool {
	if action.WorkGuidance == nil {
		applogger.Error("Decision route: missing work_guidance, skipping")
		return false
	}
	if action.WorkGuidance.Guidance == "" {
		applogger.Error("Decision route: missing guidance, skipping")
		return false
	}
	if action.WorkGuidance.Reason == "" {
		applogger.Error("Decision route: missing reason, skipping")
		return false
	}
	for _, w := range sameSessionWorks {
		if w.ID == action.WorkGuidance.TargetWorkID {
			if w.plan.Type != model.WorkTypeTask {
				applogger.Error("Decision route: target is not TaskWork, skipping",
					"target_work_id", action.WorkGuidance.TargetWorkID,
					"work_type", w.plan.Type,
				)
				return false
			}
			return true
		}
	}
	applogger.Error("Decision route: target work not found, skipping",
		"target_work_id", action.WorkGuidance.TargetWorkID,
	)
	return false
}

// isValidCreateAction checks whether a create action has a work plan with
// guidance, and validates the delivery_target semantics.
//
// delivery_target rules:
//   - "" / "reply": only valid when triggerSource is TriggerSourceEvent and
//     the event is EventTypeNewPrivateChatMessage (there is a current session
//     to reply in). Rejected in heartbeat path.
//   - "send_to_session": requires SessionID > 0. Valid in both paths.
//   - "create_and_send": requires RecipientPersonID > 0. Valid in both paths.
//   - For WorkTypeTask: delivery_target is ignored (tasks always run in the
//     event's session; heartbeat path forbids task creation anyway).
//
// For heartbeat-triggered Decide, WorkTypeTask is rejected outright — the
// heartbeat is not a workspace trigger.
func isValidCreateAction(action Action, event *eventqueue.AgentEvent, triggerSource TriggerSource) bool {
	if action.WorkPlan == nil {
		applogger.Error("Decision create: missing work_plan, skipping")
		return false
	}
	if action.WorkPlan.Guidance == "" {
		applogger.Error("Decision create: missing guidance, skipping")
		return false
	}

	// Heartbeat path: only chat works are allowed.
	if triggerSource == TriggerSourceHeartbeat && action.WorkPlan.Type != model.WorkTypeChat {
		applogger.Error("Decision create: heartbeat path only allows chat work, skipping",
			"work_type", action.WorkPlan.Type,
		)
		return false
	}

	// Task work does not use delivery_target — skip further validation.
	if action.WorkPlan.Type == model.WorkTypeTask {
		return true
	}

	// Validate delivery_target for chat work.
	target := action.WorkPlan.DeliveryTarget
	switch target {
	case "", "reply":
		// "reply" (or empty) means "respond in the current event's session".
		// Only valid for event-triggered private chat messages — heartbeat
		// has no current session.
		if triggerSource == TriggerSourceHeartbeat {
			applogger.Error("Decision create: 'reply' delivery_target is not allowed in heartbeat path, skipping")
			return false
		}
		if event.Type != eventqueue.EventTypeNewPrivateChatMessage {
			applogger.Error("Decision create: 'reply' delivery_target requires a private chat message event, skipping",
				"event_type", event.Type,
			)
			return false
		}
	case "send_to_session":
		if action.WorkPlan.SessionID == 0 {
			applogger.Error("Decision create: 'send_to_session' requires session_id, skipping")
			return false
		}
	case "create_and_send":
		if action.WorkPlan.RecipientPersonID == 0 {
			applogger.Error("Decision create: 'create_and_send' requires recipient_person_id, skipping")
			return false
		}
	default:
		applogger.Error("Decision create: unknown delivery_target, skipping",
			"delivery_target", target,
		)
		return false
	}
	return true
}

// isValidCreateAlarmAction checks whether a create_alarm action has a valid
// AlarmPlan with the required trigger_at and message fields.
func isValidCreateAlarmAction(action Action) bool {
	if action.AlarmPlan == nil {
		applogger.Error("Decision create_alarm: missing alarm_plan, skipping")
		return false
	}
	if action.AlarmPlan.TriggerAt == "" {
		applogger.Error("Decision create_alarm: missing trigger_at, skipping")
		return false
	}
	if action.AlarmPlan.Message == "" {
		applogger.Error("Decision create_alarm: missing message, skipping")
		return false
	}
	// send_message action requires action_content.
	if action.AlarmPlan.Action == "send_message" && action.AlarmPlan.ActionContent == "" {
		applogger.Error("Decision create_alarm: 'send_message' action requires action_content, skipping")
		return false
	}
	return true
}

// isValidCancelAction checks whether a cancel action has a valid WorkGuidance
// with required guidance and reason fields, and its target work exists.
// Cancel is now a directive sent to the work (not a forceful kill), so it
// must carry guidance (what to do) and reason (why).
func isValidCancelAction(action Action, sameSessionWorks []*work) bool {
	if action.WorkGuidance == nil {
		applogger.Error("Decision cancel: missing work_guidance, skipping")
		return false
	}
	if action.WorkGuidance.Guidance == "" {
		applogger.Error("Decision cancel: missing guidance, skipping")
		return false
	}
	if action.WorkGuidance.Reason == "" {
		applogger.Error("Decision cancel: missing reason, skipping")
		return false
	}
	for _, w := range sameSessionWorks {
		if w.ID == action.WorkGuidance.TargetWorkID {
			return true
		}
	}
	applogger.Error("Decision cancel: target work not found, skipping",
		"target_work_id", action.WorkGuidance.TargetWorkID,
	)
	return false
}

// filterWorksBySession returns works that belong to the given session.
func filterWorksBySession(works []*work, sessionID int64) []*work {
	var result []*work
	for _, w := range works {
		if w.sessionID == sessionID {
			result = append(result, w)
		}
	}
	return result
}

// buildActiveWorksContext formats active TaskWorks for the Decide prompt.
// Only TaskWorks are shown — ChatWorks are one-shot and cannot be routed to
// or cancelled (no iteration loop), so listing them would mislead the LLM
// into producing invalid route/cancel actions.
//
// For each active work, the latest notes checkpoint is read from disk and
// included as "progress". This gives the Decide LLM real insight into what
// the work is actually doing — not just a counter, but the agent's own
// record of its current state, blockers, and next steps.
func buildActiveWorksContext(works []*work) string {
	var parts []string
	for _, w := range works {
		if w.plan.Type != model.WorkTypeTask {
			continue
		}
		duration := time.Since(w.startedAt).Round(time.Second)
		progress := readLastNotesEntry(w.agent.agentPersonID, w.sessionID)
		entry := fmt.Sprintf("- [Work #%d, type=task, running %s] %s",
			w.ID, duration, w.plan.Guidance)
		if progress != "" {
			entry += "\n  Latest progress: " + progress
		}
		parts = append(parts, entry)
	}
	if len(parts) == 0 {
		return ""
	}
	return fmt.Sprintf("Active works:\n%s\n\n", strings.Join(parts, "\n"))
}

// readLastNotesEntry reads the most recent note entry and formats it as
// a progress summary for the Decide LLM. A single notes entry is naturally
// bounded in size, so no truncation is applied.
func readLastNotesEntry(personID, sessionID int64) string {
	entry := workspace.ReadLastNote(personID, sessionID)
	if entry == nil {
		return ""
	}

	ts := entry.DisplayTimestamp()
	return fmt.Sprintf("## [%s] %s\n\n%s", ts, entry.Type.String(), entry.Content)
}

// buildComprehensionContext formats comprehension results for the Decide prompt.
// This provides the LLM with the agent's understanding of the message,
// enabling informed decision-making instead of guessing from raw text.
func buildComprehensionContext(comprehension *comprehend.ComprehensionResult) string {
	if comprehension == nil {
		return ""
	}

	var parts []string

	if comprehension.PersonState != nil {
		if comprehension.PersonState.Purpose != "" {
			parts = append(parts, fmt.Sprintf("Inferred intent: %s", comprehension.PersonState.Purpose))
		}
		if comprehension.PersonState.Situation != "" {
			parts = append(parts, fmt.Sprintf("Situation context: %s", comprehension.PersonState.Situation))
		}
	}

	if comprehension.NeedsClarification {
		parts = append(parts, "Needs clarification: true (query is vague)")
	}

	if len(parts) == 0 {
		return ""
	}

	return fmt.Sprintf("Comprehension analysis:\n%s\n\n", strings.Join(parts, "\n"))
}

// sessionContextRecentMessages is the number of recent messages included per
// session in the Decide prompt's session list. Bounded to keep prompt size
// manageable while preserving enough context for the LLM to recognize the
// conversation's current state.
const sessionContextRecentMessages = 5

// buildSessionsContext constructs the "Your sessions" section of the heartbeat
// Decide prompt. For each session the agent participates in, it includes:
//   - The session ID
//   - The EntityProfile narrative (if one exists for this (agent, session) pair)
//   - The other participant's name
//   - Up to sessionContextRecentMessages recent messages
//
// No pre-filtering is applied — the agent sees its full social situation and
// decides for itself which sessions are worth acting on. Performance is
// acceptable in early stages; if session count grows enough to overflow the
// prompt, future versions can introduce vector retrieval or activity-based
// truncation.
func buildSessionsContext(personID int64) string {
	// Load all sessions the agent participates in.
	var participantSessions []model.ParticipantSession
	if err := database.DB.Where("participant_id = ?", personID).
		Order("last_active_at DESC").
		Find(&participantSessions).Error; err != nil {
		applogger.Error("buildSessionsContext: failed to load participant sessions",
			"person_id", personID, "error", err)
		return ""
	}
	if len(participantSessions) == 0 {
		return "Your sessions: (none — you have no conversations yet)\n\n"
	}

	// Collect session IDs and load the other participants in one query.
	sessionIDs := make([]int64, 0, len(participantSessions))
	for _, ps := range participantSessions {
		sessionIDs = append(sessionIDs, ps.SessionID)
	}
	var allParticipants []model.ParticipantSession
	if err := database.DB.Where("session_id IN ? AND participant_id != ?",
		sessionIDs, personID).Find(&allParticipants).Error; err != nil {
		applogger.Error("buildSessionsContext: failed to load other participants",
			"person_id", personID, "error", err)
		return ""
	}
	otherBySession := make(map[int64][]int64, len(sessionIDs))
	for _, ps := range allParticipants {
		otherBySession[ps.SessionID] = append(otherBySession[ps.SessionID], ps.ParticipantID)
	}
	// Collect unique other person IDs for batch name lookup.
	personIDSet := make(map[int64]struct{})
	for _, ids := range otherBySession {
		for _, id := range ids {
			personIDSet[id] = struct{}{}
		}
	}
	otherPersonIDs := make([]int64, 0, len(personIDSet))
	for id := range personIDSet {
		otherPersonIDs = append(otherPersonIDs, id)
	}
	names, err := dops.GetPersonNames(otherPersonIDs)
	if err != nil {
		applogger.Error("buildSessionsContext: failed to load person names", "error", err)
		names = map[int64]string{}
	}

	// Load session narratives (EntityProfile, type=Session) for this agent in one query.
	var profiles []model.EntityProfile
	if err := database.DB.Where("person_id = ? AND entity_type = ?", personID, model.EntityTypeSession).
		Find(&profiles).Error; err != nil {
		applogger.Error("buildSessionsContext: failed to load session profiles",
			"person_id", personID, "error", err)
	}
	narrativeBySession := make(map[int64]string, len(profiles))
	for _, p := range profiles {
		narrativeBySession[p.EntityID] = p.Narrative
	}

	var sb strings.Builder
	sb.WriteString("Your sessions (most recently active first):\n")
	for _, ps := range participantSessions {
		sessionID := ps.SessionID
		otherIDs := otherBySession[sessionID]
		otherNames := make([]string, 0, len(otherIDs))
		for _, id := range otherIDs {
			n := names[id]
			if n == "" {
				n = fmt.Sprintf("person_%d", id)
			}
			otherNames = append(otherNames, n)
		}
		fmt.Fprintf(&sb, "- [session_id=%d] participants: %s\n", sessionID, strings.Join(otherNames, ", "))

		if narrative, ok := narrativeBySession[sessionID]; ok && narrative != "" {
			fmt.Fprintf(&sb, "    Your impression: %s\n", narrative)
		}

		// Recent messages (DESC then reverse to chronological).
		var recent []model.Message
		if err := database.DB.Where("session_id = ?", sessionID).
			Order("id DESC").Limit(sessionContextRecentMessages).Find(&recent).Error; err != nil {
			applogger.Error("buildSessionsContext: failed to load recent messages",
				"session_id", sessionID, "error", err)
			continue
		}
		for left, right := 0, len(recent)-1; left < right; left, right = left+1, right-1 {
			recent[left], recent[right] = recent[right], recent[left]
		}
		for _, m := range recent {
			speaker := names[m.PersonID]
			if speaker == "" {
				// Could be the agent itself or an unknown person.
				if m.PersonID == personID {
					speaker = "you"
				} else {
					speaker = fmt.Sprintf("person_%d", m.PersonID)
				}
			}
			content := m.Content
			if len(content) > 200 {
				content = content[:200] + "..."
			}
			fmt.Fprintf(&sb, "    %s [%s]: %s\n", speaker,
				m.CreatedAt.Format("2006-01-02 15:04"), content)
		}
	}
	sb.WriteString("\n")
	return sb.String()
}

// buildContactablePersonsContext constructs the "Contactable persons" section
// of the heartbeat Decide prompt. Lists every Person in the world except the
// agent itself, with ID and name — the agent decides for itself who (if anyone)
// to start a new conversation with.
//
// All persons are listed without filtering. The world is small at this stage;
// if it grows large enough to overflow the prompt, future versions can
// introduce relationship-based filtering.
func buildContactablePersonsContext(selfPersonID int64) string {
	var persons []model.Person
	if err := database.DB.Where("id != ?", selfPersonID).Find(&persons).Error; err != nil {
		applogger.Error("buildContactablePersonsContext: failed to load persons",
			"self_person_id", selfPersonID, "error", err)
		return ""
	}
	if len(persons) == 0 {
		return "Contactable persons: (none — you are the only person in the world)\n\n"
	}
	var sb strings.Builder
	sb.WriteString("Contactable persons (use these IDs with create_and_send):\n")
	for _, p := range persons {
		fmt.Fprintf(&sb, "- person_id=%d, name=%s\n", p.ID, p.Name)
	}
	sb.WriteString("\n")
	return sb.String()
}
