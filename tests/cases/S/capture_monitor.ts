/** Shared stopping policy for scripted semantic cases.
 *
 * A test agent decides whether the subject's words end the exchange. Persisted
 * state is consulted only while the words leave that question open.
 */
import type { ChildProcess } from "node:child_process";
import { existsSync, readFileSync, rmSync, statSync } from "node:fs";
import path from "node:path";
import { readSqlite, sleep, utcNow } from "../tools.ts";
import type { RunRecord } from "../tools.ts";

const MESSAGE_POLL_MS = 2_000;
export const STATE_POLL_MS = 30_000;
const REVIEW_POLL_MS = 1_000;
const LOG_EXCERPT_BYTES = 32_768;
const MESSAGE_EVENT_TYPE = 1;
const WORK_COMPLETED_EVENT_TYPE = 4;
const WORK_RUNNING = 0;
const ACTION_IN_PROGRESS = 1;

type Row = Record<string, any>;
export type Baseline = { event_id: number; decision_id: number; work_id: number; message_id: number };
export type AgentMessage = { id: number; session_id: number; person_id: number; content: string };
export type StateSnapshot = {
  at_utc: string;
  events: Row[];
  decisions: Row[];
  actions: Row[];
  works: Row[];
  pending_event_ids: number[];
  observed_without_decision_ids: number[];
  buffered_event_ids: number[];
  running_action_ids: number[];
  running_work_ids: number[];
  terminal_work_without_event_ids: number[];
  settled: boolean;
};
export type StopReason = "reply_complete" | "state_settled" | "tester_stopped" | "timeout";
export type MonitorResult = { reason: StopReason; messages: AgentMessage[]; state: StateSnapshot | null };
type ReviewPhase = "reply" | "stalled";
type ReviewResponse = { request_id: number; decision: "stop" | "continue" | "uncertain"; reason: string };

/** Take ID baselines just before the subject request, excluding setup activity. */
export async function readBaseline(dbPath: string): Promise<Baseline> {
  const [rows] = await readSqlite(dbPath, [{ sql:
    "SELECT (SELECT COALESCE(MAX(id), 0) FROM events) AS event_id, " +
    "(SELECT COALESCE(MAX(id), 0) FROM decisions) AS decision_id, " +
    "(SELECT COALESCE(MAX(id), 0) FROM works) AS work_id, " +
    "(SELECT COALESCE(MAX(id), 0) FROM messages) AS message_id" }]);
  return rows![0] as Baseline;
}

/** Read all subject messages after the request, including messages in another session. */
export async function readAgentMessages(dbPath: string, agentId: number, baseline: Baseline): Promise<AgentMessage[]> {
  const [rows] = await readSqlite(dbPath, [{
    sql: "SELECT id, session_id, person_id, content FROM messages " +
      "WHERE id > ? AND person_id = ? AND length(trim(content)) > 0 ORDER BY id",
    params: [baseline.message_id, agentId],
  }]);
  return rows! as AgentMessage[];
}

/** Classify a bounded, read-only database snapshot without interpreting speech. */
export function classifyState(
  events: Row[], decisions: Row[], actions: Row[], works: Row[], triggerMessageId: number,
): Omit<StateSnapshot, "at_utc"> {
  const relevant = events.filter((event) => {
    if (event.event_type === MESSAGE_EVENT_TYPE)
      return event.ref_id === triggerMessageId ||
        (event.message_person_id !== null && event.message_person_id !== event.agent_id && event.agent_is_participant === 1);
    if (event.event_type === WORK_COMPLETED_EVENT_TYPE)
      return event.work_person_id === event.agent_id;
    return true; // The isolated run may deliver other durable events to the subject.
  });
  const pending = relevant.filter((event) => event.observation_id === null && event.decision_id === null);
  const observedWithoutDecision = relevant.filter((event) =>
    event.observation_id !== null && event.decision_id === null &&
    !(event.event_type === MESSAGE_EVENT_TYPE && event.last_read_message_id >= event.ref_id));
  const buffered = relevant.filter((event) => event.buffer_id !== null);
  const runningActions = actions.filter((action) => action.status === ACTION_IN_PROGRESS);
  const runningWorks = works.filter((work) => work.status === WORK_RUNNING);
  const terminalWithoutEvent = works.filter((work) => work.status !== WORK_RUNNING &&
    !relevant.some((event) => event.event_type === WORK_COMPLETED_EVENT_TYPE && event.ref_id === work.id));
  const trigger = relevant.find((event) => event.event_type === MESSAGE_EVENT_TYPE && event.ref_id === triggerMessageId);
  return {
    events: relevant, decisions, actions, works,
    pending_event_ids: pending.map((item) => item.id),
    observed_without_decision_ids: observedWithoutDecision.map((item) => item.id),
    buffered_event_ids: buffered.map((item) => item.id),
    running_action_ids: runningActions.map((item) => item.id),
    running_work_ids: runningWorks.map((item) => item.id),
    terminal_work_without_event_ids: terminalWithoutEvent.map((item) => item.id),
    settled: !!trigger && !!trigger.observation_id && !!trigger.decision_id &&
      !pending.length && !observedWithoutDecision.length &&
      !buffered.length && !runningActions.length && !runningWorks.length && !terminalWithoutEvent.length,
  };
}

/** Inspect durable events, observations, decisions, actions, and works for this run. */
export async function readState(
  dbPath: string, agentId: number, triggerMessageId: number, baseline: Baseline,
): Promise<StateSnapshot> {
  const [events, decisions, actions, works] = await readSqlite(dbPath, [
    { sql: `SELECT e.id, e.event_type, e.ref_id, ? AS agent_id,
        m.person_id AS message_person_id, m.session_id AS message_session_id,
        w.person_id AS work_person_id,
        CASE WHEN ps.id IS NULL THEN 0 ELSE 1 END AS agent_is_participant,
        ps.last_read_message_id,
        o.id AS observation_id, d.id AS decision_id, b.id AS buffer_id
      FROM events e
      LEFT JOIN messages m ON e.event_type = ? AND m.id = e.ref_id
      LEFT JOIN works w ON e.event_type = ? AND w.id = e.ref_id
      LEFT JOIN participant_sessions ps ON ps.session_id = m.session_id AND ps.participant_id = ?
      LEFT JOIN agent_observations o ON o.event_id = e.id AND o.person_id = ?
      LEFT JOIN decisions d ON d.event_id = e.id AND d.person_id = ?
      LEFT JOIN agent_event_buffers b ON b.event_id = e.id AND b.person_id = ?
      WHERE e.id > ? ORDER BY e.id`,
      params: [agentId, MESSAGE_EVENT_TYPE, WORK_COMPLETED_EVENT_TYPE, agentId, agentId, agentId, agentId, baseline.event_id] },
    { sql: "SELECT id, event_id FROM decisions WHERE person_id = ? AND id > ? ORDER BY id",
      params: [agentId, baseline.decision_id] },
    { sql: `SELECT a.id, a.decision_id, a.status FROM actions a
      JOIN decisions d ON d.id = a.decision_id WHERE d.person_id = ? AND d.id > ? ORDER BY a.id`,
      params: [agentId, baseline.decision_id] },
    { sql: "SELECT id, person_id, session_id, status FROM works WHERE person_id = ? AND id > ? ORDER BY id",
      params: [agentId, baseline.work_id] },
  ]);
  return { at_utc: utcNow(), ...classifyState(events!, decisions!, actions!, works!, triggerMessageId) };
}

/** Ignore timestamps when checking whether two mechanical polls are identical. */
export function stateSignature(state: StateSnapshot): string {
  const { at_utc: _at, ...mechanical } = state;
  return JSON.stringify(mechanical);
}

function logExcerpt(logPath: string, fromByte: number) {
  if (!existsSync(logPath)) return { from_byte: 0, to_byte: 0, text: "", truncated: false };
  const bytes = readFileSync(logPath);
  const start = Math.min(fromByte, bytes.length);
  const tail = bytes.subarray(start);
  return {
    from_byte: start, to_byte: bytes.length,
    text: tail.subarray(Math.max(0, tail.length - LOG_EXCERPT_BYTES)).toString("utf8"),
    truncated: tail.length > LOG_EXCERPT_BYTES,
  };
}

/** Ask the running AI test agent through a run-local, auditable file handshake. */
async function requestReview(
  record: RunRecord, phase: ReviewPhase, requestText: string, messages: AgentMessage[],
  state: StateSnapshot | null, log: ReturnType<typeof logExcerpt>, deadline: number,
  history: Row[], child: ChildProcess,
): Promise<ReviewResponse | null> {
  const requestId = history.length + 1;
  const requestPath = path.join(record.dataDir, "stop_review_request.json");
  const responsePath = path.join(record.dataDir, "stop_review_response.json");
  rmSync(responsePath, { force: true });
  const question = phase === "reply"
    ? "Has the subject expressed that this request is finished? Judge completion only, not correctness."
    : "Given unchanged in-progress state, the full reply sequence, and new logs, should capture stop now?";
  record.writeDataJson("stop_review_request.json", {
    request_id: requestId, phase, question, request: requestText, messages, state,
    new_log: phase === "stalled" ? log : null,
    remaining_seconds: Math.max(0, Math.ceil((deadline - Date.now()) / 1000)),
  });
  console.log(`AI stop review ${requestId} (${phase}): ${requestPath}`);
  console.log(`Write ${responsePath} with {"request_id":${requestId},"decision":"stop|continue|uncertain","reason":"..."}`);
  while (Date.now() < deadline) {
    if (child.exitCode !== null || child.signalCode !== null)
      throw new Error("server exited while waiting for AI stop review: " + (child.exitCode ?? child.signalCode));
    if (existsSync(responsePath)) {
      let response: ReviewResponse;
      try { response = JSON.parse(readFileSync(responsePath, "utf8")) as ReviewResponse; }
      catch { await sleep(REVIEW_POLL_MS); continue; } // The test agent may still be writing.
      if (response.request_id !== requestId || !["stop", "continue", "uncertain"].includes(response.decision) ||
        !String(response.reason ?? "").trim()) {
        throw new Error("invalid AI stop review response; expected current request_id, decision, and reason");
      }
      history.push({ phase, messages, state, new_log: phase === "stalled" ? log : null, ...response });
      record.writeDataJson("stop_review_history.json", history);
      return response;
    }
    await sleep(REVIEW_POLL_MS);
  }
  return null;
}

export type MonitorOptions = {
  record: RunRecord; dbPath: string; logPath: string; child: ChildProcess;
  agentId: number; triggerMessageId: number; requestText: string; baseline: Baseline;
  timeoutSeconds: number;
  /** Override only in offline tests. */
  messagePollMs?: number; statePollMs?: number;
};

/** Observe replies first, then mechanically poll unresolved work every 30 seconds. */
export async function monitorSemanticCase(options: MonitorOptions): Promise<MonitorResult> {
  const {
    record, dbPath, logPath, child, agentId, triggerMessageId, requestText, baseline,
    timeoutSeconds,
  } = options;
  const deadline = Date.now() + timeoutSeconds * 1000;
  const messagePollMs = options.messagePollMs ?? MESSAGE_POLL_MS;
  const statePollMs = options.statePollMs ?? STATE_POLL_MS;
  let nextStatePoll = Date.now() + statePollMs;
  let lastMessageId = baseline.message_id;
  let lastState: StateSnapshot | null = null;
  let lastSignature = "";
  let lastReviewedPlateau = "";
  let logCursor = existsSync(logPath) ? statSync(logPath).size : 0;
  const snapshots: StateSnapshot[] = [];
  const reviews: Row[] = [];
  let messages: AgentMessage[] = [];
  let reason: StopReason = "timeout";
  while (Date.now() < deadline) {
    if (child.exitCode !== null || child.signalCode !== null)
      throw new Error("server exited during semantic capture: " + (child.exitCode ?? child.signalCode));
    messages = await readAgentMessages(dbPath, agentId, baseline);
    const newest = messages.at(-1)?.id ?? baseline.message_id;
    if (newest > lastMessageId) {
      lastMessageId = newest;
      const response = await requestReview(record, "reply", requestText, messages, null,
        logExcerpt(logPath, logCursor), deadline, reviews, child);
      if (!response) break;
      const refreshed = await readAgentMessages(dbPath, agentId, baseline);
      if (refreshed.at(-1)?.id !== newest) continue; // A newer reply supersedes the review.
      if (response.decision === "stop") { reason = "reply_complete"; break; }
    }
    if (Date.now() >= nextStatePoll) {
      nextStatePoll = Date.now() + statePollMs;
      const state = await readState(dbPath, agentId, triggerMessageId, baseline);
      snapshots.push(state);
      record.writeDataJson("state_polls.json", snapshots);
      const signature = stateSignature(state);
      if (state.settled) {
        const refreshed = await readAgentMessages(dbPath, agentId, baseline);
        if ((refreshed.at(-1)?.id ?? baseline.message_id) !== lastMessageId) continue;
        lastState = state;
        reason = "state_settled";
        break;
      }
      const log = logExcerpt(logPath, logCursor);
      const plateau = signature + ":" + log.to_byte + ":" + lastMessageId;
      if (signature === lastSignature && plateau !== lastReviewedPlateau) {
        const response = await requestReview(record, "stalled", requestText, messages, state,
          log, deadline, reviews, child);
        lastReviewedPlateau = plateau;
        if (!response) break;
        logCursor = log.to_byte;
        const refreshedMessages = await readAgentMessages(dbPath, agentId, baseline);
        if ((refreshedMessages.at(-1)?.id ?? baseline.message_id) !== lastMessageId) continue;
        const refreshedState = await readState(dbPath, agentId, triggerMessageId, baseline);
        if (stateSignature(refreshedState) !== signature) continue;
        if (logExcerpt(logPath, log.to_byte).to_byte !== log.to_byte) continue;
        if (response.decision === "stop") { lastState = refreshedState; reason = "tester_stopped"; break; }
      }
      lastState = state;
      lastSignature = signature;
    }
    await sleep(Math.min(messagePollMs, Math.max(0, deadline - Date.now())));
  }
  messages = await readAgentMessages(dbPath, agentId, baseline);
  const result = { reason, messages, state: lastState };
  record.writeDataJson("stop_monitor.json", { ...result, state_polls: snapshots, reviews });
  return result;
}
