/** Run the chat-to-reminder case against an isolated product server. */
import { fileURLToPath } from "node:url";
import path from "node:path";
import {
  request, runChatCase,
} from "../../chat_runner.ts";
import type { ChatSession, DbEvidence } from "../../chat_runner.ts";
import { monitorSemanticCase, readBaseline } from "../../capture_monitor.ts";
import { Result, Scenario } from "./case_contract.ts";
import type { ScheduleScenario } from "./case_contract.ts";

const CASE_DIR = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

// Product constants mirrored from server/internal/model and runtime action code.
const EVENT_TYPE_MESSAGE = 1;
const ACTION_TYPE_CREATE_ALARM = 4;
const EFFECT_TYPE_SCHEDULED_EVENT = 3;
const SCHEDULED_STATUS_PENDING = 0;
const ACTIVITY_PAGE_LIMIT = 200;
const CHAT_PATH = "/api/chat/new";
const MESSAGES_PATH = "/api/messages";
const ACTIVITIES_PATH = "/api/sessions";
const SUBJECT_ENTRY =
  `POST ${CHAT_PATH}; GET ${MESSAGES_PATH}/:id; GET ${ACTIVITIES_PATH}/:id/activities; ` +
  "read-only SELECT on messages, events, decisions, actions, action_effects, scheduled_events";

/** Parse SQLite/GORM timestamps with a space between date and time. */
function parseTime(value: unknown) {
  const time = Date.parse(String(value).replace(" ", "T"));
  if (!Number.isFinite(time)) throw new Error("invalid timestamp: " + value);
  return time;
}

/** Check stable record, state, and time facts; leave reminder meaning to the judge. */
export function checkDbEvidence(evidence: DbEvidence, session: ChatSession, scenario: ScheduleScenario): string[] {
  const failures: string[] = [];
  const sessionId = session.chat.session_id;
  const agentId = session.agent_id;
  const senders = evidence.messages.filter((item) => item.session_id === sessionId &&
    item.person_id === session.user_id && item.content === scenario.request);
  if (senders.length !== 1) return ["record: chat session did not preserve the exact requested message"];
  const sender = senders[0];
  if (session.chat.message_id !== sender.id)
    return ["record: chat response message ID does not match the persisted request"];
  if (!evidence.events.some((item) => item.event_type === EVENT_TYPE_MESSAGE && item.ref_id === sender.id))
    return ["record: no event recorded for the original request message"];
  const pending = evidence.scheduled_events.filter((item) => item.person_id === agentId &&
    item.session_id === sessionId && item.status === SCHEDULED_STATUS_PENDING);
  if (!pending.length)
    return ["state: no pending scheduled event belongs to the intended Agent and session"];
  const pendingIds = new Set(pending.map((item) => item.id));
  const effects = evidence.action_effects.filter((item) =>
    item.effect_type === EFFECT_TYPE_SCHEDULED_EVENT && pendingIds.has(item.effect_id));
  if (!effects.length) return ["record: no action_effect links the pending event to an action"];
  const actionIds = new Set(effects.map((item) => item.action_id));
  const alarms = evidence.actions.filter((item) => actionIds.has(item.id) && item.type === ACTION_TYPE_CREATE_ALARM);
  if (!alarms.length) return ["record: no create_alarm action is linked to the pending event"];
  const decisionIds = new Set(alarms.map((item) => item.decision_id));
  if (!evidence.decisions.some((item) => decisionIds.has(item.id) && item.person_id === agentId))
    return ["record: no Agent decision backs the create_alarm action"];
  const planOk = alarms.some((action) => {
    try {
      const plan = JSON.parse(action.plan_json || "{}");
      return plan && typeof plan === "object" && !Array.isArray(plan) && plan.trigger_at && plan.message;
    } catch { return false; }
  });
  if (!planOk) failures.push("record: create_alarm plan lacks trigger_at or message");
  for (const event of pending)
    if (!String(event.message ?? "").trim()) failures.push("state: scheduled event has no reminder message note");
  const deltas: Array<[number, number]> = [];
  for (const event of pending) {
    try { deltas.push([event.id, (parseTime(event.trigger_at) - parseTime(sender.created_at)) / 1000]); }
    catch { failures.push("record: scheduled event has an invalid or missing trigger time"); }
  }
  if (deltas.length && !deltas.some(([, delta]) =>
    Math.abs(delta - scenario.expected_delay_seconds) <= scenario.delay_tolerance_seconds)) {
    const observed = deltas.map(([id, delta]) => `event ${id}: ${delta.toFixed(0)}s`).join(", ");
    failures.push(`time: trigger_at is outside the declared tolerance of the requested future time ` +
      `(expected ${scenario.expected_delay_seconds}s +/- ${scenario.delay_tolerance_seconds}s; observed ${observed})`);
  }
  return failures;
}

/** Run this case's owned executor. */
export async function run() {
  return runChatCase({
    caseDir: CASE_DIR, scenarioModel: Scenario, resultModel: Result,
    subjectEntry: SUBJECT_ENTRY,
    evidenceTables: ["messages", "events", "decisions", "actions", "action_effects", "scheduled_events"],
    failurePrefixes: "`record` (required persistence absent), `state` (product state contradicts the intended Agent or session), `time` (trigger outside the declared future-time tolerance)",
    evidenceExplanation: "Inspect `data/db_evidence.json` for the original message and its event, then for the pending scheduled event linked through action_effects to a create_alarm action and its Agent decision.",
    async perform(base, userId, llmId, scenario, record, child) {
      const agent = await request(base, "POST", "/api/agents", {
        name: "Reminder keeper",
        description: "Keeps the user's reminders at the requested time and content",
        character_settings: "Reliable and literal about requested reminder times and content.",
        llm_config_id: llmId,
      });
      const dbPath = path.join(record.dataDir, "product/db/database.db");
      const baseline = await readBaseline(dbPath);
      const route = CHAT_PATH + "?" + new URLSearchParams({ agent_id: String(agent.id), message: scenario.request });
      const chat = await request(base, "POST", route);
      const session: ChatSession = { user_id: userId, agent_id: agent.id, chat, messages: [], activities: [], completed: false };
      record.writeDataJson("session.json", session);
      const stopped = await monitorSemanticCase({
        record, dbPath, logPath: path.join(record.dataDir, "server.log"), child,
        agentId: agent.id, triggerMessageId: chat.message_id, requestText: scenario.request,
        baseline, timeoutSeconds: scenario.limits.reminder_timeout_seconds,
      });
      session.messages = await request(base, "GET", `${MESSAGES_PATH}/${chat.session_id}`);
      session.activities = await request(base, "GET", `${ACTIVITIES_PATH}/${chat.session_id}/activities?limit=${ACTIVITY_PAGE_LIMIT}`);
      session.agent_messages = stopped.messages;
      session.stop_reason = stopped.reason;
      session.completed = true;
      record.writeDataJson("session.json", session);
    },
    checkEvidence: checkDbEvidence,
  });
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url))
  process.exitCode = await run();
