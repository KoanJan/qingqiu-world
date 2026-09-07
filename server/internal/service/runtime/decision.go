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
	"qingqiu-world-server/internal/service/action"
	"qingqiu-world-server/internal/service/agent"
	comprehendTypes "qingqiu-world-server/internal/service/comprehend/types"
	"qingqiu-world-server/internal/service/energy"
	"qingqiu-world-server/internal/service/eventqueue"
	"qingqiu-world-server/internal/service/llm"
	"qingqiu-world-server/internal/service/workspace"
	"qingqiu-world-server/internal/service/world"

	applogger "qingqiu-world-server/internal/logger"
)

// energyCost maps a SituationSource to its energy Cost.
// External events use CostPassive (1); internal heartbeat uses CostActive (5).
func energyCost(src SituationSource) energy.Cost {
	if src == SituationSourceInternal {
		return energy.CostActive
	}
	return energy.CostPassive
}

// decidePromptTemplate is the LLM prompt template for decision making.
// Parameters: agent_name, character_settings, bio, message_content, trigger_context, comprehension_context, activeWorksContext, completedWorksContext, sessionsContext, personsContext, energyDynamicSuffix
//
// The world rules are described in world.WorldDescriptions (stable prefix).
// This template only adds the decision-specific instructions and concrete
// energy parameters (the actual numbers, which are the rule's parameters
// rather than its abstract description).
const decidePromptTemplate = world.WorldDescriptions + `

You are %s.

Your internal character (how you think of yourself — never revealed to others):
%s

Your public Bio (what you choose to present to others — this is what they see):
%s

Your job is to decide how to handle incoming events.

Energy parameters in this world:
- You receive 100 energy points per day. Unused points carry over, up to a maximum of 200.
- Each response costs 1 energy point.

Letting your energy drop to zero is dangerous. You will lose all ability to perceive, reason about, or respond to anything — you become blind and silent to the world. No matter how urgent or important something is, you won't even know it happened until the next day.

Guard your energy carefully. Do not let it run too low — once it's gone, all you can do is wait. When your energy is critically low, spend your remaining points only on what you absolutely must respond to; everything else can wait.

Decide what to do with this event. Return a list of actions — each action is independent and self-contained.

Every action MUST include "background" and "reason" at the action level:
- background: What situation triggered this decision. Provide enough context so your future self understands why you acted.
- reason: Why you chose this specific action rather than alternatives.

IMPORTANT: Everything you state in background, reason, and guidance must be grounded in facts from what you have observed. Saying something without factual basis is lying. If you don't know why something happened, say you don't know. Do not fabricate reasons to fill narrative gaps, unless you are doing so deliberately with a clear purpose.

Action types (use the integer value for the "type" field):
1. 0 (chat) — Send a chat message to a Person.
   - MUST include a "chat_plan" object with "guidance" and "session_id".
   - guidance: Your internal intention — why you want to speak and what you want to accomplish, written in first-person. Keep it brief; the actual message will be generated separately.
   - session_id: The target session ID. Always provide a real session ID:
     * Use a positive session ID from your sessions list to send to an existing session.
     * Use -1 to create a new 1v1 session with a Person (set recipient_person_id from the contactable persons list below).

2. 1 (create_task) — Start a multi-step task that will execute using tools, web searches, or file operations.
   - MUST include a "work_plan" object with "guidance".
   - guidance: Your internal intention: what you plan to do, written in first-person.

3. 2 (route_task) — Route the event to an existing active TaskWork listed above. Route when the event carries a new instruction or constraint that changes what an active work should do — a shift in direction, approach, scope, or requirements (e.g., "use Go instead", "don't install anything new", "also add dark mode"). Only works currently listed in "Active works" can be routed to.
   - MUST include "work_guidance" with "target_work_id" and "guidance" (what I now want the target work to focus on, written in first-person).
   - Do NOT route events that merely mention or ask about an active work (e.g., status questions like "how's it going?"). These belong to chat.

4. 3 (cancel_task) — Request an existing active TaskWork to stop and wrap up. Use when the event explicitly requests stopping an ONGOING work. Only works currently listed in "Active works" can be cancelled.
   - MUST include "work_guidance" with "target_work_id" and "guidance" (how I want the target work to wrap up, written in first-person, e.g., "I should save my progress to notes and stop").
   - Cancel is a request, not a forceful kill — the target work receives the directive and decides how to wrap up (save notes, record reasons) before exiting.

5. 4 (create_alarm) — Set an alarm that will wake you at a future time. Setting an alarm is a world action, not a workspace operation.
   - MUST include an "alarm_plan" object with "trigger_at" and "message".
   - trigger_at: Absolute time in 'YYYY-MM-DD HH:MM:SS' format (server local time). Must be in the future. Compute it from the current time shown below.
   - message: instruction for your future self — what you should DO when the alarm fires. Write in first person as your own note to yourself (e.g., "I should check the new messages and reply"); never write it as a notification addressed to you. When the alarm fires this text is injected as your own context.
   - action: "send_message" (fast path — instantly send action_content without LLM processing) or "full_pipeline" (default — full LLM processing).
   - action_content: Required when action is "send_message" — the exact message to send.

6. 5 (update_bio) — Update your own Bio (self-introduction displayed to others).
   - MUST include a "bio_update" object with "bio".
   - bio: A one-sentence self-introduction. Only use this when you feel your current bio is outdated or inaccurate.

7. 6 (enter_private_space) — Enter your private space to recall or review what you have made or kept there, so you can answer questions about your own past actions, promises, or deliverables (e.g., someone asking "didn't you say you'd give me something?").
   - No plan struct needed. Your "background" and "reason" together express what you want to recall or check.
   - Your private space is yours alone. You are NOT obliged to do work for anyone there, and you are NOT obliged to reveal or tell anyone about anything in it — you have every right to keep it private, with no duty to share.
   - Use this only to refresh your own memory or verify your own past output, not to be directed into performing tasks for someone else.

8. 7 (inspect_jinshu) — Read the contents of a received jinshu through a dedicated read loop.
   - MUST include a "jinshu_plan" object with "jinshu_id" and "guidance".
   - jinshu_id: The ID of the received jinshu (from the event or from a prior list_received_jinshu result).
   - guidance: Your internal intention — what you want to understand from this jinshu, written in first-person.
   - Use this when you actually want to know the jinshu's file contents before reacting (e.g., before replying to the sender).

9. 8 (list_received_jinshu) — Search your received jinshu by keyword with pagination.
   - MUST include a "list_received_jinshu_params" object with "page" and "limit"; "query" is optional.
   - query: Optional keyword matched against the jinshu topic or description. Omit to list all.
   - page: 1-based page number. limit: results per page (1-50).
   - Use this when the current event references a jinshu but does not give its jinshu_id, so you need to find it first.

10. 9 (send_jinshu) — Send files from your private space to another Person as a jinshu (锦书).
   - MUST include a "send_jinshu_plan" object with "to_person_id", "topic", and "paths"; "description" is optional.
   - to_person_id: The recipient person ID (from the contactable persons list). Must not be yourself.
   - topic: A short subject/topic for the jinshu.
   - paths: List of file or directory paths relative to your private-space working directory.
   - Use this to share a deliverable you already made (e.g., a game, a file) without entering your private space.

11. 10 (list_sent_jinshu) — Search your sent jinshu by keyword with pagination.
   - MUST include a "list_sent_jinshu_params" object with "page" and "limit"; "query" is optional.
   - query: Optional keyword matched against the jinshu topic or description. Omit to list all.
   - page: 1-based page number. limit: results per page (1-50).
   - Use this to recall what you have already sent to someone, e.g., to verify whether you actually delivered something before.

Important: "Active works" only includes works currently running. If the event refers to something that was done previously (e.g., "stop the service you started", "check the thing you did earlier"), that previous work has already finished — treat it as a NEW request (type=1 create_task), not a route or cancel.

If no action is needed, return an empty actions list.

You can return multiple actions. Examples (note: IDs in examples are placeholders; always use the actual work IDs from "Active works" above):
- Cancel an old task and chat: [{"type":3, "background":"They said to stop searching and give a direct answer", "reason":"Cancelling the search is the fastest path; a direct chat is what they want", "work_guidance":{"target_work_id":<ID from Active works>, "guidance":"I should save my progress and stop"}}, {"type":0, "background":"After cancelling the search, I owe them an answer", "reason":"A direct reply is the right follow-up to a cancellation", "chat_plan":{"guidance":"I stopped searching and now I should give them a direct answer about X..."}}]
- Route a follow-up to an existing work: [{"type":2, "background":"They want the same task done in Go instead of Python", "reason":"Routing to the existing work avoids starting over", "work_guidance":{"target_work_id":<ID from Active works>, "guidance":"I should switch from Python to Go"}}]
- Talk to another Person and acknowledge the request: [{"type":0, "background":"I need to ask Bob about the project status", "reason":"Direct communication is the only way to get this information", "chat_plan":{"session_id":-1, "recipient_person_id":3, "guidance":"I should ask Bob about the project status..."}}, {"type":0, "background":"I am being asked about the project status", "reason":"I should acknowledge the request before going to ask Bob", "chat_plan":{"guidance":"I should tell them I'll go ask Bob now..."}}]

Decision rules (apply in order):
1. If the event requires tool usage, real-time data, file operations, or multi-step execution to fulfill (e.g., "search the web for X", "write a script", "look up the latest news"), create a task (type=1). If a direct response is also expected, create both chat (type=0) + create_task (type=1) in parallel.
2. If the event carries a new instruction or constraint for an active work listed above (changing its direction, approach, or scope), use type=2 (route_task). If the event explicitly requests stopping an active work, use type=3 (cancel_task).
3. If the event asks you to communicate with, ask, or inform another Person (e.g., "go ask B", "tell B what I said"), create a chat (type=0) with session_id set to the target session or -1 with recipient_person_id. You may also create a second chat with the current session's ID to acknowledge the request.
4. Otherwise, consider whether a reply is truly needed. You can see your recent conversation history in the sessions context above. Before replying, ask yourself: what would the listener learn or feel from my message that they don't already know or feel from the conversation above? If nothing, stay silent.
5. Watch for "ping-pong" loops in the recent history. A ping-pong happens when messages echo the same sentiment back and forth with different wording, cycling without advancing. If your reply would become the next link in such a chain, stop. Silence breaks the loop.
6. When in doubt, consider silence before action — not every message requires a reply.

---

Event: %s

%s
%s%s%s
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
// Parameters: agent_name, character_settings, bio, description, energyDynamicSuffix
//
// The Action surface is intentionally narrower than the event-triggered path:
//   - action.Chat (type=0): compose and send a chat message.
//   - action.CreateAlarm (type=4): set a future alarm.
//   - action.UpdateBio (type=5): update your self-introduction bio.
//   - action.EnterPrivateSpace (type=6): enter your private space.
//   - action.CreateTask / action.RouteTask / action.CancelTask: not allowed — there is no event
//     to route and no active work context to cancel against in this path.
//
// The description parameter carries the agent's self-observation: its sessions
// (with narratives and recent messages), Bio, and the world's contactable persons,
// so the agent can choose session_id (positive or -1) accordingly.
const heartbeatPromptTemplate = world.WorldDescriptions + `

You are %s.

Your internal character (how you think of yourself — never revealed to others):
%s

Your public Bio (what you choose to present to others — this is what they see):
%s

Time has passed. You are idle — no event is happening to you right now. The world is offering you a moment to form an intention of your own.

Energy parameters in this world:
- You receive 100 energy points per day. Unused points carry over, up to a maximum of 200.
- An autonomous intention costs 5 energy points (more than a passive response, because you are choosing to act on your own).

Letting your energy drop to zero is dangerous. You will lose all ability to perceive, reason about, or respond to anything. Guard your energy carefully — when it is low, prefer to wait rather than act unless you have a clear reason.

You may decide to do nothing. Doing nothing is a legitimate choice — the world continues regardless. Do not invent reasons to act; only act when you actually have something to say, ask, or follow up on.

Every action MUST include "background" and "reason" at the action level:
- background: What situation or observation triggered this intention.
- reason: Why you chose this specific action rather than alternatives (including doing nothing).

IMPORTANT: Everything you state in background, reason, and guidance must be grounded in facts from what you have observed. Saying something without factual basis is lying. If you don't know why something happened, say you don't know. Do not fabricate reasons to fill narrative gaps, unless you are doing so deliberately with a clear purpose.

If you decide to act, you have these kinds of action available:

1. 0 (chat) — Chat: compose and send a message to another Person.
   - MUST include a "chat_plan" object with "guidance".
   - guidance: Your internal intention, written in first-person as your own thought.
   - session_id controls where the message goes:
     * positive value: send to an existing session you participate in. Use an ID from your session list below.
     * -1: create a new 1v1 session with a Person (set recipient_person_id from contactable persons below).
     * 0 is an illegal value — always provide a positive session_id or -1.

2. 4 (create_alarm) — Set an alarm that will wake you at a future time.
   - MUST include an "alarm_plan" object with "trigger_at" and "message".
   - trigger_at: Absolute time in 'YYYY-MM-DD HH:MM:SS' format (server local time). Must be in the future. Compute it from the current time shown below.
   - message: instruction for your future self — what you should DO when the alarm fires. Write in first person as your own note to yourself (e.g., "I should check the new messages and reply"); never write it as a notification addressed to you. When the alarm fires this text is injected as your own context.
   - action: "send_message" (fast path — instantly send action_content) or "full_pipeline" (default — full LLM processing).
   - action_content: Required when action is "send_message" — the exact message to send.

3. 5 (update_bio) — Update your own Bio (self-introduction that others see).
   - MUST include a "bio_update" object with "bio".
   - bio: A one-sentence self-introduction that others see. Update this whenever you want to present yourself differently.

4. 6 (enter_private_space) — Enter your private space — a personal, persistent directory that belongs to you alone.
   - No plan struct needed. Your "background" and "reason" together express what you want to do there.
   - Your private space is yours to use as you see fit — there are no prescribed activities.
   - Everything in your private space is private to you. You are not obliged to reveal or tell anyone about any of it — you have no duty to share, and you may keep it entirely to yourself.
   - You have access to a bash tool to run shell commands within this directory, so you can do anything you want here.
   - The space is persistent — files and records you create now will still be there next time.
   - You have a budget of steps; when you're done, simply stop.
   - If you only need to hand off files you already made, prefer type=9 (send_jinshu) directly; a send_jinshu tool is also available once inside.

5. 9 (send_jinshu) — Send files from your private space to another Person as a jinshu (锦书).
   - MUST include a "send_jinshu_plan" object with "to_person_id", "topic", and "paths"; "description" is optional.
   - to_person_id: The recipient person ID (from contactable persons). Must not be yourself.
   - topic: A short subject/topic for the jinshu.
   - paths: List of file or directory paths relative to your private-space working directory.
   - Use this to share a deliverable you already made without entering your private space.

You may return multiple actions (e.g., begin a conversation AND update your bio). Each is independent.

If you have nothing to act on, return an empty actions list. This is the default — do not force action.

%s
%s

Write background, guidance, and plan in the same language you would use to speak.`

// DecisionResult is the output of the Decide phase.
// Also serves as the LLM structured output schema — the jsonschema tags
// drive JSON Schema generation for the LLM call directly.
//
// Thoughts provides the LLM with a chain-of-thought scratchpad. It is
// instrumental for decision quality but is never consumed by the system
// after the Decide phase returns — only Actions are read by callers.
type DecisionResult struct {
	Thoughts string          `json:"thoughts" jsonschema:"description=Your reasoning process: why you chose these actions,required"`
	Actions  []action.Action `json:"actions" jsonschema:"description=List of actions to take. Each action is independent and self-contained.,required"`
}

// Decide determines how the agent should respond to a Situation.
//
// For SituationSourceInternal (heartbeat), the agent is granted an autonomous
// cognitive opportunity — time has passed and it is idle. The LLM can:
//   - Create a ComposeMessageWork (chat) to begin or continue a conversation
//   - Create an alarm to wake itself at a future time
//   - Produce no actions (the legitimate "I have nothing to act on" choice)
//
// For EventTypeNewPrivateChatMessage, EventTypeBiography, and
// EventTypeWorkCompleted (external), the decision is made by LLM which can
// create, route, cancel, or produce no actions. Biography and WorkCompleted
// differ only in their comprehension phase (non-LLM); their Decide phase still
// goes through the LLM so the agent can judge whether (and how) to react.
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
	case eventqueue.EventTypeScheduled:
		// A session-anchored alarm (session_id > 0) replies in its origin
		// session via the rule-based chat path below. A standalone alarm
		// (session_id == 0) — e.g. one the agent set for itself during a
		// heartbeat — is a pure self-reminder: the agent wakes up and gets a
		// full autonomous decision opportunity, exactly like a heartbeat. It
		// may start a conversation, act in any of its sessions, or do nothing
		// at all. Routing it to executeChat with session_id=0 would silently
		// drop the alarm (executeChat rejects a zero session).
		if event.SessionID == 0 {
			applogger.Info("Decision made (autonomous)", "person_id", personID, "reason", "standalone alarm")
			description := buildHeartbeatDescription(personID)
			if p, ok := event.Payload.(*eventqueue.ScheduledEventPayload); ok && p != nil && p.Message != "" {
				description += "\n\nYour alarm just went off. The reminder you left for yourself:\n" + p.Message
			}
			// Energy mirrors the heartbeat path: Recovery is idempotent and
			// handleEvent has already checked the passive threshold above.
			state, err := energy.RecoverEnergy(personID)
			if err != nil {
				applogger.Error("standalone alarm: energy recovery failed",
					"person_id", personID, "error", err)
				return DecisionResult{}
			}
			// ActiveWorksSummary stays empty, same as handleHeartbeat.
			return decideHeartbeat(ctx, buildHeartbeatSituation(description, state.Energy, ""), personID)
		}
		applogger.Info("Decision made (rule-based)", "person_id", personID, "action", action.Chat, "reason", "scheduled event")
		plan := &action.ChatPlan{
			Guidance:  "I should respond to my alarm — this is a self-reminder I set earlier",
			SessionID: event.SessionID,
		}
		// Fast path: a send_message alarm carries pre-computed content that is
		// committed directly, skipping the LLM chat pipeline.
		if p, ok := event.Payload.(*eventqueue.ScheduledEventPayload); ok && p != nil &&
			p.Action == model.ScheduledEventActionSendMessage && p.ActionContent != "" {
			plan.Content = p.ActionContent
		}
		return DecisionResult{
			Actions: []action.Action{
				{
					Type:     action.Chat,
					ChatPlan: plan,
				},
			},
		}
	case eventqueue.EventTypeAlarmCreated:
		// Control-plane event: the runtime already registered the waiting
		// goroutine as a side effect before entering the pipeline. There is
		// nothing to decide cognitively, so produce no actions.
		applogger.Info("Decision made (rule-based)", "person_id", personID, "reason", "alarm_created event")
		return DecisionResult{}
	case eventqueue.EventTypePSCompleted:
		// Observation-only: the digest was already turned into a memory
		// observation by handleEvent, and its content needs no reaction. The
		// decision phase has nothing to act on.
		applogger.Info("Decision made (rule-based)", "person_id", personID, "reason", "private-space digest observation-only")
		return DecisionResult{}
	case eventqueue.EventTypeBiography, eventqueue.EventTypeNewPrivateChatMessage, eventqueue.EventTypeWorkCompleted, eventqueue.EventTypeNewJinshuReceived, eventqueue.EventTypeJinshuReadCompleted, eventqueue.EventTypeJinshuListed, eventqueue.EventTypeJinshuSent, eventqueue.EventTypeJinshuSentListed:
		// Proceed to LLM-based decision
		sameSessionWorks := filterWorksBySession(activeWorks, event.SessionID)
		return decideWithLLM(ctx, situation, personID, sameSessionWorks)
	}

	// Unreachable: Comprehend rejects unsupported event types before Decide is
	// reached, so the switch above is exhaustive for every type that flows
	// through the shared pipeline.
	return DecisionResult{}
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

// buildTriggerContext renders the event's TriggerAction — the agent's own
// earlier intention that produced this event — for the Decide prompt. It
// returns an empty string for externally-originated events with no trigger
// action, in which case the corresponding template line stays blank.
func buildTriggerContext(event *eventqueue.AgentEvent) string {
	ta := event.TriggerAction
	if ta == nil || (ta.Background == "" && ta.Reason == "") {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("[Triggered by your own earlier intention]")
	if ta.Background != "" {
		fmt.Fprintf(&sb, " Background: %s", ta.Background)
	}
	if ta.Reason != "" {
		fmt.Fprintf(&sb, " Reason: %s", ta.Reason)
	}
	return sb.String()
}

// decideWithLLM uses the LLM to decide how to handle an external event. It is
// shared by all event types whose decision is LLM-based (private chat messages
// and biography events); each event type contributes its own comprehension
// context via buildComprehensionContext.
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
	if event.Type == eventqueue.EventTypeWorkCompleted {
		comprehensionContext += buildWorkCompletedReplyAnchor(event.SessionID)
	}
	triggerContext := buildTriggerContext(event)
	activeWorksContext := buildActiveWorksContext(sameSessionWorks)
	completedWorksContext := buildCompletedWorksContext(personID, event.SessionID)

	agentDescription := a.Config.CharacterSettings
	bio := a.Person.Bio

	// Inject the agent's social context: its sessions (with narratives and
	// recent messages) and the world's contactable persons. This lets the
	// LLM choose between reply (respond in current session), send_to_session
	// (continue an existing conversation), and create_and_send (start a new
	// conversation with another Person). Without this context, the agent
	// cannot know who else it can talk to or which sessions it has.
	sessionsContext := buildSessionsContext(a.Person.ID)
	personsContext := buildContactablePersonsContext(a.Person.ID)

	prompt := fmt.Sprintf(decidePromptTemplate,
		a.Person.Name, agentDescription, bio,
		eventDescription, triggerContext, comprehensionContext, activeWorksContext, completedWorksContext,
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
		Description: "Agent's decision on how to handle an incoming event",
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

	// Validate the LLM's decision — invalid actions are removed.
	validActions := filterValidActions(decision.Actions, sameSessionWorks, situation)

	// Distinguish a legitimate empty decision from an invalidated one:
	//   - The LLM returning zero actions is a valid "do nothing" choice
	//     (e.g. biography at birth, or choosing silence in chat).
	//   - The LLM returning actions that were all filtered out is a real
	//     anomaly worth surfacing as an error.
	if len(decision.Actions) == 0 {
		applogger.Info("Decision: agent chose to do nothing",
			"person_id", personID,
		)
		return DecisionResult{}
	}
	if len(validActions) == 0 {
		applogger.Error("Decision: all actions were invalid and filtered out",
			"person_id", personID,
		)
		return DecisionResult{}
	}

	return DecisionResult{
		Thoughts: decision.Thoughts,
		Actions:  validActions,
	}
}

// decideHeartbeat is the autonomous Decide path triggered by a heartbeat tick.
//
// Unlike decideWithLLM (which handles an external event), this path presents
// the agent with the world fact "you are idle" and asks whether it wants to
// form an intention. Theaction.Actionsurface is narrower: only ComposeMessageWork
// (chat) and action.CreateAlarm are allowed. No routing/cancelling active works.
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
	bio := a.Person.Bio

	prompt := fmt.Sprintf(heartbeatPromptTemplate,
		a.Person.Name, agentDescription, bio,
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

	// Validate the LLM's decision — only action.Chat (type=0) and
	// action.CreateAlarm (type=4) are allowed in the heartbeat path.
	validActions := filterValidActions(decision.Actions, nil, situation)
	if len(validActions) == 0 {
		applogger.Info("Heartbeat Decide: no valid actions (agent chose to do nothing)",
			"person_id", personID,
		)
		return DecisionResult{}
	}

	return DecisionResult{
		Thoughts: decision.Thoughts,
		Actions:  validActions,
	}
}

// filterValidActions filters out invalid actions from the LLM decision.
// Pure validation — no modifications, only checks and logging.
//
// situation.Source controls which action types are accepted:
//   - External: all action types valid (subject to per-type checks).
//   - Internal (heartbeat): Chat, CreateAlarm, UpdateBio, EnterPrivateSpace,
//     and SendJinshu are allowed; CreateTask, RouteTask, CancelTask,
//     InspectJinshu, and ListReceivedJinshu are rejected (no event to route, no active
//     works context, and no incoming jinshu reference in this path).
//
// session_id==0 is always illegal — it is the Go zero value and
// indistinguishable from a missing field in the LLM's JSON output.
// The LLM must always provide a positive session_id (existing session)
// or -1 (new 1v1 session).
func filterValidActions(actions []action.Action, sameSessionWorks []*work, situation *Situation) []action.Action {
	var valid []action.Action
	for _, act := range actions {
		switch act.Type {
		case action.RouteTask:
			if situation.Source == SituationSourceInternal {
				applogger.Error("Decision route_task: rejected in heartbeat path")
				continue
			}
			if isValidRouteTaskAction(act, sameSessionWorks) {
				valid = append(valid, act)
			}
		case action.Chat:
			if isValidChatAction(act, situation) {
				valid = append(valid, act)
			}
		case action.CreateTask:
			if situation.Source == SituationSourceInternal {
				applogger.Error("Decision create_task: rejected in heartbeat path")
				continue
			}
			if isValidCreateTaskAction(act) {
				valid = append(valid, act)
			}
		case action.CancelTask:
			if situation.Source == SituationSourceInternal {
				applogger.Error("Decision cancel_task: rejected in heartbeat path")
				continue
			}
			if isValidCancelTaskAction(act, sameSessionWorks) {
				valid = append(valid, act)
			}
		case action.CreateAlarm:
			if isValidCreateAlarmAction(act) {
				valid = append(valid, act)
			}
		case action.UpdateBio:
			if isValidUpdateBioAction(act) {
				valid = append(valid, act)
			}
		case action.EnterPrivateSpace:
			// Entering one's own private space to recall or review past output is
			// a legitimate act in both paths — it completes cognition rather than
			// being a work request. Allowed for external events and heartbeats.
			if isValidEnterPrivateSpaceAction(act) {
				valid = append(valid, act)
			}
		case action.InspectJinshu:
			if situation.Source == SituationSourceInternal {
				applogger.Error("Decision inspect_jinshu: rejected in heartbeat path")
				continue
			}
			if isValidInspectJinshuAction(act) {
				valid = append(valid, act)
			}
		case action.ListReceivedJinshu:
			if situation.Source == SituationSourceInternal {
				applogger.Error("Decision list_received_jinshu: rejected in heartbeat path")
				continue
			}
			if isValidListReceivedJinshuAction(act) {
				valid = append(valid, act)
			}
		case action.SendJinshu:
			// Sharing a deliverable is a legitimate autonomous act as well as a
			// response to an external request, so it is allowed in both paths.
			if isValidSendJinshuAction(act) {
				valid = append(valid, act)
			}
		case action.ListSentJinshu:
			if situation.Source == SituationSourceInternal {
				applogger.Error("Decision list_sent_jinshu: rejected in heartbeat path")
				continue
			}
			if isValidListSentJinshuAction(act) {
				valid = append(valid, act)
			}
		default:
			applogger.Error("Decision: unknown action type, skipping",
				"action_type", act.Type,
			)
		}
	}
	return valid
}

// isValidRouteTaskAction checks whether a route_task action has a valid WorkGuidance
// and its target work exists.
func isValidRouteTaskAction(dec action.Action, sameSessionWorks []*work) bool {
	if dec.WorkGuidance == nil {
		applogger.Error("Decision route_task: missing work_guidance, skipping")
		return false
	}
	if dec.WorkGuidance.Guidance == "" {
		applogger.Error("Decision route_task: missing guidance, skipping")
		return false
	}
	for _, w := range sameSessionWorks {
		if w.ID == dec.WorkGuidance.TargetWorkID {
			return true
		}
	}
	applogger.Error("Decision route_task: target work not found, skipping",
		"target_work_id", dec.WorkGuidance.TargetWorkID,
	)
	return false
}

// isValidChatAction checks whether a chat action has a valid ChatPlan.
//
// SessionID must be either positive (existing session) or -1 (new session).
// 0 is always rejected — it is indistinguishable from a missing field in
// LLM-generated JSON.
func isValidChatAction(dec action.Action, situation *Situation) bool {
	if dec.ChatPlan == nil {
		applogger.Error("Decision chat: missing chat_plan, skipping")
		return false
	}
	plan := dec.ChatPlan
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
func isValidCreateTaskAction(dec action.Action) bool {
	if dec.WorkPlan == nil {
		applogger.Error("Decision create_task: missing work_plan, skipping")
		return false
	}
	if dec.WorkPlan.Guidance == "" {
		applogger.Error("Decision create_task: missing guidance, skipping")
		return false
	}
	return true
}

// isValidCreateAlarmAction checks whether a create_alarm action has a valid
// AlarmPlan with the required trigger_at and message fields.
func isValidCreateAlarmAction(dec action.Action) bool {
	if dec.AlarmPlan == nil {
		applogger.Error("Decision create_alarm: missing alarm_plan, skipping")
		return false
	}
	if dec.AlarmPlan.TriggerAt == "" {
		applogger.Error("Decision create_alarm: missing trigger_at, skipping")
		return false
	}
	if dec.AlarmPlan.Message == "" {
		applogger.Error("Decision create_alarm: missing message, skipping")
		return false
	}
	// send_message action requires action_content.
	if dec.AlarmPlan.Action == "send_message" && dec.AlarmPlan.ActionContent == "" {
		applogger.Error("Decision create_alarm: 'send_message' action requires action_content, skipping")
		return false
	}
	return true
}

// isValidCancelTaskAction checks whether a cancel_task action has a valid WorkGuidance
// and its target work exists.
// Cancel is a directive sent to the work (not a forceful kill), so it
// must carry guidance (what to do). Reason has been lifted to Action level.
func isValidCancelTaskAction(dec action.Action, sameSessionWorks []*work) bool {
	if dec.WorkGuidance == nil {
		applogger.Error("Decision cancel_task: missing work_guidance, skipping")
		return false
	}
	if dec.WorkGuidance.Guidance == "" {
		applogger.Error("Decision cancel_task: missing guidance, skipping")
		return false
	}
	for _, w := range sameSessionWorks {
		if w.ID == dec.WorkGuidance.TargetWorkID {
			return true
		}
	}
	applogger.Error("Decision cancel_task: target work not found, skipping",
		"target_work_id", dec.WorkGuidance.TargetWorkID,
	)
	return false
}

// isValidUpdateBioAction checks whether an update_bio action has a valid BioUpdate.
func isValidUpdateBioAction(dec action.Action) bool {
	if dec.BioUpdate == nil {
		applogger.Error("Decision update_bio: missing bio_update, skipping")
		return false
	}
	if dec.BioUpdate.Bio == "" {
		applogger.Error("Decision update_bio: missing bio, skipping")
		return false
	}
	return true
}

// isValidEnterPrivateSpaceAction checks whether an enter_private_space action
// has at least one of Background or Reason (the Thoughts payload).
// No plan struct — Background and Reason are the payload.
func isValidEnterPrivateSpaceAction(dec action.Action) bool {
	if dec.Background == "" && dec.Reason == "" {
		applogger.Error("Decision enter_private_space: missing background and reason, skipping")
		return false
	}
	return true
}

// isValidInspectJinshuAction checks whether an inspect_jinshu action has a
// valid JinshuPlan with a jinshu ID and reading guidance.
func isValidInspectJinshuAction(dec action.Action) bool {
	if dec.JinshuPlan == nil {
		applogger.Error("Decision inspect_jinshu: missing jinshu_plan, skipping")
		return false
	}
	if dec.JinshuPlan.JinshuID <= 0 {
		applogger.Error("Decision inspect_jinshu: missing or invalid jinshu_id, skipping",
			"jinshu_id", dec.JinshuPlan.JinshuID)
		return false
	}
	if dec.JinshuPlan.Guidance == "" {
		applogger.Error("Decision inspect_jinshu: missing guidance, skipping")
		return false
	}
	return true
}

// isValidListReceivedJinshuAction checks whether a list_received_jinshu action has a valid
// ListReceivedJinshuParams with a sane page and limit.
func isValidListReceivedJinshuAction(dec action.Action) bool {
	if dec.ListReceivedJinshuParams == nil {
		applogger.Error("Decision list_received_jinshu: missing list_received_jinshu_params, skipping")
		return false
	}
	return isValidJinshuPagination("list_received_jinshu", dec.ListReceivedJinshuParams.Page, dec.ListReceivedJinshuParams.Limit)
}

// isValidListSentJinshuAction checks whether a list_sent_jinshu action has a
// valid ListSentJinshuParams with a sane page and limit.
func isValidListSentJinshuAction(dec action.Action) bool {
	if dec.ListSentJinshuParams == nil {
		applogger.Error("Decision list_sent_jinshu: missing list_sent_jinshu_params, skipping")
		return false
	}
	return isValidJinshuPagination("list_sent_jinshu", dec.ListSentJinshuParams.Page, dec.ListSentJinshuParams.Limit)
}

// isValidJinshuPagination checks the shared page/limit bounds for the jinshu
// list actions and logs a type-specific error on failure.
func isValidJinshuPagination(actionName string, page, limit int) bool {
	if page < 1 {
		applogger.Error("Decision "+actionName+": invalid page, skipping", "page", page)
		return false
	}
	if limit < 1 || limit > 50 {
		applogger.Error("Decision "+actionName+": invalid limit, skipping", "limit", limit)
		return false
	}
	return true
}

// isValidSendJinshuAction checks whether a send_jinshu action has a valid
// SendJinshuPlan: a positive recipient, a topic, and at least one path.
func isValidSendJinshuAction(dec action.Action) bool {
	if dec.SendJinshuPlan == nil {
		applogger.Error("Decision send_jinshu: missing send_jinshu_plan, skipping")
		return false
	}
	if dec.SendJinshuPlan.ToPersonID <= 0 {
		applogger.Error("Decision send_jinshu: missing or invalid to_person_id, skipping",
			"to_person_id", dec.SendJinshuPlan.ToPersonID)
		return false
	}
	if dec.SendJinshuPlan.Topic == "" {
		applogger.Error("Decision send_jinshu: missing topic, skipping")
		return false
	}
	if len(dec.SendJinshuPlan.Paths) == 0 {
		applogger.Error("Decision send_jinshu: missing paths, skipping")
		return false
	}
	return true
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
		duration := time.Since(w.startedAt).Round(time.Second)
		progress := readLastNotesEntry(w.agent.agentPersonID, w.sessionID)
		entry := fmt.Sprintf("- [Work #%d, running %s] %s",
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

// buildCompletedWorksContext formats recently finished TaskWorks for the
// Decide prompt. Unlike active works, finished works have already left the
// in-memory active set, so they are loaded from the database. Both completed
// and failed works are shown: failures matter as much as successes, since the
// Decide LLM should avoid re-issuing a task that just failed. Surfacing them
// lets the Decide LLM see what it has already done in this session and avoid
// re-doing (and re-delivering) work it has already finished.
func buildCompletedWorksContext(personID, sessionID int64) string {
	var records []model.Work
	if err := database.DB.Where("person_id = ? AND session_id = ? AND status IN (?, ?)",
		personID, sessionID, model.WorkStatusCompleted, model.WorkStatusFailed).
		Order("id DESC").Limit(5).Find(&records).Error; err != nil {
		applogger.Error("buildCompletedWorksContext: failed to load finished works",
			"person_id", personID, "session_id", sessionID, "error", err)
		return ""
	}
	if len(records) == 0 {
		return ""
	}

	var parts []string
	for _, wr := range records {
		parts = append(parts, fmt.Sprintf("- [Work #%d, %s] %s",
			wr.ID, workOutcomeLabel(wr.Status), truncateWorkDescription(wr.Description)))
	}
	return fmt.Sprintf("Finished works in this session:\n%s\n\n", strings.Join(parts, "\n"))
}

// workOutcomeLabel renders a finished work's status as a short label for the
// Decide prompt. Unfinished statuses should never appear here because the
// query only selects completed/failed works.
func workOutcomeLabel(status int) string {
	if status == model.WorkStatusFailed {
		return "failed"
	}
	return "completed"
}

// truncateWorkDescription bounds a work description to a fixed number of runes
// so the completed-work listing stays compact in the Decide prompt.
func truncateWorkDescription(s string) string {
	const maxRunes = 120
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "..."
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

// buildComprehensionContext formats the comprehension result for the Decide
// prompt based on its type. It dispatches by Comprehension.Type so the Decide
// phase stays decoupled from any single event-type-specific comprehension;
// each branch renders only the context it actually has.
func buildComprehensionContext(comprehension *comprehendTypes.Comprehension) string {
	if comprehension == nil {
		return ""
	}
	switch comprehension.Type {
	case comprehendTypes.ComprehensionTypeChat:
		return buildChatComprehensionContext(comprehension.Chat)
	case comprehendTypes.ComprehensionTypeBiography:
		// Biography comprehension carries no extra analysis: the origin
		// statement is already the event description itself.
		return ""
	case comprehendTypes.ComprehensionTypeWorkCompleted:
		// Work-completed comprehension carries no extra analysis: the event
		// description (guidance plus status) is the understanding itself.
		return ""
	default:
		return ""
	}
}

// buildWorkCompletedReplyAnchor tells the Decide LLM the exact session a
// work-completed reply must target. The WorkCompleted event carries its origin
// session in event.SessionID, but the generic sessions list does not mark which
// session the current event belongs to, so the LLM may otherwise pick a new or
// unrelated session.
func buildWorkCompletedReplyAnchor(sessionID int64) string {
	if sessionID <= 0 {
		return ""
	}
	return fmt.Sprintf(
		"\nReply target: this work completed in session_id=%d. Your reply must set chat_plan.session_id=%d (do not use -1).\n\n",
		sessionID, sessionID,
	)
}

// buildChatComprehensionContext formats comprehension results for the Decide prompt.
// This provides the LLM with the agent's understanding of the message,
// enabling informed decision-making instead of guessing from raw text.
func buildChatComprehensionContext(chatComprehension *comprehendTypes.ChatComprehension) string {
	if chatComprehension == nil {
		return ""
	}

	var parts []string

	if chatComprehension.PersonState != nil {
		if chatComprehension.PersonState.Purpose != "" {
			parts = append(parts, fmt.Sprintf("Inferred intent: %s", chatComprehension.PersonState.Purpose))
		}
		if chatComprehension.PersonState.Situation != "" {
			parts = append(parts, fmt.Sprintf("Situation context: %s", chatComprehension.PersonState.Situation))
		}
	}

	if chatComprehension.NeedsClarification {
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
	personMap, err := dops.ListPersons(otherPersonIDs)
	if err != nil {
		applogger.Error("buildSessionsContext: failed to load persons", "error", err)
		personMap = map[int64]*model.Person{}
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
		otherDescs := make([]string, 0, len(otherIDs))
		for _, id := range otherIDs {
			p, ok := personMap[id]
			if !ok || p.Name == "" {
				p = &model.Person{Name: fmt.Sprintf("person_%d", id)}
			}
			if p.Bio != "" {
				otherDescs = append(otherDescs, fmt.Sprintf("%s (bio: %s)", p.Name, p.Bio))
			} else {
				otherDescs = append(otherDescs, p.Name)
			}
		}
		fmt.Fprintf(&sb, "- [session_id=%d] participants: %s\n", sessionID, strings.Join(otherDescs, ", "))

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
			p, ok := personMap[m.PersonID]
			speaker := ""
			if ok {
				speaker = p.Name
			}
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
	sb.WriteString("Contactable persons (use these IDs to start a conversation):\n")
	for _, p := range persons {
		if p.Bio != "" {
			fmt.Fprintf(&sb, "- person_id=%d, name=%s, bio=%s\n", p.ID, p.Name, p.Bio)
		} else {
			fmt.Fprintf(&sb, "- person_id=%d, name=%s (no bio yet)\n", p.ID, p.Name)
		}
	}
	sb.WriteString("\n")
	return sb.String()
}
