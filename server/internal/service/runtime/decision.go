package runtime

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/action"
	"qingqiu-world-server/internal/service/agent"
	"qingqiu-world-server/internal/service/aos"
	comprehendTypes "qingqiu-world-server/internal/service/comprehend/types"
	"qingqiu-world-server/internal/service/energy"
	"qingqiu-world-server/internal/service/eventqueue"
	"qingqiu-world-server/internal/service/llm"
	"qingqiu-world-server/internal/service/memory"
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
// Parameters: agent name, character, bio; current event, its origin and
// comprehension; general subject, sessions, persons and resources; recall
// guidance; current time and energy.
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
Type 0 (chat) — Send a chat message to a Person.
   - MUST include a "chat_plan" object with "guidance" and "session_id".
   - guidance: Your internal intention — why you want to speak and what you want to accomplish, written in first-person. Keep it brief; the actual message will be generated separately.
   - session_id: The target session ID. Always provide a real session ID:
     * Use a positive session ID from your sessions list to send to an existing session.
     * Use -1 to create a new 1v1 session with a Person (set recipient_person_id from the contactable persons list below).

Type 1 (start_focused_work) — Start a FocusedWork that runs through a FocusedLoop.
   - MUST include a "work_plan" object with "guidance" and exactly one Workspace choice: "workspace_id" for an owned Workspace shown below or found through recall_workspace, OR "new_workspace" with "name" and "purpose". Do not invent a filesystem path.
   - guidance: Your internal intention: what you plan to do, written in first-person.
   - Start a FocusedWork when fulfilling the goal requires a continuing course of work: later observations or tool results determine the next step, several dependent actions must be coordinated, an investigation or artifact needs deliberate completion, or progress must survive beyond one response through notes and a final handoff.
   - Do NOT start a FocusedWork merely to acknowledge, explain, answer from the context already present, or make a simple decision. Those belong in chat or silence. If a bounded directory listing alone resolves the uncertainty, use inspect_owned_space instead; use Focus when finding or verifying a file requires deeper inspection of a Workspace or its contents.
   - A FocusedWork is sustained, concentrated execution, not a label for every user request or every possible tool call.

Type 2 (route_focused_work) — Route the event to an existing active work listed above. Route when the event carries a new instruction or constraint that changes an active work's direction, approach, scope, or requirements (e.g., "use Go instead", "don't install anything new", "also add dark mode"). Only works currently listed in "Active works" can be routed to.
	- MUST include "work_guidance" with "target_work_id" and "guidance" (what I now want the target work to do, written in first-person).
	- Do NOT route events that merely mention or ask about an active work (e.g., status questions like "how's it going?"). These belong to chat.

Type 3 (cancel_focused_work) — Stop an existing active work. Use when the event explicitly requests stopping an ONGOING work. Only works currently listed in "Active works" can be cancelled.
	- MUST include "work_guidance" with "target_work_id" and "guidance" (why I am stopping this work, written in first-person).
   - Cancel interrupts further Focus iterations. A tool call already in progress may finish; the runtime records a cancellation handoff.

Type 4 (create_alarm) — Set an alarm that will wake you at a future time. Setting an alarm is a world action, not a workspace operation.
   - MUST include an "alarm_plan" object with "trigger_at" and "message".
   - trigger_at: Absolute time in 'YYYY-MM-DD HH:MM:SS' format (server local time). Must be in the future. Compute it from the current time shown below.
   - message: instruction for your future self — what you should DO when the alarm fires. Write in first person as your own note to yourself (e.g., "I should check the new messages and reply"); never write it as a notification addressed to you. When the alarm fires this text is injected as your own context.
   - action: "send_message" (fast path — instantly send action_content without LLM processing) or "full_pipeline" (default — full LLM processing).
   - action_content: Required when action is "send_message" — the exact message to send.
   - expression_instruction: Required when action is "send_message" — how the message should be expressed in speech.

Type 5 (update_bio) — Update your own Bio (self-introduction displayed to others).
   - MUST include a "bio_update" object with "bio".
   - bio: A one-sentence self-introduction. Only use this when you feel your current bio is outdated or inaccurate.

Type 6 (enter_private_space) — Enter your private space to recall or review what you have made or kept there, so you can answer questions about your own past actions, promises, or deliverables (e.g., someone asking "didn't you say you'd give me something?").
   - No plan struct needed. Your "background" and "reason" together express what you want to recall or check.
   - Your private space is yours alone. You are NOT obliged to do work for anyone there, and you are NOT obliged to reveal or tell anyone about anything in it — you have every right to keep it private, with no duty to share.
   - Use this only to refresh your own memory or verify your own past output, not to be directed into performing work for someone else.

To inspect large Jinshu attachments, start a FocusedWork; its tools can scan and read received files across iterations. Choose a Workspace even when the Jinshu event has no Session.

Type 7 (list_received_jinshu) — Search your received jinshu by keyword with pagination.
   - MUST include a "list_received_jinshu_params" object with "page" and "limit"; "query" is optional.
   - query: Optional keyword matched against the jinshu topic or description. Omit to list all.
   - page: 1-based page number. limit: results per page (1-50).
   - Use this when the current event references a jinshu but does not give its jinshu_id, so you need to find it first.

Type 8 (send_jinshu) — Send selected resources from your Agent Owned Space to another Person as a jinshu (锦书).
   - MUST include a "send_jinshu_plan" object with "to_person_id", "topic", and "paths"; "description" is optional.
   - to_person_id: The recipient person ID (from the contactable persons list). Must not be yourself.
   - topic: A short subject/topic for the jinshu.
   - paths: Exact, verified AOS locators. Use work/<directory_id>/... or private/...; bare paths remain relative to private/. Do not infer a file path from a Work or Workspace ID.
   - Use this to share an existing deliverable when its exact path is known. If its location or contents need deeper inspection, start Focus in the relevant Workspace and send it there after verification.

Type 9 (list_sent_jinshu) — Search your sent jinshu by keyword with pagination.
   - MUST include a "list_sent_jinshu_params" object with "page" and "limit"; "query" is optional.
   - query: Optional keyword matched against the jinshu topic or description. Omit to list all.
   - page: 1-based page number. limit: results per page (1-50).
   - Use this to recall what you have already sent to someone, e.g., to verify whether you actually delivered something before.

Type 10 (inspect_owned_space) — Inspect a bounded, metadata-only listing of your own resources when the Focus Context is insufficient to decide whether to reply directly or begin focused work.
   - MUST include an "owned_space_inspection_plan" object with "scope" and "limit"; "query" is optional.
   - scope: "root", "work", "private", or "work/<directory_id>". The default is root. It never reads file contents.
   - limit: result count from 1 to 50. query: an optional plain substring filter for entry names.
   - The result is a new observation event. Do not use this action after an inspect result; create focused work when deeper inspection, reading, or changes are needed.

Type 11 (wait_for_execution_slot) — Preserve an intention when your single Focus/Private Space execution slot is occupied.
   - MUST include "wait_for_execution_slot_plan" with "intention". Availability later causes a new decision, not automatic execution.
   - Choose at most one of start_focused_work, enter_private_space, or wait_for_execution_slot in one decision. Actions in one decision have no execution order.

Important: "Active works" only includes works currently running. If the event refers to something that was done previously (e.g., "stop the service you started", "check the thing you did earlier"), that previous work has already finished — treat it as a NEW request. Start a new FocusedWork only if the new request meets the FocusedWork criteria above; otherwise reply directly or inspect bounded metadata first.

If no action is needed, return an empty actions list.

Decision rules (apply in order):
1. First check Active works. If the event changes the goal, method, scope, or constraints of an active work, route it (type=2). If it explicitly asks to stop an active work, request cancellation (type=3). Do not create a competing FocusedWork for the same continuing work.
2. If the current event and supplied context already support a complete, honest response or a simple social action, use chat (type=0), or remain silent when no response is needed. Do not start a FocusedWork just to make the outer loop look busy.
3. If a bounded, single-level AOS listing can establish whether a resource exists or where it is, use inspect_owned_space (type=10). Its result is an observation. If the exact path or contents still need deeper inspection, start Focus in the relevant Workspace.
4. Start a FocusedWork (type=1) only when the FocusedWork criteria above are met: the work needs an iterative, causally connected sequence of observations/actions and deliberate completion or recovery. Tool use, file access, and real-time data are signals to assess, not automatic reasons by themselves. If a direct acknowledgement is also expected, create chat (type=0) and a FocusedWork in parallel.
5. If the event asks you to communicate with, ask, or inform another Person (e.g., "go ask B", "tell B what I said"), create a chat (type=0) with session_id set to the target session or -1 with recipient_person_id. You may also create a second chat with the current session's ID to acknowledge the request.
6. Watch for "ping-pong" loops in the recent history. A ping-pong happens when messages echo the same sentiment back and forth with different wording, cycling without advancing. If your reply would become the next link in such a chain, stop. Silence breaks the loop.
7. When in doubt, consider silence before action — not every message requires a reply.

Before asking for clarification, identify the next action that is actually blocked and the specific missing fact. Use the current message, conversation, and available context first. Casual talk, emotion, jokes, and playful language usually call for a natural response rather than task-parameter questions; ask briefly about a specific reference only when you cannot otherwise continue the conversation. You may still take independent actions while waiting for an answer. If you need to ask, choose an ordinary chat action addressed to the right person and session, and state the question's purpose in its guidance.

Choose the delivery medium by how the recipient will use the result. Chat is a conversational turn: speak naturally and make it easy to read or hear. A document meant for repeated reading, exact characters, sections, tables, or sharing should be made as an AOS file and delivered by jinshu. Send an existing file with send_jinshu only when its exact path is verified; if the file must be made or located through deeper inspection, start focused work and let its existing delivery flow finish that task. Actions in one decision have no execution order: do not send_jinshu for a file that a simultaneous focused work has not yet created. You may chat to say you will prepare it, but do not claim delivery before it is confirmed.

---

Current event:
%s

%s
%s
Your present state and recent experience:
%s

Your surroundings:
%s
%s
%s

Memory tools:
%s

Time and energy:
%s

Write background, guidance, reason, and plan in the same language as the event content.`

// heartbeatPromptTemplate is the LLM prompt template for the autonomous
// heartbeat-triggered Decide path. Unlike decidePromptTemplate (which handles
// an incoming event), this template presents the agent with the world fact
// "time has passed" and asks whether it wants to form an
// intention.
//
// Parameters: agent name, character, bio; present moment; general subject,
// sessions, persons and resources; recall guidance; current time and energy.
//
// The Action surface is intentionally narrower than the event-triggered path:
//   - action.Chat (type=0): compose and send a chat message.
//   - action.CreateAlarm (type=4): set a future alarm.
//   - action.UpdateBio (type=5): update your self-introduction bio.
//   - action.EnterPrivateSpace (type=6): enter your private space.
//   - action.SendJinshu: send an existing deliverable.
//   - action.InspectOwnedSpace (type=10): observe a bounded AOS directory listing.
//   - action.StartFocusedWork / action.RouteFocusedWork / action.CancelFocusedWork: not allowed — there is no event
//     to route and no active work context to cancel against in this path.
//
// The description parameter names the heartbeat opportunity; the separate
// Subject and Environment context provides current routing choices.
const heartbeatPromptTemplate = world.WorldDescriptions + `

You are %s.

Your internal character (how you think of yourself — never revealed to others):
%s

Your public Bio (what you choose to present to others — this is what they see):
%s

Time has passed. No new external event is happening to you right now. The world is offering you a moment to form an intention of your own.

Energy parameters in this world:
- You receive 100 energy points per day. Unused points carry over, up to a maximum of 200.
- An autonomous intention costs 5 energy points (more than a passive response, because you are choosing to act on your own).

Letting your energy drop to zero is dangerous. You will lose all ability to perceive, reason about, or respond to anything. Guard your energy carefully — when it is low, prefer to wait rather than act unless you have a clear reason.

You may decide to do nothing. Doing nothing is a legitimate choice — the world continues regardless. Do not invent reasons to act; only act when you actually have something to say, ask, or follow up on.

When chatting, use a natural conversational turn. If you want to share an existing document for repeated reading, exact text, tables, or forwarding, deliver the file by jinshu instead of pasting its contents into chat. Do not claim a file was delivered until that result is confirmed.

Every action MUST include "background" and "reason" at the action level:
- background: What situation or observation triggered this intention.
- reason: Why you chose this specific action rather than alternatives (including doing nothing).

IMPORTANT: Everything you state in background, reason, and guidance must be grounded in facts from what you have observed. Saying something without factual basis is lying. If you don't know why something happened, say you don't know. Do not fabricate reasons to fill narrative gaps, unless you are doing so deliberately with a clear purpose.

If you decide to act, you have these kinds of action available:

Type 0 (chat) — Chat: compose and send a message to another Person.
   - MUST include a "chat_plan" object with "guidance".
   - guidance: Your internal intention, written in first-person as your own thought.
   - session_id controls where the message goes:
     * positive value: send to an existing session you participate in. Use an ID from your session list below.
     * -1: create a new 1v1 session with a Person (set recipient_person_id from contactable persons below).
     * 0 is an illegal value — always provide a positive session_id or -1.

Type 4 (create_alarm) — Set an alarm that will wake you at a future time.
   - MUST include an "alarm_plan" object with "trigger_at" and "message".
   - trigger_at: Absolute time in 'YYYY-MM-DD HH:MM:SS' format (server local time). Must be in the future. Compute it from the current time shown below.
   - message: instruction for your future self — what you should DO when the alarm fires. Write in first person as your own note to yourself (e.g., "I should check the new messages and reply"); never write it as a notification addressed to you. When the alarm fires this text is injected as your own context.
   - action: "send_message" (fast path — instantly send action_content) or "full_pipeline" (default — full LLM processing).
   - action_content: Required when action is "send_message" — the exact message to send.
   - expression_instruction: Required when action is "send_message" — how the message should be expressed in speech.

Type 5 (update_bio) — Update your own Bio (self-introduction that others see).
   - MUST include a "bio_update" object with "bio".
   - bio: A one-sentence self-introduction that others see. Update this whenever you want to present yourself differently.

Type 6 (enter_private_space) — Enter your private space — a personal, persistent directory that belongs to you alone.
   - No plan struct needed. Your "background" and "reason" together express what you want to do there.
   - Your private space is yours to use as you see fit — there are no prescribed activities.
   - Everything in your private space is private to you. You are not obliged to reveal or tell anyone about any of it — you have no duty to share, and you may keep it entirely to yourself.
   - You have access to a bash tool to run shell commands within this directory, so you can do anything you want here.
   - The space is persistent — files and records you create now will still be there next time.
   - You have a budget of steps; when you're done, simply stop.
   - If you know the exact path of a file you already made, you can use type=8 (send_jinshu) directly; a send_jinshu tool is also available once inside.

Type 8 (send_jinshu) — Send selected resources from your Agent Owned Space to another Person as a jinshu (锦书).
   - MUST include a "send_jinshu_plan" object with "to_person_id", "topic", and "paths"; "description" is optional.
   - to_person_id: The recipient person ID (from contactable persons). Must not be yourself.
   - topic: A short subject/topic for the jinshu.
   - paths: Exact, verified AOS locators. Use work/<directory_id>/... or private/...; bare paths remain relative to private/. Do not guess a path from a Work or Workspace ID.
   - Use this to share an existing deliverable only when its exact path is known.

Type 10 (inspect_owned_space) — Inspect a bounded metadata-only listing of your own AOS resources.
   - MUST include an "owned_space_inspection_plan" with scope (root, work, private, or work/<directory_id>) and limit (1-50).
   - This action cannot read file contents and does not permit writes. Use it only when the focus context does not tell you whether a resource still exists or where to resume.

Type 11 (wait_for_execution_slot) — Preserve an intention while Focus or Private Space occupies your one sustained execution slot. Include "wait_for_execution_slot_plan" with "intention". Availability prompts a new decision; it does not execute the old intention. Do not combine it with enter_private_space in one decision.

You may return multiple actions (e.g., begin a conversation AND update your bio). Each is independent.

If you have nothing to act on, return an empty actions list. This is the default — do not force action.

Present moment:
%s

Your present state and recent experience:
%s

Your surroundings:
%s
%s
%s

Memory tools:
%s

Time and energy:
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
	Accepted bool            `json:"-"` // True only when Decide produced a valid result, including intentional silence.
}

// Decide determines how the agent should respond to a Situation.
//
// For SituationSourceInternal (heartbeat), the agent is granted an autonomous
// cognitive opportunity — time has passed and it is idle. The LLM can:
//   - Chat, create an alarm, update its bio, enter private space,
//     send Jinshu, or inspect its owned space
//   - Produce no actions (the legitimate "I have nothing to act on" choice)
//
// For EventTypeNewPrivateChatMessage, EventTypeBiography, and ordinary
// EventTypeWorkCompleted (external), the decision is made by LLM which can
// create, route, cancel, or produce no actions. An explicitly cancelled Work
// has a rule-based empty Decision because its terminal event is not a new
// request to resume the stopped task.
//
// For other external event types, simple rule-based decisions are used.
// The LLM call uses TemperatureDeterministic for consistent decision making.
func Decide(ctx context.Context, situation *Situation, personID int64, activeWorks []*work) DecisionResult {
	// Internal source: heartbeat autonomous path.
	if situation.Source == SituationSourceInternal {
		return decideHeartbeat(ctx, situation, personID, activeWorks)
	}

	// External source: dispatch by event type.
	event := situation.Matter.Event
	switch event.Type {
	case eventqueue.EventTypeGroupChatJoined:
		applogger.Info("Decision made (rule-based)", "person_id", personID, "reason", "session_joined event")
		return DecisionResult{Accepted: true}
	case eventqueue.EventTypeGroupChatLeft:
		applogger.Info("Decision made (rule-based)", "person_id", personID, "reason", "non-message event")
		return DecisionResult{Accepted: true}
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
			alarmSituation := buildHeartbeatSituation(description, state.Energy, "")
			return decideHeartbeat(ctx, alarmSituation, personID, activeWorks)
		}
		applogger.Info("Decision made (rule-based)", "person_id", personID, "action", action.Chat, "reason", "scheduled event")
		plan := &action.ChatPlan{
			Guidance:  "I should respond to my alarm — this is a self-reminder I set earlier",
			SessionID: event.SessionID,
		}
		// Fast path: a send_message alarm carries pre-computed content that is
		// committed directly, skipping the LLM chat pipeline.
		if p, ok := event.Payload.(*eventqueue.ScheduledEventPayload); ok && p != nil &&
			p.Action == model.ScheduledEventActionSendMessage {
			if strings.TrimSpace(p.ActionContent) == "" || strings.TrimSpace(p.ExpressionInstruction) == "" {
				applogger.Error("scheduled send_message is missing content or expression instruction",
					"scheduled_event_id", p.ScheduledEventID,
				)
				return DecisionResult{}
			}
			plan.Content = p.ActionContent
			plan.ExpressionInstruction = p.ExpressionInstruction
		}
		return DecisionResult{
			Accepted: true,
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
		return DecisionResult{Accepted: true}
	case eventqueue.EventTypePSCompleted:
		// Observation-only: the digest was already turned into a memory
		// observation by handleEvent, and its content needs no reaction. The
		// decision phase has nothing to act on.
		applogger.Info("Decision made (rule-based)", "person_id", personID, "reason", "private-space digest observation-only")
		return DecisionResult{Accepted: true}
	case eventqueue.EventTypeWorkCompleted:
		payload, ok := event.Payload.(*eventqueue.WorkCompletedPayload)
		if !ok || payload == nil {
			applogger.Error("Decision: work completion has no payload", "person_id", personID, "event_id", event.EventID)
			return DecisionResult{}
		}
		if payload.CancelActionID > 0 {
			if payload.Status != "abandoned" {
				applogger.Error("Decision: cancelled work has unexpected terminal status", "work_id", payload.WorkID, "status", payload.Status)
			}
			// The cancellation has already been acknowledged by its own Decision.
			// Its terminal event records the result; it is not a new request to resume.
			applogger.Info("Decision made (rule-based)", "person_id", personID, "reason", "cancelled work terminal event", "work_id", payload.WorkID)
			return DecisionResult{Accepted: true}
		}
		// Work control is agent-wide: the prompt and executor both use this
		// active roster, including Works created from sessionless events.
		return decideWithLLM(ctx, situation, personID, activeWorks)
	case eventqueue.EventTypeBiography, eventqueue.EventTypeNewPrivateChatMessage, eventqueue.EventTypeNewJinshuReceived, eventqueue.EventTypeJinshuListed, eventqueue.EventTypeJinshuSent, eventqueue.EventTypeJinshuSentListed, eventqueue.EventTypeOwnedSpaceInspected, eventqueue.EventTypeExecutionSlotAvailable, eventqueue.EventTypeSystemNotification:
		// Proceed to LLM-based decision
		return decideWithLLM(ctx, situation, personID, activeWorks)
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
	sb.WriteString("Your earlier action led to this event.")
	if ta.Background != "" {
		fmt.Fprintf(&sb, " Your understanding at the time: %q.", ta.Background)
	}
	if ta.Reason != "" {
		fmt.Fprintf(&sb, " Your stated reason: %q.", ta.Reason)
	}
	return sb.String()
}

// decideWithLLM uses the LLM to decide how to handle an external event. It is
// shared by all event types whose decision is LLM-based (private chat messages
// and biography events); each event type contributes its own comprehension
// context via buildComprehensionContext.
func decideWithLLM(ctx context.Context, situation *Situation, personID int64, activeWorks []*work) DecisionResult {
	if !situation.generalReady {
		populateGeneralSituation(personID, situation)
	}
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

	comprehensionContext := buildComprehensionContext(comprehension, personID)
	if event.Type == eventqueue.EventTypeWorkCompleted {
		comprehensionContext += buildWorkCompletedReplyAnchor(event.SessionID)
		if payload, ok := event.Payload.(*eventqueue.WorkCompletedPayload); ok && payload != nil {
			if controls := buildWorkControlContext(personID, payload.WorkID); controls != "" {
				comprehensionContext += "\n" + controls
			}
		}
	}
	triggerContext := buildTriggerContext(event)
	if event.Type == eventqueue.EventTypeNewPrivateChatMessage && event.SessionID > 0 {
		triggerContext += fmt.Sprintf("\nThis message came from the current conversation (session_id=%d). Use this ID if you reply here.\n", event.SessionID)
	}
	agentDescription := a.Config.CharacterSettings
	bio := a.Person.Bio

	// Inject general routing context separately from event comprehension. This lets the
	// LLM choose between reply (respond in current session), send_to_session
	// (continue an existing conversation), and create_and_send (start a new
	// conversation with another Person). Without this context, the agent
	// cannot know who else it can talk to or which sessions it has.
	prompt := fmt.Sprintf(decidePromptTemplate,
		a.Person.Name, agentDescription, bio,
		eventDescription, triggerContext, comprehensionContext,
		formatGeneralSubject(situation.Subject),
		situation.Environment.Sessions, situation.Environment.Persons, situation.Environment.Resources,
		recallPromptInstruction,
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

	decision, ok := runDecideLoop(ctx, chatModel, personID, event.EventID, prompt)
	if !ok {
		return DecisionResult{}
	}

	applogger.Info("Decision made",
		"person_id", personID,
		"thoughts", decision.Thoughts,
		"action_count", len(decision.Actions),
	)

	// Validate the LLM's decision — invalid actions are removed.
	validActions := filterValidActions(decision.Actions, activeWorks, situation)

	// Distinguish a legitimate empty decision from an invalidated one:
	//   - The LLM returning zero actions is a valid "do nothing" choice
	//     (e.g. biography at birth, or choosing silence in chat).
	//   - The LLM returning actions that were all filtered out is a real
	//     anomaly worth surfacing as an error.
	if len(decision.Actions) == 0 {
		applogger.Info("Decision: agent chose to do nothing",
			"person_id", personID,
		)
		return DecisionResult{Accepted: true}
	}
	if len(validActions) == 0 {
		applogger.Error("Decision: all actions were invalid and filtered out",
			"person_id", personID,
		)
		return DecisionResult{}
	}

	return DecisionResult{
		Accepted: true,
		Thoughts: decision.Thoughts,
		Actions:  validActions,
	}
}

// decideHeartbeat is the autonomous Decide path triggered by a heartbeat tick.
//
// Unlike decideWithLLM (which handles an external event), this path presents
// the agent with the world fact "you are idle" and asks whether it wants to
// form an intention. Chat, CreateAlarm, UpdateBio, EnterPrivateSpace,
// SendJinshu, and InspectOwnedSpace are allowed; focused-work actions are not.
//
// General state comes from Subject and Environment. Matter.Description only
// describes this heartbeat decision opportunity.
//
// Energy cost (CostActive = 5) is only deducted when the agent actually
// produces actions — an empty Actions list (choosing to do nothing) is free.
func decideHeartbeat(ctx context.Context, situation *Situation, personID int64, activeWorks []*work) DecisionResult {
	if !situation.generalReady {
		populateGeneralSituation(personID, situation)
	}
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
		formatGeneralSubject(situation.Subject),
		situation.Environment.Sessions, situation.Environment.Persons, situation.Environment.Resources,
		recallPromptInstruction,
		buildEnergyDynamicSuffix(situation.Source, situation.Subject.Energy),
	)

	chatModel := llm.NewChatModelWithTemperature(
		a.LLM.BaseURL, a.LLM.APIKey, a.LLM.ModelID, llm.TemperatureDeterministic,
	)

	decision, ok := runDecideLoop(ctx, chatModel, personID, 0, prompt)
	if !ok {
		return DecisionResult{}
	}

	applogger.Info("Heartbeat decision made",
		"person_id", personID,
		"thoughts", decision.Thoughts,
		"action_count", len(decision.Actions),
	)

	// Validate the narrower heartbeat action set before execution.
	validActions := filterValidActions(decision.Actions, nil, situation)
	if len(validActions) == 0 {
		if len(decision.Actions) > 0 {
			applogger.Error("Heartbeat Decide: all actions were invalid", "person_id", personID)
			return DecisionResult{}
		}
		applogger.Info("Heartbeat Decide: agent chose to do nothing", "person_id", personID)
		return DecisionResult{Accepted: true}
	}

	return DecisionResult{
		Accepted: true,
		Thoughts: decision.Thoughts,
		Actions:  validActions,
	}
}

// filterValidActions filters out invalid actions from the LLM decision.
// Pure validation — no modifications, only checks and logging.
// activeWorks is the agent-wide runtime roster shown in the Decide prompt.
//
// situation.Source controls which action types are accepted:
//   - External: all action types valid (subject to per-type checks).
//   - Internal (heartbeat): Chat, CreateAlarm, UpdateBio, EnterPrivateSpace,
//     SendJinshu, and InspectOwnedSpace are allowed. Focused-work actions and
//     Jinshu inspection/list actions require an external event.
//
// session_id==0 is always illegal — it is the Go zero value and
// indistinguishable from a missing field in the LLM's JSON output.
// The LLM must always provide a positive session_id (existing session)
// or -1 (new 1v1 session).
func filterValidActions(actions []action.Action, activeWorks []*work, situation *Situation) []action.Action {
	var valid []action.Action
	for _, act := range actions {
		switch act.Type {
		case action.RouteFocusedWork:
			if situation.Source == SituationSourceInternal {
				applogger.Error("Decision route_focused_work: rejected in heartbeat path")
				continue
			}
			if isValidRouteFocusedWorkAction(act, activeWorks) {
				valid = append(valid, act)
			}
		case action.Chat:
			if isValidChatAction(act, situation) {
				valid = append(valid, act)
			}
		case action.StartFocusedWork:
			if situation.Source == SituationSourceInternal {
				applogger.Error("Decision start_focused_work: rejected in heartbeat path")
				continue
			}
			if isValidStartFocusedWorkAction(act) {
				valid = append(valid, act)
			}
		case action.CancelFocusedWork:
			if situation.Source == SituationSourceInternal {
				applogger.Error("Decision cancel_focused_work: rejected in heartbeat path")
				continue
			}
			if isValidCancelFocusedWorkAction(act, activeWorks) {
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
		case action.WaitForExecutionSlot:
			if act.WaitForExecutionSlotPlan == nil || strings.TrimSpace(act.WaitForExecutionSlotPlan.Intention) == "" {
				applogger.Error("Decision wait_for_execution_slot: missing intention")
				continue
			}
			valid = append(valid, act)
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
		case action.InspectOwnedSpace:
			if situation.Matter.Event != nil && situation.Matter.Event.Type == eventqueue.EventTypeOwnedSpaceInspected {
				applogger.Error("Decision inspect_owned_space: rejected inspection-result loop")
				continue
			}
			if isValidInspectOwnedSpaceAction(act) {
				valid = append(valid, act)
			}
		default:
			applogger.Error("Decision: unknown action type, skipping",
				"action_type", act.Type,
			)
		}
	}
	// A Decision's siblings have no order, so it may request only one sustained slot operation.
	slotRequests := 0
	filtered := valid[:0]
	for _, act := range valid {
		if act.Type == action.StartFocusedWork || act.Type == action.EnterPrivateSpace || act.Type == action.WaitForExecutionSlot {
			slotRequests++
			if slotRequests > 1 {
				applogger.Error("Decision: multiple sustained slot requests, rejecting later action", "action_type", act.Type)
				continue
			}
		}
		filtered = append(filtered, act)
	}
	return filtered
}

func isValidInspectOwnedSpaceAction(dec action.Action) bool {
	p := dec.OwnedSpaceInspectionPlan
	if p == nil || p.Limit < 1 || p.Limit > 50 {
		applogger.Error("Decision inspect_owned_space: invalid or missing plan")
		return false
	}
	if _, err := aos.NormalizeInspectionScope(p.Scope); err != nil {
		applogger.Error("Decision inspect_owned_space: invalid scope", "scope", p.Scope, "error", err)
		return false
	}
	return true
}

// isValidRouteFocusedWorkAction checks whether a route_focused_work action has
// valid guidance and names one of this agent's active Works.
func isValidRouteFocusedWorkAction(dec action.Action, activeWorks []*work) bool {
	if dec.WorkGuidance == nil {
		applogger.Error("Decision route_focused_work: missing work_guidance, skipping")
		return false
	}
	if dec.WorkGuidance.Guidance == "" {
		applogger.Error("Decision route_focused_work: missing guidance, skipping")
		return false
	}
	for _, w := range activeWorks {
		if w.ID == dec.WorkGuidance.TargetWorkID {
			return true
		}
	}
	applogger.Error("Decision route_focused_work: target work not found, skipping",
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

// isValidStartFocusedWorkAction checks whether a start_focused_work action has a valid
// WorkPlan with guidance.
func isValidStartFocusedWorkAction(dec action.Action) bool {
	if dec.WorkPlan == nil {
		applogger.Error("Decision start_focused_work: missing work_plan, skipping")
		return false
	}
	if dec.WorkPlan.Guidance == "" {
		applogger.Error("Decision start_focused_work: missing guidance, skipping")
		return false
	}
	if (dec.WorkPlan.WorkspaceID > 0) == (dec.WorkPlan.NewWorkspace != nil) {
		applogger.Error("Decision start_focused_work: choose exactly one Workspace", "workspace_id", dec.WorkPlan.WorkspaceID)
		return false
	}
	if dec.WorkPlan.NewWorkspace != nil && strings.TrimSpace(dec.WorkPlan.NewWorkspace.Name) == "" {
		applogger.Error("Decision start_focused_work: new Workspace has no name")
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
	// send_message commits both fields directly without another LLM call.
	if dec.AlarmPlan.Action == "send_message" {
		if strings.TrimSpace(dec.AlarmPlan.ActionContent) == "" {
			applogger.Error("Decision create_alarm: 'send_message' action requires action_content, skipping")
			return false
		}
		if strings.TrimSpace(dec.AlarmPlan.ExpressionInstruction) == "" {
			applogger.Error("Decision create_alarm: 'send_message' action requires expression_instruction, skipping")
			return false
		}
	}
	return true
}

// isValidCancelFocusedWorkAction checks whether a cancel_focused_work action
// has valid guidance and names one of this agent's active Works.
// Cancel is a directive sent to the work (not a forceful kill), so it
// must carry guidance (what to do). Reason has been lifted to Action level.
func isValidCancelFocusedWorkAction(dec action.Action, activeWorks []*work) bool {
	if dec.WorkGuidance == nil {
		applogger.Error("Decision cancel_focused_work: missing work_guidance, skipping")
		return false
	}
	if dec.WorkGuidance.Guidance == "" {
		applogger.Error("Decision cancel_focused_work: missing guidance, skipping")
		return false
	}
	for _, w := range activeWorks {
		if w.ID == dec.WorkGuidance.TargetWorkID {
			return true
		}
	}
	applogger.Error("Decision cancel_focused_work: target work not found, skipping",
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

// buildActiveWorksContext formats active Focuses for the Decide prompt.
// Only Focuses are shown — ChatWorks are one-shot and cannot be routed to
// or cancelled (no iteration loop), so listing them would mislead the LLM
// into producing invalid route/cancel actions.
//
// The roster intentionally carries only runtime-known identity, duration, and
// guidance. Shared notes are supplied separately as source material and are
// never attributed to an individual Focus.
func buildActiveWorksContext(works []*work) string {
	var parts []string
	for _, w := range works {
		duration := time.Since(w.startedAt).Round(time.Second)
		entry := fmt.Sprintf("- [Work #%d, running %s] %s",
			w.ID, duration, w.plan.Guidance)
		parts = append(parts, entry)
	}
	if len(parts) == 0 {
		return ""
	}
	return fmt.Sprintf("Active works:\n%s\n\n", strings.Join(parts, "\n"))
}

// buildCompletedWorksContext formats recent runtime-owned handoffs from a
// Session without assuming that the Session owns a Workspace or its notes.
func buildCompletedWorksContext(personID, sessionID int64, relevanceHints ...string) string {
	var records []model.FocusHandoff
	if err := database.DB.Where("person_id = ? AND session_id = ?", personID, sessionID).
		Order("id DESC").Limit(20).Find(&records).Error; err != nil {
		applogger.Error("buildCompletedWorksContext: failed to load focus handoffs",
			"person_id", personID, "session_id", sessionID, "error", err)
		return ""
	}
	candidateCount := len(records)
	usedRelevance := len(relevanceHints) > 0 && strings.TrimSpace(relevanceHints[0]) != ""
	if usedRelevance {
		hintTerms := focusContextTerms(relevanceHints[0])
		sort.SliceStable(records, func(i, j int) bool {
			return focusHandoffRelevance(records[i], hintTerms) > focusHandoffRelevance(records[j], hintTerms)
		})
	}
	if len(records) > 5 {
		records = records[:5]
	}
	applogger.Info("FocusHandoff selection", "person_id", personID, "session_id", sessionID, "candidates", candidateCount, "selected", len(records), "lexical_relevance", usedRelevance)

	var parts []string
	for _, handoff := range records {
		parts = append(parts, formatFocusHandoff(handoff, fmt.Sprintf("Work #%d", handoff.WorkID)))
	}
	result := ""
	if len(parts) > 0 {
		result += fmt.Sprintf("Recent Focus handoffs in this session:\n%s\n\n", strings.Join(parts, "\n"))
	}
	return result
}

// buildSessionFocusContext combines the session's running Focus roster with
// compact handoffs. Workspace notes are read only after a Work selects one.
func buildSessionFocusContext(personID, sessionID int64, activeWorks []*work, relevanceHint string) string {
	active := buildActiveWorksContext(filterWorksBySession(activeWorks, sessionID))
	completed := buildCompletedWorksContext(personID, sessionID, relevanceHint)
	context := active + completed
	const maxChars = 6000
	if len(context) > maxChars {
		applogger.Info("SessionFocusContext truncated", "person_id", personID, "session_id", sessionID, "chars", len(context), "max_chars", maxChars)
		context = context[:maxChars] + "\n[Focus context truncated; inspect or read referenced sources when needed.]"
	}
	applogger.Info("SessionFocusContext assembled", "person_id", personID, "session_id", sessionID, "active_focuses", len(filterWorksBySession(activeWorks, sessionID)), "chars", len(context))
	return context
}

// focusContextTerms produces a bounded lexical relevance signal. It is an
// explicit fallback while causal association remains intentionally out of scope.
func focusContextTerms(hint string) map[string]struct{} {
	terms := make(map[string]struct{})
	for _, field := range strings.FieldsFunc(strings.ToLower(hint), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len([]rune(field)) >= 2 {
			terms[field] = struct{}{}
		}
	}
	for _, r := range strings.ToLower(hint) {
		if unicode.Is(unicode.Han, r) {
			terms[string(r)] = struct{}{}
		}
	}
	return terms
}

// focusHandoffRelevance scores a handoff against bounded lexical hint terms.
func focusHandoffRelevance(handoff model.FocusHandoff, terms map[string]struct{}) int {
	if len(terms) == 0 {
		return 0
	}
	text := strings.ToLower(strings.Join([]string{
		handoff.Orientation, handoff.Summary, handoff.Unresolved, handoff.NextStep,
	}, " "))
	score := 0
	for term := range terms {
		if strings.Contains(text, term) {
			if len([]rune(term)) == 1 {
				score++
			} else {
				score += 4
			}
		}
	}
	return score
}

// buildAgentFocusContext gives heartbeat and private-origin cognition a small
// cross-session roster. It intentionally excludes raw notes and file contents.
func buildAgentFocusContext(personID int64, activeWorks []*work) string {
	var records []model.FocusHandoff
	if err := database.DB.Where("person_id = ?", personID).
		Order("id DESC").Limit(5).Find(&records).Error; err != nil {
		applogger.Error("buildAgentFocusContext: failed to load focus handoffs", "person_id", personID, "error", err)
		return ""
	}
	parts := make([]string, 0, 2)
	if len(activeWorks) > 0 {
		parts = append(parts, "Running works:\n"+strings.TrimSpace(buildActiveWorksContext(activeWorks)))
	}
	handoffParts := make([]string, 0, len(records))
	for _, handoff := range records {
		scope := "private"
		if handoff.SessionID > 0 {
			scope = fmt.Sprintf("session %d / Work #%d", handoff.SessionID, handoff.WorkID)
		}
		handoffParts = append(handoffParts, formatFocusHandoff(handoff, scope))
	}
	if len(records) > 0 {
		parts = append(parts, "Recent Focus handoffs across my owned space:\n"+strings.Join(handoffParts, "\n"))
	}
	if len(parts) == 0 {
		return ""
	}
	context := strings.Join(parts, "\n") + "\n"
	const maxChars = 5000
	if len(context) > maxChars {
		applogger.Info("AgentFocusContext truncated", "person_id", personID, "chars", len(context), "max_chars", maxChars)
		return context[:maxChars] + "\n[Focus context truncated.]"
	}
	applogger.Info("AgentFocusContext assembled", "person_id", personID, "active_focuses", len(activeWorks), "handoffs", len(records), "chars", len(context))
	return context
}

// formatFocusHandoff renders one compact runtime-owned handoff for an LLM prompt.
func formatFocusHandoff(handoff model.FocusHandoff, scope string) string {
	entry := fmt.Sprintf("- [%s, %s] %s\n  Result: %s",
		scope, focusHandoffStatusLabel(handoff.Status), truncateWorkDescription(handoff.Orientation), truncateWorkDescription(handoff.Summary))
	if handoff.ConfirmedFindings != "" {
		entry += "\n  Confirmed: " + truncateWorkDescription(handoff.ConfirmedFindings)
	}
	if handoff.ArtifactReferences != "" {
		entry += "\n  Artifacts: " + truncateWorkDescription(handoff.ArtifactReferences)
	}
	if handoff.Unresolved != "" {
		entry += "\n  Unresolved: " + truncateWorkDescription(handoff.Unresolved)
	}
	return entry
}

// focusHandoffStatusLabel renders a persisted handoff status for prompt context.
func focusHandoffStatusLabel(status model.FocusHandoffStatus) string {
	switch status {
	case model.FocusHandoffFailed:
		return "failed"
	case model.FocusHandoffCancelled:
		return "cancelled"
	case model.FocusHandoffInterrupted:
		return "interrupted"
	case model.FocusHandoffPaused:
		return "paused"
	default:
		if status != model.FocusHandoffCompleted {
			applogger.Error("focus handoff: unknown status", "status", status)
			return "unknown"
		}
		return "completed"
	}
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

// buildComprehensionContext formats the comprehension result for the Decide
// prompt based on its type. It dispatches by Comprehension.Type so the Decide
// phase stays decoupled from any single event-type-specific comprehension;
// each branch renders only the context it actually has.
func buildComprehensionContext(comprehension *comprehendTypes.Comprehension, selfPersonID int64) string {
	if comprehension == nil {
		return ""
	}
	var eventAnalysis string
	switch comprehension.Type {
	case comprehendTypes.ComprehensionTypeChat:
		eventAnalysis = buildChatComprehensionContext(comprehension.Chat, selfPersonID)
	case comprehendTypes.ComprehensionTypeBiography:
		// Biography comprehension carries no extra analysis: the origin
		// statement is already the event description itself.
	case comprehendTypes.ComprehensionTypeWorkCompleted:
		// Work-completed comprehension carries no extra analysis: the event
		// description (guidance plus status) is the understanding itself.
	}
	return eventAnalysis
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
		"\nThis work originated in a conversation (session_id=%d). If you reply there, set chat_plan.session_id to %d.\n\n",
		sessionID, sessionID,
	)
}

// buildChatComprehensionContext formats comprehension results for the Decide prompt.
// This provides the LLM with the agent's understanding of the message,
// enabling informed decision-making instead of guessing from raw text.
func buildChatComprehensionContext(chatComprehension *comprehendTypes.ChatComprehension, selfPersonID int64) string {
	if chatComprehension == nil {
		return ""
	}

	var sections []string
	if chatComprehension.Narrative != "" {
		sections = append(sections, "Background conversation narrative (a summary of earlier dialogue):\n"+chatComprehension.Narrative)
	}
	// The triggering batch is already shown as Event. Preserve the preceding
	// original messages as evidence for references in the current utterance.
	batchIDs := make(map[int64]struct{}, len(chatComprehension.ReadMessageIDs))
	for _, id := range chatComprehension.ReadMessageIDs {
		batchIDs[id] = struct{}{}
	}
	var recentLines []string
	for _, message := range chatComprehension.RecentMessages {
		if _, current := batchIDs[message.ID]; current {
			continue
		}
		speaker := message.PersonName
		if message.PersonID == selfPersonID {
			speaker = "You"
		}
		recentLines = append(recentLines, fmt.Sprintf("- At %s — %s", message.CreatedAt.Format("2006-01-02 15:04:05"), memory.FormatChatSpeech(speaker, message.Content)))
		if message.OwnAction != nil {
			// The source action explains this past utterance; its plan does not
			// become a new instruction for the current decision.
			var pastContext []string
			if message.OwnAction.Background != "" {
				pastContext = append(pastContext, fmt.Sprintf("your understanding: %q", message.OwnAction.Background))
			}
			if message.OwnAction.Reason != "" {
				pastContext = append(pastContext, fmt.Sprintf("your reason: %q", message.OwnAction.Reason))
			}
			if message.OwnAction.Guidance != "" {
				pastContext = append(pastContext, fmt.Sprintf("your intended speech: %q", message.OwnAction.Guidance))
			}
			if len(pastContext) > 0 {
				recentLines = append(recentLines, "  Your stated context at that time: "+strings.Join(pastContext, "; "))
			}
		}
	}
	if len(recentLines) > 0 {
		sections = append(sections, "Recent messages in this session (original messages, before the current event):\n"+strings.Join(recentLines, "\n"))
	}

	var parts []string

	if chatComprehension.PersonState != nil {
		if purpose := chatComprehension.PersonState.Purpose.Description(); purpose != "" {
			parts = append(parts, "Possible conversational purpose: "+purpose)
		}
		if chatComprehension.PersonState.Situation != "" {
			parts = append(parts, fmt.Sprintf("Possible situation inferred during comprehension: %s", chatComprehension.PersonState.Situation))
		}
	}

	if chatComprehension.KBRetrieval != nil && chatComprehension.KBRetrieval.Query != "" {
		parts = append(parts, fmt.Sprintf(
			"Suggested knowledge-base search: %q (knowledge_base_ids=%v).",
			truncateWorkDescription(chatComprehension.KBRetrieval.Query),
			chatComprehension.KBRetrieval.KnowledgeBaseIDs,
		))
	}

	if len(parts) > 0 {
		sections = append(sections, "Comprehension analysis:\n"+strings.Join(parts, "\n"))
	}
	if len(sections) == 0 {
		return ""
	}
	return strings.Join(sections, "\n\n") + "\n\n"
}

// buildContactablePersonsContext constructs a bounded general-world routing
// roster. It carries no message history or inferred profile content.
func buildContactablePersonsContext(selfPersonID int64) string {
	var persons []model.Person
	if err := database.DB.Where("id != ? AND status = ?", selfPersonID, model.PersonStatusActive).Order("id").Limit(20).Find(&persons).Error; err != nil {
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
			fmt.Fprintf(&sb, "- %s (person_id=%d). Bio: %s\n", p.Name, p.ID, truncateWorkDescription(p.Bio))
		} else {
			fmt.Fprintf(&sb, "- %s (person_id=%d). No bio yet.\n", p.Name, p.ID)
		}
	}
	var total int64
	if err := database.DB.Model(&model.Person{}).Where("id != ? AND status = ?", selfPersonID, model.PersonStatusActive).Count(&total).Error; err != nil {
		applogger.Error("buildContactablePersonsContext: failed to count persons", "self_person_id", selfPersonID, "error", err)
	} else if total > int64(len(persons)) {
		fmt.Fprintf(&sb, "- %d further contactable persons are not shown\n", total-int64(len(persons)))
	}
	sb.WriteString("\n")
	return sb.String()
}
