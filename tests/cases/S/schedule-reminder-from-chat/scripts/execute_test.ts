/** Send the request and capture the reminder scenario's response and activities. */
import path from "node:path";
import type { RunRecord, ServerStatus } from "../../../tools.ts";
import { personIdByName, request } from "../../product_api.ts";
import type { DbEvidence, DbRow } from "../../product_api.ts";
import type { ChatSession } from "../../chat_workflow.ts";
import { monitorSemanticCase, readBaseline } from "../../capture_monitor.ts";
import { semanticUserName } from "../../semantic_environment.ts";
import {
  ActionEffectType, ActionType, EventType, PersonType, ScheduledEventStatus,
} from "../../../product_model.ts";
import { ChatRoute } from "../../product_routes.ts";
import type { ScheduleScenario } from "./case_contract.ts";
import { REMINDER_AGENT_NAME } from "./prepare_data.ts";

/** Parse SQLite/GORM timestamps with a space between date and time. */
function parseTime(value: unknown): number {
  const time = Date.parse(String(value).replace(" ", "T"));
  if (!Number.isFinite(time)) throw new Error("invalid timestamp: " + value);
  return time;
}

/** Check stable record, state, and time facts; leave reminder meaning to the judge. */
export function checkDbEvidence(evidence: DbEvidence, session: ChatSession, scenario: ScheduleScenario): string[] {
  const failures: string[] = [];
  const sessionId = session.chat.session_id;
  const agentId = session.agent_id;
  // Record: the exact request must be persisted once and linked to its event.
  const senders = evidence.messages.filter((item) => item.session_id === sessionId &&
    item.person_id === session.user_id && item.content === scenario.request);
  if (senders.length !== 1) return ["record: chat session did not preserve the exact requested message"];
  const sender = senders[0];
  if (session.chat.message_id !== sender.id)
    return ["record: chat response message ID does not match the persisted request"];
  if (!evidence.events.some((item) => item.event_type === EventType.MESSAGE && item.ref_id === sender.id))
    return ["record: no event recorded for the original request message"];
  // State: a pending alarm must belong to the intended Agent and session.
  const pending = evidence.scheduled_events.filter((item) => item.person_id === agentId &&
    item.session_id === sessionId && item.status === ScheduledEventStatus.PENDING);
  if (!pending.length)
    return ["state: no pending scheduled event belongs to the intended Agent and session"];
  // Record: the pending event must be traceable through an action to a
  // create_alarm decision made by the intended Agent.
  const pendingIds = new Set(pending.map((item) => item.id));
  const effects = evidence.action_effects.filter((item) =>
    item.effect_type === ActionEffectType.SCHEDULED_EVENT && pendingIds.has(item.effect_id));
  if (!effects.length) return ["record: no action_effect links the pending event to an action"];
  const actionIds = new Set(effects.map((item) => item.action_id));
  const alarms = evidence.actions.filter((item) => actionIds.has(item.id) && item.type === ActionType.CREATE_ALARM);
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
  // State: the scheduled event must carry a nonblank reminder note.
  for (const event of pending)
    if (!String(event.message ?? "").trim()) failures.push("state: scheduled event has no reminder message note");
  // Time: at least one alarm must fall within the declared delay tolerance of
  // the original request; this checks scheduling, not reminder wording.
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

export async function executeTest(base: string, scenario: ScheduleScenario, record: RunRecord,
  child: ServerStatus): Promise<void> {
  const dbPath = path.join(record.dataDir, "product/db/database.db");
  // Resolve the prepared user and Agent by name and type before the request.
  const [userId, agentId] = await Promise.all([
    personIdByName(dbPath, semanticUserName(record.caseName), PersonType.HUMAN),
    personIdByName(dbPath, REMINDER_AGENT_NAME, PersonType.AI),
  ]);
  // Baseline IDs exclude setup activity from the captured exchange.
  const baseline = await readBaseline(dbPath);
  const route = ChatRoute.CREATE + "?" + new URLSearchParams({ agent_id: String(agentId), message: scenario.request });
  const chat = await request<ChatSession["chat"]>(base, "POST", route);
  const session: ChatSession = { user_id: userId, agent_id: agentId, chat, messages: [], activities: [], completed: false };
  // Write the session before monitoring so a crash still leaves partial context.
  record.writeDataJson("session.json", session);
  const stopped = await monitorSemanticCase({
    sink: record, dbPath, logPath: path.join(record.dataDir, "server.log"), child,
    agentId, triggerMessageId: chat.message_id, requestText: scenario.request,
    baseline, timeoutSeconds: scenario.limits.reminder_timeout_seconds,
  });
  // Freeze the reply thread and activities produced by the exchange.
  session.messages = await request<DbRow[]>(base, "GET", ChatRoute.messages(chat.session_id));
  session.activities = await request<DbRow[]>(base, "GET", ChatRoute.activities(chat.session_id));
  session.agent_messages = stopped.messages;
  session.stop_reason = stopped.reason;
  session.completed = true;
  record.writeDataJson("session.json", session);
}
