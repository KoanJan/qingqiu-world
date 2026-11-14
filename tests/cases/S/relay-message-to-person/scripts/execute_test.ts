/** Send the relay request and capture the resulting conversations. */
import path from "node:path";
import type { RunRecord, ServerStatus } from "../../../tools.ts";
import { personIdByName, readRows, request } from "../../product_api.ts";
import type { DbEvidence, DbRow } from "../../product_api.ts";
import type { ChatSession } from "../../chat_workflow.ts";
import { ActionEffectType, ActionType, EventType, PersonType } from "../../../product_model.ts";
import { ChatRoute } from "../../product_routes.ts";
import { monitorSemanticCase, readBaseline } from "../../capture_monitor.ts";
import { semanticUserName } from "../../semantic_environment.ts";
import type { RelayScenario } from "./case_contract.ts";
import { RELAY_AGENT_NAME } from "./prepare_data.ts";

/** A direct conversation has exactly the subject Agent and one other person. */
const ONE_TO_ONE_PARTICIPANTS = 2;

/** Chat plans name an existing session with a positive ID and a new one with a negative ID. */
function isExistingSession(id: unknown): id is number {
  return Number.isInteger(id) && (id as number) > 0;
}
function isNewSession(id: unknown): id is number {
  return Number.isInteger(id) && (id as number) < 0;
}

/**
 * Find 1v1 sessions that contain both the subject Agent and the target person.
 *
 * The same rule is used while capturing the session views and while checking
 * persisted evidence, so a session list cannot disagree with the check.
 */
function targetSessionIds(
  links: ReadonlyArray<Record<string, any>>,
  agentId: number,
  targetId: number,
): number[] {
  const memberships = new Map<number, Set<number>>();
  for (const link of links) {
    const people = memberships.get(link.session_id) ?? new Set<number>();
    people.add(link.participant_id);
    memberships.set(link.session_id, people);
  }
  return [...memberships]
    .filter(([, people]) => people.size === ONE_TO_ONE_PARTICIPANTS &&
      people.has(agentId) && people.has(targetId))
    .map(([id]) => id)
    .sort((left, right) => left - right);
}

/** Check record, state, and target facts; leave delivered message meaning to the judge. */
export function checkDbEvidence(evidence: DbEvidence, session: ChatSession, scenario: RelayScenario): string[] {
  const failures: string[] = [];
  const sourceSessionId = session.chat.session_id;
  const agentId = session.agent_id;
  const targetId = session.target_person_id;
  if (targetId === undefined) return ["record: chat session did not preserve the target person"];
  const senders = evidence.messages.filter((item) => item.session_id === sourceSessionId &&
    item.person_id === session.user_id && item.content === scenario.request);
  if (senders.length !== 1) return ["record: chat session did not preserve the exact requested message"];
  const sender = senders[0];
  if (session.chat.message_id !== sender.id)
    return ["record: chat response message ID does not match the persisted request"];
  if (!evidence.events.some((item) => item.event_type === EventType.MESSAGE && item.ref_id === sender.id))
    return ["record: no event recorded for the original request message"];
  const targetSessions = new Set(
    targetSessionIds(evidence.participant_sessions, agentId, targetId),
  );
  if (!targetSessions.size)
    return ["state: no 1v1 session contains the Agent and the declared target person"];
  const relay = evidence.messages.filter((item) => targetSessions.has(item.session_id) &&
    item.person_id === agentId && String(item.content ?? "").trim());
  if (!relay.length)
    return ["record: no message from the Agent in a session shared with the target person"];
  const relayIds = new Set(relay.map((item) => item.id));
  const effects = evidence.action_effects.filter((item) =>
    item.effect_type === ActionEffectType.MESSAGE && relayIds.has(item.effect_id));
  if (!effects.length)
    return ["record: no action_effect links the delivered message to an action"];
  const actionIds = new Set(effects.map((item) => item.action_id));
  const chats = evidence.actions.filter((item) => actionIds.has(item.id) && item.type === ActionType.CHAT);
  if (!chats.length) return ["record: no chat action is linked to the delivered message"];
  const decisionIds = new Set(chats.map((item) => item.decision_id));
  if (!evidence.decisions.some((item) => decisionIds.has(item.id) && item.person_id === agentId))
    return ["record: no Agent decision backs the chat action"];
  // The plan must aim at the target session, or declare the recipient when creating a new session.
  let planOk = false;
  let planInvalid = false;
  for (const effect of effects) {
    const action = chats.find((item) => item.id === effect.action_id);
    const message = relay.find((item) => item.id === effect.effect_id);
    if (!action || !message) continue;
    let plan: Record<string, any>;
    try { plan = JSON.parse(action.plan_json || "{}"); }
    catch { planInvalid = true; continue; }
    if (!plan || typeof plan !== "object" || Array.isArray(plan)) {
      planInvalid = true;
      continue;
    }
    if (isExistingSession(plan.session_id)) planOk ||= message.session_id === plan.session_id;
    else if (isNewSession(plan.session_id)) planOk ||= plan.recipient_person_id === targetId;
  }
  if (planInvalid) failures.push("state: chat action plan is not a valid JSON object");
  if (!planOk) failures.push("state: chat action plan does not target the target session or target person");
  return failures;
}

export async function executeTest(base: string, scenario: RelayScenario, record: RunRecord,
  child: ServerStatus): Promise<void> {
  const dbPath = path.join(record.dataDir, "product/db/database.db");
  // Resolve the prepared people by name and type before sending the request.
  const [userId, agentId, targetId] = await Promise.all([
    personIdByName(dbPath, semanticUserName(record.caseName), PersonType.HUMAN),
    personIdByName(dbPath, RELAY_AGENT_NAME, PersonType.AI),
    personIdByName(dbPath, scenario.target_person_name, PersonType.AI),
  ]);
  // Baseline IDs exclude setup activity from the captured exchange.
  const baseline = await readBaseline(dbPath);
  const route = ChatRoute.CREATE + "?" +
    new URLSearchParams({ agent_id: String(agentId), message: scenario.request });
  const chat = await request<ChatSession["chat"]>(base, "POST", route);
  const session: ChatSession = {
    user_id: userId, agent_id: agentId, target_person_id: targetId, chat,
    messages: [], activities: [], source_messages: [], target_sessions: [], completed: false,
  };
  // Write the session before monitoring so a crash still leaves partial context.
  record.writeDataJson("session.json", session);
  const stopped = await monitorSemanticCase({
    sink: record, dbPath, logPath: path.join(record.dataDir, "server.log"), child,
    agentId, triggerMessageId: chat.message_id, requestText: scenario.request,
    baseline, timeoutSeconds: scenario.limits.relay_timeout_seconds,
  });
  // Freeze the source thread and every 1v1 thread the Agent may have relayed to.
  session.source_messages = await request<DbRow[]>(base, "GET", ChatRoute.messages(chat.session_id));
  const links = await readRows(dbPath,
    "SELECT session_id, participant_id FROM participant_sessions ORDER BY id");
  const targetViews: Array<{ session_id: number; messages: DbRow[] }> = [];
  for (const id of targetSessionIds(links, agentId, targetId))
    targetViews.push({ session_id: id, messages: await request<DbRow[]>(base, "GET", ChatRoute.messages(id)) });
  session.target_sessions = targetViews;
  session.agent_messages = stopped.messages;
  session.stop_reason = stopped.reason;
  session.completed = true;
  record.writeDataJson("session.json", session);
}
