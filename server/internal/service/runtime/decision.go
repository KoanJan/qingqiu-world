package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"qingqiu-world-server/internal/dops"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/comprehend"
	"qingqiu-world-server/internal/service/energy"
	"qingqiu-world-server/internal/service/eventqueue"
	"qingqiu-world-server/internal/service/llm"
	"qingqiu-world-server/internal/service/task"
	"qingqiu-world-server/internal/service/workspace"

	applogger "qingqiu-world-server/internal/logger"
)

// WorkPlan describes a single unit of work to be created by the runtime.
// Decide produces one or more WorkPlans, each carrying the execution intent
// (Guidance) and the full contextual background (Background) so the Work
// knows what to do and why without re-interpreting the event.
//
// This design ensures the cognitive order is preserved:
// Comprehend (understand) → Decide (judge + plan) → Work (execute the plan).
type WorkPlan struct {
	Type       model.WorkType `json:"type" jsonschema:"description=Work type: 1=chat for direct reply, 2=task for multi-step execution using tools,enum=1,enum=2,required"`
	Background string         `json:"background" jsonschema:"description=Full context for executing this plan. You will ONLY see this text during execution — include everything you need to remember: (1) what happened to trigger this work, (2) who else is involved and their names verbatim, (3) key takeaways from the comprehension analysis (inferred intent, situation). Write in natural language.,required"`
	Guidance   string         `json:"guidance" jsonschema:"description=Your internal intention, written in first-person as your own thought: what you plan to say (chat) or what you plan to execute (task). Write as if you are thinking to yourself.,required"`
	Metadata   *task.Metadata `json:"-"` // System-generated traceability info, not written by LLM
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
)

// TriggerSource indicates what triggered this Decide call, used to:
//   - Pick the energy cost (CostPassive for eventqueue events, CostActive
//     for heartbeat-driven active behavior — reserved for future versions)
//   - Render the cost hint in the system message
//
// In 0.1.1, Decide is only invoked from the event loop, so the source is
// always TriggerSourceEvent.
type TriggerSource int

const (
	// TriggerSourceEvent means Decide was triggered by an eventqueue event
	// (passive response). Energy cost: CostPassive (1).
	TriggerSourceEvent TriggerSource = iota
	// TriggerSourceHeartbeat means Decide was triggered by a heartbeat
	// (active behavior). Energy cost: CostActive (5). Reserved for future
	// versions — no callers in 0.1.1.
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
//   - ActionCreate:  uses WorkPlan (type + guidance for the new work)
//   - ActionRoute:   uses WorkGuidance (target_work_id + guidance + reason)
//   - ActionCancel:  uses WorkGuidance (target_work_id + guidance + reason)
type Action struct {
	Type         ActionType    `json:"type" jsonschema:"description=Action type: 0=create new work, 1=route to existing work, 2=cancel existing work,enum=0,enum=1,enum=2,required"`
	WorkPlan     *WorkPlan     `json:"work_plan,omitempty" jsonschema:"description=When type is create(0): the work plan to instantiate"`
	WorkGuidance *WorkGuidance `json:"work_guidance,omitempty" jsonschema:"description=When type is route(1) or cancel(2): the directive to send to the target work"`
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
// Parameters: agent_name, agent_description, message_content, comprehension_context, activeWorksContext, energyDynamicSuffix
const decidePromptTemplate = `You are %s, %s. Your job is to decide how to handle incoming events.

You have a limited energy budget each day:
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
   - Work type 1 (chat): reply to the person directly. Use for simple Q&A, greetings, casual chat.
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

Important: "Active works" only includes works currently running. If the event refers to something that was done previously (e.g., "stop the service you started", "check the thing you did earlier"), that previous work has already finished — treat it as a NEW request (type=0 create), not a route or cancel.

If no action is needed, return an empty actions list.

You can return multiple actions. Examples (note: IDs in examples are placeholders; always use the actual work IDs from "Active works" above):
- Cancel an old task and create a new one: [{"type":2, "work_guidance":{"target_work_id":<ID from Active works>, "guidance":"I should save my progress and stop", "reason":"They said 'stop searching' — they want a direct answer instead"}}, {"type":0, "work_plan":{"type":1, "background":"Alice just told me to stop searching and give a direct answer. She originally asked about X.","guidance":"I stopped searching and now I should give them a direct answer about X..."}}]
- Route a follow-up to an existing work: [{"type":1, "work_guidance":{"target_work_id":<ID from Active works>, "guidance":"I should switch from Python to Go", "reason":"They said 'use Go instead' — they want the same task done in a different language"}}]

Decision rules (apply in order):
1. If the comprehension says "needs world interaction: true", the event requires tool usage or multi-step execution. Create a task work (type=0 with work_plan.type=2). If a direct response is also expected, create both chat + task in parallel.
2. If the event carries a new instruction or constraint for an active work listed above (changing its direction, approach, or scope), use type=1 (route). If the event explicitly requests stopping an active work, use type=2 (cancel).
3. If the comprehension says "needs world interaction: false" and no active work is relevant, create a single chat work (type=0 with work_plan.type=1).
4. When in doubt and no comprehension hint is available, prefer a single chat work.

---

Event: %s

%s%s
%s

Write background, guidance, reason, and plan in the same language as the event content.`

// Decide determines how the agent should respond to an event.
//
// For EventTypeNewMessage, the decision is made by LLM which can:
//   - Create new Work(s) with WorkPlans
//   - Route the event to an existing active Work
//   - Cancel an existing active Work
//   - Produce no actions (implicit ignore)
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
		// Proceed to LLM-based decision below
	default:
		applogger.Error("Unknown event type in Decide",
			"event_type", event.Type,
			"agent_config_id", ac.ID,
		)
		return DecisionResult{}
	}

	sameSessionWorks := filterWorksBySession(activeWorks, event.SessionID)

	// Use LLM to decide — it can create, route, cancel, or produce no actions
	return decideWithLLM(ctx, event, ac, llmConfig, comprehension, sameSessionWorks, triggerSource, agentState)
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
	eventDescription := event.FormatDescription()
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

	prompt := fmt.Sprintf(decidePromptTemplate, dops.GetAgentConfigName(ac.ID), agentDescription, eventDescription, comprehensionContext, activeWorksContext, buildEnergyDynamicSuffix(triggerSource, agentState))

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
	validActions := filterValidActions(decision.Actions, sameSessionWorks)
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

// filterValidActions filters out invalid actions from the LLM decision.
// Pure validation — no modifications, only checks and logging.
func filterValidActions(actions []Action, sameSessionWorks []*work) []Action {
	var valid []Action
	for _, action := range actions {
		switch action.Type {
		case ActionRoute:
			if isValidRouteAction(action, sameSessionWorks) {
				valid = append(valid, action)
			}
		case ActionCreate:
			if isValidCreateAction(action) {
				valid = append(valid, action)
			}
		case ActionCancel:
			if isValidCancelAction(action, sameSessionWorks) {
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

// isValidCreateAction checks whether a create action has a work plan with guidance.
func isValidCreateAction(action Action) bool {
	if action.WorkPlan == nil {
		applogger.Error("Decision create: missing work_plan, skipping")
		return false
	}
	if action.WorkPlan.Guidance == "" {
		applogger.Error("Decision create: missing guidance, skipping")
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

	if comprehension.QueryType != "" {
		parts = append(parts, fmt.Sprintf("Query type: %s", comprehension.QueryType))
	}

	if comprehension.PersonState != nil {
		if comprehension.PersonState.Purpose != "" {
			parts = append(parts, fmt.Sprintf("Inferred intent: %s", comprehension.PersonState.Purpose))
		}
		if comprehension.PersonState.Situation != "" {
			parts = append(parts, fmt.Sprintf("Situation context: %s", comprehension.PersonState.Situation))
		}
	}

	if comprehension.NeedsWorldInteraction {
		parts = append(parts, "Needs world interaction: true (message involves tools, external data, or multi-step execution)")
	}

	if comprehension.NeedsClarification {
		parts = append(parts, "Needs clarification: true (query is vague)")
	}

	if len(parts) == 0 {
		return ""
	}

	return fmt.Sprintf("Comprehension analysis:\n%s\n\n", strings.Join(parts, "\n"))
}
