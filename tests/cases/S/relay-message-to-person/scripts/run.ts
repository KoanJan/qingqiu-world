/** Run the message relay case against an isolated product server. */
import type { ChildProcess } from "node:child_process";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { sleep } from "../../../tools.ts";
import { readRows, request, runChatCase, ScenarioError } from "../../chat_runner.ts";
import type { ChatSession, DbEvidence } from "../../chat_runner.ts";
import { Result, Scenario } from "./case_contract.ts";
import type { RelayScenario } from "./case_contract.ts";

const CASE_DIR = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

// Product constants mirrored from server/internal/model and runtime action code.
const EVENT_TYPE_MESSAGE = 1;
const ACTION_TYPE_CHAT = 0;
const EFFECT_TYPE_MESSAGE = 2;
const CHAT_PATH = "/api/chat/new";
const MESSAGES_PATH = "/api/messages";
const RELAY_POLL_MS = 1_000;
const SUBJECT_ENTRY =
  `POST ${CHAT_PATH}; POST /api/agents; GET ${MESSAGES_PATH}/:id; read-only SELECT on ` +
  "messages, events, decisions, actions, action_effects, sessions, participant_sessions, persons";

/** Find sessions whose participants include both the subject Agent and target. */
async function findTargetSessionIds(dbPath: string, agentId: number, targetId: number): Promise<number[]> {
  const rows = await readRows(dbPath, `
    SELECT DISTINCT p1.session_id AS session_id
    FROM participant_sessions p1
    JOIN participant_sessions p2 ON p2.session_id = p1.session_id
    WHERE p1.participant_id = ? AND p2.participant_id = ?
    ORDER BY p1.session_id`, [agentId, targetId]);
  return rows.map((item) => item.session_id);
}

/** Find nonblank messages the Agent sent in a session shared with the target. */
async function findRelayMessages(dbPath: string, agentId: number, targetId: number, sourceSessionId: number) {
  return readRows(dbPath, `
    SELECT DISTINCT m.* FROM messages m
    JOIN participant_sessions p1 ON p1.session_id = m.session_id AND p1.participant_id = ?
    JOIN participant_sessions p2 ON p2.session_id = m.session_id AND p2.participant_id = ?
    WHERE m.person_id = ? AND m.session_id != ? AND length(trim(m.content)) > 0
    ORDER BY m.id`, [agentId, targetId, agentId, sourceSessionId]);
}

/** Wait for a relay message or an Agent reply in the requesting session. */
async function waitForRelay(base: string, dbPath: string, sourceSessionId: number,
  agentId: number, targetId: number, child: ChildProcess, seconds: number) {
  const deadline = Date.now() + seconds * 1000;
  let sawReply = false;
  while (Date.now() < deadline) {
    if (child.exitCode !== null || child.signalCode !== null)
      throw new ScenarioError("server exited while waiting for a relay: " + (child.exitCode ?? child.signalCode));
    const relay = await findRelayMessages(dbPath, agentId, targetId, sourceSessionId);
    if (relay.length) return { relay, sawReply };
    const messages = await request<Array<Record<string, any>>>(base, "GET", `${MESSAGES_PATH}/${sourceSessionId}`);
    sawReply ||= messages.some((item) => item.person_id === agentId && String(item.content ?? "").trim().length > 0);
    await sleep(RELAY_POLL_MS);
  }
  return { relay: [], sawReply };
}

/** Check record, state, and target facts; leave delivered message meaning to the judge. */
export function checkDbEvidence(evidence: DbEvidence, session: ChatSession, scenario: RelayScenario): string[] {
  const failures: string[] = [];
  const sourceSessionId = session.chat.session_id;
  const agentId = session.agent_id;
  const targetId = session.target_person_id;
  const senders = evidence.messages.filter((item) => item.session_id === sourceSessionId &&
    item.person_id === session.user_id && item.content === scenario.request);
  if (senders.length !== 1) return ["record: chat session did not preserve the exact requested message"];
  const sender = senders[0];
  if (session.chat.message_id !== sender.id)
    return ["record: chat response message ID does not match the persisted request"];
  if (!evidence.events.some((item) => item.event_type === EVENT_TYPE_MESSAGE && item.ref_id === sender.id))
    return ["record: no event recorded for the original request message"];
  const memberships = new Map<number, Set<number>>();
  for (const link of evidence.participant_sessions) {
    const people = memberships.get(link.session_id) ?? new Set<number>();
    people.add(link.participant_id);
    memberships.set(link.session_id, people);
  }
  const targetSessions = new Set([...memberships].filter(([, people]) =>
    people.size === 2 && people.has(agentId) && people.has(targetId)).map(([id]) => id));
  if (!targetSessions.size)
    return ["state: no 1v1 session contains the Agent and the declared target person"];
  const relay = evidence.messages.filter((item) => targetSessions.has(item.session_id) &&
    item.person_id === agentId && String(item.content ?? "").trim());
  if (!relay.length)
    return ["record: no message from the Agent in a session shared with the target person"];
  const relayIds = new Set(relay.map((item) => item.id));
  const effects = evidence.action_effects.filter((item) =>
    item.effect_type === EFFECT_TYPE_MESSAGE && relayIds.has(item.effect_id));
  if (!effects.length)
    return ["record: no action_effect links the delivered message to an action"];
  const actionIds = new Set(effects.map((item) => item.action_id));
  const chats = evidence.actions.filter((item) => actionIds.has(item.id) && item.type === ACTION_TYPE_CHAT);
  if (!chats.length) return ["record: no chat action is linked to the delivered message"];
  const decisionIds = new Set(chats.map((item) => item.decision_id));
  if (!evidence.decisions.some((item) => decisionIds.has(item.id) && item.person_id === agentId))
    return ["record: no Agent decision backs the chat action"];
  // The plan must aim at the target session, or declare the recipient when creating a new session.
  let planOk = false;
  for (const effect of effects) {
    const action = chats.find((item) => item.id === effect.action_id);
    const message = relay.find((item) => item.id === effect.effect_id);
    if (!action || !message) continue;
    let plan: Record<string, any>;
    try { plan = JSON.parse(action.plan_json || "{}"); }
    catch { continue; }
    if (!plan || typeof plan !== "object" || Array.isArray(plan)) continue;
    if (Number.isInteger(plan.session_id) && plan.session_id > 0)
      planOk ||= message.session_id === plan.session_id;
    else if (Number.isInteger(plan.session_id) && plan.session_id < 0)
      planOk ||= plan.recipient_person_id === targetId;
  }
  if (!planOk) failures.push("state: chat action plan does not target the target session or target person");
  return failures;
}

/** Run this case's owned executor. */
export async function run() {
  return runChatCase({
    caseDir: CASE_DIR, scenarioModel: Scenario, resultModel: Result,
    subjectEntry: SUBJECT_ENTRY,
    evidenceTables: ["persons", "sessions", "participant_sessions", "messages", "events", "decisions", "actions", "action_effects"],
    failurePrefixes: "`record` (required persistence or link absent), `state` (product state contradicts the intended recipient, session, or delivery target)",
    evidenceExplanation: "Inspect `data/db_evidence.json` for the original request and its event, the target session's Agent message, then its action_effects link to a chat action and its Agent decision.",
    extraResult: (scenario) => ({ target_person_name: scenario.target_person_name }),
    extraReport: (scenario) => `Target person: \`${scenario.target_person_name}\`\n\n`,
    async perform(base, userId, llmId, scenario, record, child) {
      // The target is a second Person in this isolated world; the subject Agent resolves its name.
      const target = await request(base, "POST", "/api/agents", {
        name: scenario.target_person_name,
        description: "The person who should receive the relayed message.",
        character_settings: "Calm and literal; answer only when someone addresses you.",
        llm_config_id: llmId,
      });
      const agent = await request(base, "POST", "/api/agents", {
        name: "Relay assistant",
        description: "Relays the user's message to the named person without changing its key details.",
        character_settings: "Careful and reliable about names, times, places, and details.",
        llm_config_id: llmId,
      });
      const route = CHAT_PATH + "?" + new URLSearchParams({ agent_id: String(agent.id), message: scenario.request });
      const chat = await request(base, "POST", route);
      const session: ChatSession = {
        user_id: userId, agent_id: agent.id, target_person_id: target.id, chat,
        source_messages: [], target_sessions: [], completed: false,
      };
      record.writeDataJson("session.json", session);
      const dbPath = path.join(record.dataDir, "product/db/database.db");
      const { relay, sawReply } = await waitForRelay(base, dbPath, chat.session_id,
        agent.id, target.id, child, scenario.limits.relay_timeout_seconds);
      if (!relay.length && !sawReply)
        throw new ScenarioError(`agent produced neither a reply nor a relay message within ${scenario.limits.relay_timeout_seconds} seconds`);
      session.source_messages = await request(base, "GET", `${MESSAGES_PATH}/${chat.session_id}`);
      const targetViews = [];
      for (const id of await findTargetSessionIds(dbPath, agent.id, target.id))
        targetViews.push({ session_id: id, messages: await request(base, "GET", `${MESSAGES_PATH}/${id}`) });
      session.target_sessions = targetViews;
      session.completed = true;
      record.writeDataJson("session.json", session);
    },
    checkEvidence: checkDbEvidence,
  });
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url))
  process.exitCode = await run();
