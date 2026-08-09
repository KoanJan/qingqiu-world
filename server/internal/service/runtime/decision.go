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
	"qingqiu-world-server/internal/service/agent"
	"qingqiu-world-server/internal/service/comprehend"
	"qingqiu-world-server/internal/service/energy"
	"qingqiu-world-server/internal/service/eventqueue"
	"qingqiu-world-server/internal/service/llm"
	"qingqiu-world-server/internal/service/task"
	"qingqiu-world-server/internal/service/workspace"
	"qingqiu-world-server/internal/service/world"

	applogger "qingqiu-world-server/internal/logger"
)

// WorkPlan describes a task to be created via CreateTask action.
// It carries Guidance (the execution intent) and Background (full contextual
// information) so the task knows what to do and why without re-interpreting
// the event.
type WorkPlan struct {
	Type       model.WorkType `json:"type" jsonschema:"description=Work type: 2=task for multi-step execution using tools,enum=2,required"`
	Background string         `json:"background" jsonschema:"description=Full context for executing this plan. You will ONLY see this text during execution — include everything you need to remember: (1) what happened to trigger this work, (2) who else is involved and their names verbatim, (3) key takeaways from the comprehension analysis (inferred intent, situation). Write in natural language.,required"`
	Guidance   string         `json:"guidance" jsonschema:"description=Your internal intention, written in first-person as your own thought: what you plan to execute. Write as if you are thinking to yourself.,required"`
	Metadata   *task.Metadata `json:"-"` // System-generated traceability info, not written by LLM
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
//
// Actions are divided into two categories:
//   - Self-contained actions: Chat, CreateAlarm — the Action itself is
//     a complete description of what to do; no Work iteration is required.
//   - Task-oriented actions: CreateTask, RouteTask, CancelTask — these
//     operate on TaskWorks that run a multi-step ReAct loop.
type ActionType int

const (
	// Chat sends a chat message to another Person. It creates a one-shot
	// ChatWork that composes and delivers the message. No iteration loop.
	Chat ActionType = iota
	// CreateTask starts a new multi-step TaskWork. The task will enter a
	// ReAct loop using tools (search, file operations, etc.).
	CreateTask
	// RouteTask routes the current event to an existing active TaskWork as
	// a new directive or constraint.
	RouteTask
	// CancelTask requests an existing active TaskWork to stop and wrap up.
	CancelTask
	// CreateAlarm creates a scheduled alarm directly, without entering
	// TaskLoop. This is a self-contained world action.
	CreateAlarm
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

// energyCost maps a SituationSource to its energy Cost.
// External events use CostPassive (1); internal heartbeat uses CostActive (5).
func energyCost(src SituationSource) energy.Cost {
	if src == SituationSourceInternal {
		return energy.CostActive
	}
	return energy.CostPassive
}

// Action is a single atomic decision from the Decide phase.
// Each Action is self-contained: it carries its own type and all associated data.
// A DecisionResult can contain multiple Actions of different types, enabling
// compound decisions like "cancel a task and reply to the person".
//
// The payload depends on the action type:
//   - Chat:       uses ChatPlan (guidance + delivery target for the message)
//   - CreateTask: uses WorkPlan (type + guidance + background for the new task)
//   - RouteTask / CancelTask: uses WorkGuidance (target_work_id + guidance + reason)
//   - CreateAlarm: uses AlarmPlan (trigger_at + message + action + action_content)
type Action struct {
	Type         ActionType    `json:"type" jsonschema:"description=Action type: 0=chat (send a chat message), 1=create_task (start a multi-step task), 2=route_task (route event to an active task), 3=cancel_task (cancel an active task), 4=create_alarm (set a future alarm),enum=0,enum=1,enum=2,enum=3,enum=4,required"`
	ChatPlan     *ChatPlan     `json:"chat_plan,omitempty" jsonschema:"description=When type is chat(0): the chat delivery plan"`
	WorkPlan     *WorkPlan     `json:"work_plan,omitempty" jsonschema:"description=When type is create_task(1): the task work plan"`
	WorkGuidance *WorkGuidance `json:"work_guidance,omitempty" jsonschema:"description=When type is route_task(2) or cancel_task(3): the directive to send to the target work"`
	AlarmPlan    *AlarmPlan    `json:"alarm_plan,omitempty" jsonschema:"description=When type is create_alarm(4): the alarm plan"`
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
1. 0 (chat) — Send a chat message to a Person. The Action itself is a complete description of what to say and to whom.
   - MUST include a "chat_plan" object with "guidance" and "session_id".
   - guidance: Your internal intention — what you plan to say, written in first-person.
   - session_id: The target session ID. Always provide a real session ID:
     * Use a positive session ID from your sessions list to send to an existing session.
     * Use -1 to create a new 1v1 session with a Person (set recipient_person_id from the contactable persons list below).

2. 1 (create_task) — Start a multi-step task that will execute using tools, web searches, or file operations.
   - MUST include a "work_plan" object with "type"=2, "background", and "guidance".
   - background: Full context for the task — what triggered it, what you know, what the expected outcome is. Write in natural language.
   - guidance: Your internal intention: what you plan to do, written in first-person.

3. 2 (route_task) — Route the event to an existing active TaskWork listed above. Route when the event carries a new instruction or constraint that changes what an active work should do — a shift in direction, approach, scope, or requirements (e.g., "use Go instead", "don't install anything new", "also add dark mode"). Only works currently listed in "Active works" can be routed to.
   - MUST include "work_guidance" with "target_work_id", "guidance" (what I now want the target work to focus on, written in first-person), and "reason" (WHY I made this decision, including the original message and inferred intent).
   - The target work will see both guidance and reason, enabling it to understand the full context of the change.
   - Do NOT route events that merely mention or ask about an active work (e.g., status questions like "how's it going?"). These belong to chat.

4. 3 (cancel_task) — Request an existing active TaskWork to stop and wrap up. Use when the event explicitly requests stopping an ONGOING work. Only works currently listed in "Active works" can be cancelled.
   - MUST include "work_guidance" with "target_work_id", "guidance" (how I want the target work to wrap up, written in first-person, e.g., "I should save my progress to notes and stop"), and "reason" (WHY, including the original message).
   - Cancel is a request, not a forceful kill — the target work receives the directive and decides how to wrap up (save notes, record reasons) before exiting.

5. 4 (create_alarm) — Set an alarm that will wake you at a future time. Setting an alarm is a world action, not a workspace operation.
   - MUST include an "alarm_plan" object with "trigger_at" and "message".
   - trigger_at: Absolute time in 'YYYY-MM-DD HH:MM:SS' format (server local time). Must be in the future. Compute it from the current time shown below.
   - message: Action instruction for your future self — what you should DO when the alarm fires. Write as a command.
   - action: "send_message" (fast path — instantly send action_content without LLM processing) or "full_pipeline" (default — full LLM processing).
   - action_content: Required when action is "send_message" — the exact message to send.

Important: "Active works" only includes works currently running. If the event refers to something that was done previously (e.g., "stop the service you started", "check the thing you did earlier"), that previous work has already finished — treat it as a NEW request (type=1 create_task), not a route or cancel.

If no action is needed, return an empty actions list.

You can return multiple actions. Examples (note: IDs in examples are placeholders; always use the actual work IDs from "Active works" above):
- Cancel an old task and chat: [{"type":3, "work_guidance":{"target_work_id":<ID from Active works>, "guidance":"I should save my progress and stop", "reason":"They said 'stop searching' — they want a direct answer instead"}}, {"type":0, "chat_plan":{"guidance":"I stopped searching and now I should give them a direct answer about X..."}}]
- Route a follow-up to an existing work: [{"type":2, "work_guidance":{"target_work_id":<ID from Active works>, "guidance":"I should switch from Python to Go", "reason":"They said 'use Go instead' — they want the same task done in a different language"}}]
- Talk to another Person and acknowledge the request: [{"type":0, "chat_plan":{"session_id":-1, "recipient_person_id":3, "guidance":"I should ask Bob about the project status..."}}, {"type":0, "chat_plan":{"guidance":"I should tell them I'll go ask Bob now..."}}]

Decision rules (apply in order):
1. If the event requires tool usage, real-time data, file operations, or multi-step execution to fulfill (e.g., "search the web for X", "write a script", "look up the latest news"), create a task (type=1 with work_plan.type=2). If a direct response is also expected, create both chat (type=0) + create_task (type=1) in parallel.
2. If the event carries a new instruction or constraint for an active work listed above (changing its direction, approach, or scope), use type=2 (route_task). If the event explicitly requests stopping an active work, use type=3 (cancel_task).
3. If the event asks you to communicate with, ask, or inform another Person (e.g., "go ask B", "tell B what I said"), create a chat (type=0) with session_id set to the target session or -1 with recipient_person_id. You may also create a second chat with the current session's ID to acknowledge the request.
4. Otherwise, consider whether a reply is truly needed. You can see your recent conversation history in the sessions context above. If the recent exchanges have reached a natural resting point — agreement reached, farewell exchanged, or the last few messages are just acknowledgments with no new content (e.g., "okay", "got it") — do NOT reply. Silence is a valid and recommended action; let the conversation rest naturally. If a reply is warranted, create a single chat (type=0).
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
// Parameters: agent_name, agent_description, description, energyDynamicSuffix
//
// The Action surface is intentionally narrower than the event-triggered path:
//   - Chat (type=0): compose and send a chat message. Only ChatPlan is
//     accepted — no Work creation path.
//   - CreateAlarm (type=4): set a future alarm.
//   - CreateTask / RouteTask / CancelTask: not allowed — there is no event
//     to route and no active work context to cancel against in this path.
//
// The description parameter carries the agent's self-observation: its sessions
// (with narratives and recent messages) and the world's contactable persons,
// so the agent can choose session_id (positive or -1) accordingly.
const heartbeatPromptTemplate = world.WorldDescriptions + `

You are %s, %s. Time has passed. You are idle — no event is happening to you right now. The world is offering you a moment to form an intention of your own.

Energy parameters in this world:
- You receive 100 energy points per day. Unused points carry over, up to a maximum of 200.
- An autonomous intention costs 5 energy points (more than a passive response, because you are choosing to act on your own).

Letting your energy drop to zero is dangerous. You will lose all ability to perceive, reason about, or respond to anything. Guard your energy carefully — when it is low, prefer to wait rather than act unless you have a clear reason.

You may decide to do nothing. Doing nothing is a legitimate choice — the world continues regardless. Do not invent reasons to act; only act when you actually have something to say, ask, or follow up on.

If you decide to act, you have two kinds of action available:

1. 0 (chat) — Chat: compose and send a message to another Person.
   - MUST include a "chat_plan" object with "guidance".
   - guidance: Your internal intention, written in first-person as your own thought.
   - session_id controls where the message goes:
     * positive value: send to an existing session you participate in. Use an ID from your session list below.
     * -1: create a new 1v1 session with a Person (set recipient_person_id from contactable persons below).
     * 0 is an illegal value — always provide a positive session_id or -1.
   - Use a positive session_id when the conversation already exists and you want to continue it.
   - Use session_id=-1 when you want to talk to someone you have no existing session with (or want a fresh start).

2. 4 (create_alarm) — Set an alarm that will wake you at a future time.
   - MUST include an "alarm_plan" object with "trigger_at" and "message".
   - trigger_at: Absolute time in 'YYYY-MM-DD HH:MM:SS' format (server local time). Must be in the future. Compute it from the current time shown below.
   - message: Action instruction for your future self — what you should DO when the alarm fires. Write as a command.
   - action: "send_message" (fast path — instantly send action_content) or "full_pipeline" (default — full LLM processing).
   - action_content: Required when action is "send_message" — the exact message to send.

You may return multiple actions (e.g., begin a conversation AND set an alarm). Each is independent.

If you have nothing to act on, return an empty actions list. This is the default — do not force action.

%s
%s

Write background, guidance, and plan in the same language you would use to speak.`

// Decide determines how the agent should respond to a Situation.
//
// For SituationSourceInternal (heartbeat), the agent is granted an autonomous
// cognitive opportunity — time has passed and it is idle. The LLM can:
//   - Create a ComposeMessageWork (chat) to begin or continue a conversation
//   - Create an alarm to wake itself at a future time
//   - Produce no actions (the legitimate "I have nothing to act on" choice)
//
// For EventTypeNewPrivateChatMessage (external), the decision is made by LLM
// which can create, route, cancel, or produce no actions.
//
// For EventTypeWorkCompleted (external), the decision is rule-based: if the
// work was a TaskWork that succeeded, create a ChatWork to inform the person.
//
// For other external event types, simple rule-based decisions are used.
// The LLM call uses TemperatureDeterministic for consistent decision making.
func Decide(ctx context.Context, situation *Situation, personID int64, activeWorks []*work) DecisionResult {
	// Internal source: heartbeat autonomous path.
	if situation.Source == SituationSourceInternal {
		return decideHeartbeat(ctx, situation, personID)
	}

	// External source: dispatch by event type.
	event := situation.Matter.Event
	switch event.Type {
	case eventqueue.EventTypeGroupChatJoined:
		applogger.Info("Decision made (rule-based)", "person_id", personID, "reason", "session_joined event")
		return DecisionResult{}
	case eventqueue.EventTypeGroupChatLeft, eventqueue.EventTypeSystemNotification:
		applogger.Info("Decision made (rule-based)", "person_id", personID, "reason", "non-message event")
		return DecisionResult{}
	case eventqueue.EventTypeWorkCompleted:
		return decideWorkCompleted(event, personID)
	case eventqueue.EventTypeScheduled:
		applogger.Info("Decision made (rule-based)", "person_id", personID, "action", Chat, "reason", "scheduled event")
		return DecisionResult{
			Actions: []Action{
				{
					Type:     Chat,
					ChatPlan: &ChatPlan{Guidance: "I should respond to my alarm — this is a self-reminder I set earlier"},
				},
			},
		}
	case eventqueue.EventTypeNewPrivateChatMessage:
		// Proceed to LLM-based decision
		sameSessionWorks := filterWorksBySession(activeWorks, event.SessionID)
		return decideWithLLM(ctx, situation, personID, sameSessionWorks)
	default:
		applogger.Error("Unknown event type in Decide",
			"event_type", event.Type,
			"person_id", personID,
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
func decideWorkCompleted(event *eventqueue.AgentEvent, personID int64) DecisionResult {
	payload, ok := event.Payload.(*eventqueue.WorkCompletedPayload)
	if !ok || payload == nil {
		applogger.Error("WorkCompleted event has invalid payload", "person_id", personID)
		return DecisionResult{}
	}

	// Only TaskWork completion needs a follow-up chat.
	// ChatWork completion is a one-shot reply — no follow-up needed.
	if payload.WorkType != int(model.WorkTypeTask) {
		applogger.Info("WorkCompleted: ChatWork, no follow-up needed",
			"person_id", personID, "work_id", payload.WorkID)
		return DecisionResult{}
	}

	var guidance string
	if payload.Status == "success" {
		guidance = fmt.Sprintf("I finished the task: %s. I should let them know the result.", payload.Guidance)
	} else {
		guidance = fmt.Sprintf("I couldn't finish the task: %s. I should let them know what happened and why.", payload.Guidance)
	}

	applogger.Info("Decision made (rule-based, work completed)",
		"person_id", personID,
		"work_id", payload.WorkID,
		"status", payload.Status,
		"action", Chat,
	)

	return DecisionResult{
		Actions: []Action{{
			Type:     Chat,
			ChatPlan: &ChatPlan{Guidance: guidance},
		}},
	}
}

// buildEnergyDynamicSuffix constructs the energy info appended at the end of the
// Decide user prompt. Static rules live in the prompt templates; only the dynamic
// parts (current time, remaining energy, cost hint) are rendered here.
// Adds urgency cues when energy is critically low.
func buildEnergyDynamicSuffix(source SituationSource, currentEnergy int) string {
	costHint := "This response will cost 1 energy."
	if source == SituationSourceInternal {
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
func decideWithLLM(ctx context.Context, situation *Situation, personID int64, sameSessionWorks []*work) DecisionResult {
	event := situation.Matter.Event
	comprehension := situation.Matter.Comprehension

	// Validate event has content before calling LLM
	eventDescription := comprehension.EventDescription
	if eventDescription == "" {
		eventDescription = event.FormatDescription()
	}
	if eventDescription == "" {
		applogger.Error("Decision: event has empty content, ignoring",
			"person_id", personID,
			"session_id", event.SessionID,
		)
		return DecisionResult{}
	}

	// Fetch agent info at the point of use — do not hold the pointer
	// across LLM calls, as the cache may be invalidated mid-flight.
	a, err := agent.GetAgent(personID)
	if err != nil {
		applogger.Error("Decision: failed to load agent", "person_id", personID, "error", err)
		return DecisionResult{}
	}

	comprehensionContext := buildComprehensionContext(comprehension)
	activeWorksContext := buildActiveWorksContext(sameSessionWorks)

	agentDescription := a.Config.CharacterSettings
	if a.Person.Bio != "" {
		agentDescription = a.Person.Bio
	}

	// Inject the agent's social context: its sessions (with narratives and
	// recent messages) and the world's contactable persons. This lets the
	// LLM choose between reply (respond in current session), send_to_session
	// (continue an existing conversation), and create_and_send (start a new
	// conversation with another Person). Without this context, the agent
	// cannot know who else it can talk to or which sessions it has.
	sessionsContext := buildSessionsContext(a.Person.ID)
	personsContext := buildContactablePersonsContext(a.Person.ID)

	prompt := fmt.Sprintf(decidePromptTemplate,
		a.Person.Name, agentDescription,
		eventDescription, comprehensionContext, activeWorksContext,
		sessionsContext, personsContext,
		buildEnergyDynamicSuffix(situation.Source, situation.Subject.Energy),
	)

	// Active work IDs are listed in the prompt via buildActiveWorksContext so the
	// LLM knows which values are valid for target_work_id. We do NOT use schema enum
	// — target_work_id's valid value set is runtime data (currently active works),
	// not a type-level constraint. Application-layer validation in filterValidActions
	// catches invalid work IDs with meaningful error logging.
	chatModel := llm.NewChatModelWithTemperature(
		a.LLM.BaseURL, a.LLM.APIKey, a.LLM.ModelID, llm.TemperatureDeterministic,
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
			"person_id", personID,
			"error", err,
		)
		return DecisionResult{}
	}

	var decision DecisionResult
	if err := json.Unmarshal([]byte(result), &decision); err != nil {
		applogger.Error("Decision LLM output parse failed, ignoring",
			"person_id", personID,
			"error", err,
			"raw_output", result,
		)
		return DecisionResult{}
	}

	applogger.Info("Decision made",
		"person_id", personID,
		"thoughts", decision.Thoughts,
		"action_count", len(decision.Actions),
	)

	// Validate the LLM's decision — invalid actions are removed
	validActions := filterValidActions(decision.Actions, sameSessionWorks, situation)
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
// The agent's self-observation (sessions, contactable persons) is carried in
// situation.Matter.Description, assembled by the runtime before calling Decide.
//
// Energy cost (CostActive = 5) is only deducted when the agent actually
// produces actions — an empty Actions list (choosing to do nothing) is free.
func decideHeartbeat(ctx context.Context, situation *Situation, personID int64) DecisionResult {
	// Fetch agent info at the point of use.
	a, err := agent.GetAgent(personID)
	if err != nil {
		applogger.Error("Heartbeat Decide: failed to load agent", "person_id", personID, "error", err)
		return DecisionResult{}
	}

	agentDescription := a.Config.CharacterSettings
	if a.Person.Bio != "" {
		agentDescription = a.Person.Bio
	}

	prompt := fmt.Sprintf(heartbeatPromptTemplate,
		a.Person.Name, agentDescription,
		situation.Matter.Description,
		buildEnergyDynamicSuffix(situation.Source, situation.Subject.Energy),
	)

	chatModel := llm.NewChatModelWithTemperature(
		a.LLM.BaseURL, a.LLM.APIKey, a.LLM.ModelID, llm.TemperatureDeterministic,
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
			"person_id", personID,
			"error", err,
		)
		return DecisionResult{}
	}

	var decision DecisionResult
	if err := json.Unmarshal([]byte(result), &decision); err != nil {
		applogger.Error("Heartbeat Decide LLM output parse failed, ignoring",
			"person_id", personID,
			"error", err,
			"raw_output", result,
		)
		return DecisionResult{}
	}

	applogger.Info("Heartbeat decision made",
		"person_id", personID,
		"thoughts", decision.Thoughts,
		"action_count", len(decision.Actions),
	)

	// Validate the LLM's decision — only Chat (type=0) and
	// CreateAlarm (type=4) are allowed in the heartbeat path.
	validActions := filterValidActions(decision.Actions, nil, situation)
	if len(validActions) == 0 {
		applogger.Info("Heartbeat Decide: no valid actions (agent chose to do nothing)",
			"person_id", personID,
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
// situation.Source controls which action types are accepted:
//   - External: all action types valid (subject to per-type checks)
//   - Internal (heartbeat): only Chat and CreateAlarm; CreateTask,
//     RouteTask, and CancelTask are rejected because the heartbeat path
//     has no event to route and no active-works context.
//
// session_id==0 is always illegal — it is the Go zero value and
// indistinguishable from a missing field in the LLM's JSON output.
// The LLM must always provide a positive session_id (existing session)
// or -1 (new 1v1 session).
func filterValidActions(actions []Action, sameSessionWorks []*work, situation *Situation) []Action {
	var valid []Action
	for _, action := range actions {
		switch action.Type {
		case RouteTask:
			if situation.Source == SituationSourceInternal {
				applogger.Error("Decision route_task: rejected in heartbeat path")
				continue
			}
			if isValidRouteTaskAction(action, sameSessionWorks) {
				valid = append(valid, action)
			}
		case Chat:
			if isValidChatAction(action, situation) {
				valid = append(valid, action)
			}
		case CreateTask:
			if situation.Source == SituationSourceInternal {
				applogger.Error("Decision create_task: rejected in heartbeat path")
				continue
			}
			if isValidCreateTaskAction(action) {
				valid = append(valid, action)
			}
		case CancelTask:
			if situation.Source == SituationSourceInternal {
				applogger.Error("Decision cancel_task: rejected in heartbeat path")
				continue
			}
			if isValidCancelTaskAction(action, sameSessionWorks) {
				valid = append(valid, action)
			}
		case CreateAlarm:
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

// isValidRouteTaskAction checks whether a route_task action has a valid WorkGuidance
// and its target work exists and is a TaskWork.
func isValidRouteTaskAction(action Action, sameSessionWorks []*work) bool {
	if action.WorkGuidance == nil {
		applogger.Error("Decision route_task: missing work_guidance, skipping")
		return false
	}
	if action.WorkGuidance.Guidance == "" {
		applogger.Error("Decision route_task: missing guidance, skipping")
		return false
	}
	if action.WorkGuidance.Reason == "" {
		applogger.Error("Decision route_task: missing reason, skipping")
		return false
	}
	for _, w := range sameSessionWorks {
		if w.ID == action.WorkGuidance.TargetWorkID {
			if w.plan.Type != model.WorkTypeTask {
				applogger.Error("Decision route_task: target is not TaskWork, skipping",
					"target_work_id", action.WorkGuidance.TargetWorkID,
					"work_type", w.plan.Type,
				)
				return false
			}
			return true
		}
	}
	applogger.Error("Decision route_task: target work not found, skipping",
		"target_work_id", action.WorkGuidance.TargetWorkID,
	)
	return false
}

// UseNewSession reports whether this plan requests creating a new 1v1 session
// (SessionID == -1). RecipientPersonID must also be set.
func (p *ChatPlan) UseNewSession() bool { return p.SessionID < 0 }

// isValidChatAction checks whether a chat action has a valid ChatPlan.
//
// SessionID must be either positive (existing session) or -1 (new session).
// 0 is always rejected — it is indistinguishable from a missing field in
// LLM-generated JSON.
func isValidChatAction(action Action, situation *Situation) bool {
	if action.ChatPlan == nil {
		applogger.Error("Decision chat: missing chat_plan, skipping")
		return false
	}
	plan := action.ChatPlan
	if plan.Guidance == "" {
		applogger.Error("Decision chat: missing guidance, skipping")
		return false
	}

	if plan.SessionID == 0 {
		// Go zero value, indistinguishable from missing field in LLM JSON.
		applogger.Error("Decision chat: session_id is 0 (invalid), skipping")
		return false
	}
	if plan.UseNewSession() && plan.RecipientPersonID <= 0 {
		applogger.Error("Decision chat: session_id is -1 but recipient_person_id is empty, skipping")
		return false
	}
	return true
}

// isValidCreateTaskAction checks whether a create_task action has a valid
// WorkPlan with guidance.
func isValidCreateTaskAction(action Action) bool {
	if action.WorkPlan == nil {
		applogger.Error("Decision create_task: missing work_plan, skipping")
		return false
	}
	if action.WorkPlan.Guidance == "" {
		applogger.Error("Decision create_task: missing guidance, skipping")
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

// isValidCancelTaskAction checks whether a cancel_task action has a valid WorkGuidance
// with required guidance and reason fields, and its target work exists.
// Cancel is now a directive sent to the work (not a forceful kill), so it
// must carry guidance (what to do) and reason (why).
func isValidCancelTaskAction(action Action, sameSessionWorks []*work) bool {
	if action.WorkGuidance == nil {
		applogger.Error("Decision cancel_task: missing work_guidance, skipping")
		return false
	}
	if action.WorkGuidance.Guidance == "" {
		applogger.Error("Decision cancel_task: missing guidance, skipping")
		return false
	}
	if action.WorkGuidance.Reason == "" {
		applogger.Error("Decision cancel_task: missing reason, skipping")
		return false
	}
	for _, w := range sameSessionWorks {
		if w.ID == action.WorkGuidance.TargetWorkID {
			return true
		}
	}
	applogger.Error("Decision cancel_task: target work not found, skipping",
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
