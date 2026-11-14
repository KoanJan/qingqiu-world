/** Shared question and observation phase for the two RAG cases. */
import path from "node:path";
import { existsSync, readFileSync } from "node:fs";
import { errorMessage, readSqlite, sha256 } from "../tools.ts";
import type { RunRecord, ServerStatus } from "../tools.ts";
import type { ScenarioData } from "./rag_contract.ts";
import { monitorSemanticCase, readBaseline } from "./capture_monitor.ts";
import type { StopReason } from "./capture_monitor.ts";
import { request, ScenarioError } from "./product_api.ts";
import type { DbRow } from "./product_api.ts";
import { FIRST_REVISION_ID, RAG_AGENT_NAME, RAG_KB_NAME } from "./rag_prepare_data.ts";
import { ChatRoute } from "./product_routes.ts";
import { DocumentStatus, PersonType } from "../product_model.ts";
import { semanticUserName } from "./semantic_environment.ts";

/** Structured provenance captured for a RAG answer. */
type RagEvidence = {
  document: DbRow | null;
  kb_access: DbRow[];
  works: DbRow[];
  interactions: DbRow[];
  kb_usage_traces: DbRow[];
  active_chunks: DbRow[];
};

/** Captured RAG session evaluated by the mechanical provenance checks. */
export type RagSession = {
  user_id: number;
  agent_id: number;
  kb_id: number;
  document: DbRow;
  chat: DbRow;
  messages: DbRow[];
  agent_messages: DbRow[];
  activities: DbRow[];
  stop_reason?: StopReason;
};
export async function readDbEvidence(
  dbPath: string,
  sessionId: number,
  docId: number,
  revisionId: number,
): Promise<RagEvidence> {
  // One read-only snapshot: retrieval traces and work interactions for this
  // session, the active revision's chunks, the document row, and all grants.
  const [traces, chunks, documents, grants, works, interactions] = await readSqlite(dbPath, [
    { sql: "SELECT * FROM kb_usage_traces WHERE session_id = ? ORDER BY id", params: [sessionId] },
    { sql: "SELECT id, knowledge_base_id, document_id, revision_id, display_text, content " +
        "FROM document_chunks WHERE document_id = ? AND revision_id = ? AND deleted = 0 ORDER BY id",
      params: [docId, revisionId] },
    { sql: "SELECT * FROM documents WHERE id = ?", params: [docId] },
    { sql: "SELECT person_id, kb_id FROM kb_access ORDER BY id" },
    { sql: "SELECT id, person_id, session_id, status FROM works WHERE session_id = ? ORDER BY id",
      params: [sessionId] },
    { sql: "SELECT i.id, i.work_id, i.iteration, i.type, i.data FROM interactions i " +
        "JOIN works w ON w.id = i.work_id WHERE w.session_id = ? ORDER BY i.id",
      params: [sessionId] },
  ]);
  return {
    document: documents![0] ?? null,
    kb_access: grants!,
    works: works!,
    interactions: interactions!,
    kb_usage_traces: traces!,
    active_chunks: chunks!,
  };
}

export type RagSessionFacts = Pick<RagSession, "agent_id" | "kb_id" | "document">;

/**
 * Check stable provenance facts; leave answer meaning to the independent judge.
 *
 * Failure prefixes separate causes: `record` is an absent required persistence
 * record, `index` is a missing index product, `retrieval` is a completed lookup
 * that returned nothing, and `state` is product state contradicting the setup.
 */
export function checkDbEvidence(
  evidence: RagEvidence,
  session: RagSessionFacts,
  fixture: string,
): string[] {
  const failures: string[] = [];
  const document = evidence.document;
  const uploaded = session.document;
  const kbId = session.kb_id;
  const agentId = session.agent_id;
  // The persisted document must still be the ready revision created at setup.
  if (
    !document ||
    document.knowledge_base_id !== kbId ||
    document.status !== DocumentStatus.READY ||
    document.active_revision_id !== uploaded.active_revision_id
  ) {
    failures.push("state: document is not the active, ready document in the created KB");
  } else if (
    !existsSync(document.file_path) ||
    sha256(readFileSync(document.file_path)) !== sha256(readFileSync(fixture))
  ) {
    failures.push("state: uploaded source bytes differ from the versioned fixture");
  }
  // The Agent must have exactly the one grant the workflow created.
  if (
    JSON.stringify(evidence.kb_access) !== JSON.stringify([{ person_id: agentId, kb_id: kbId }])
  ) {
    failures.push("state: agent does not have exactly the intended KB access grant");
  }
  // Indexing must have produced chunks for the active revision.
  const chunks = new Map(evidence.active_chunks.map((row) => [row.id, row]));
  if (!chunks.size) failures.push("index: the active document has no indexed chunks");
  // Every answer needs a persisted work and a retrieval trace to be auditable.
  if (!evidence.works.length)
    failures.push("record: no persisted work records for the chat session");
  const traces = evidence.kb_usage_traces;
  if (!traces.length)
    failures.push("record: no KB retrieval trace was recorded for the chat session");
  // Each trace must authorize only the intended KB and cite chunks from the
  // uploaded revision; malformed metadata is a record failure, not a skip.
  for (const trace of traces) {
    let authorized;
    let handles;
    try {
      authorized = JSON.parse(trace.authorized_kb_ids_json);
      handles = JSON.parse(trace.evidence_handles_json);
    } catch (error) {
      failures.push(
        "record: trace " + trace.id + " has invalid JSON evidence metadata: " + errorMessage(error),
      );
      continue;
    }
    if (JSON.stringify(authorized) !== JSON.stringify([kbId])) {
      failures.push(
        "state: trace " +
          trace.id +
          " has unexpected authorized KBs: " +
          JSON.stringify(authorized),
      );
    }
    for (const handle of handles) {
      const chunk = chunks.get(handle.chunk_id);
      if (
        !chunk ||
        handle.kb_id !== kbId ||
        handle.document_id !== uploaded.id ||
        handle.revision_id !== uploaded.active_revision_id
      ) {
        failures.push("state: trace " + trace.id + " references an inaccessible or stale chunk");
      }
    }
  }
  // A completed lookup that returned nothing differs from a missing trace:
  // both are failures, but only this one means retrieval found no evidence.
  if (traces.length && !traces.some((trace) => trace.returned_evidence_count > 0)) {
    failures.push("retrieval: KB retrieval returned no source evidence");
  }
  return failures;
}

type Message = { id: number; person_id: number; content: string };
/** Ask the question only after the source and access grant are ready. */
export async function executeRagTest(base: string, child: ServerStatus, scenario: ScenarioData,
  record: RunRecord): Promise<void> {
  const dbPath = path.join(record.dataDir, "product", "db", "database.db");
  // Resolve the prepared user, Agent, and KB; the case cannot proceed if any
  // of them is missing, duplicated, or of the wrong Person type.
  const [people, bases] = await readSqlite(dbPath, [
    { sql: "SELECT id, name, type FROM persons WHERE name IN (?, ?)",
      params: [semanticUserName(record.caseName), RAG_AGENT_NAME] },
    { sql: "SELECT id FROM knowledge_bases WHERE name = ? AND description = ?",
      params: [RAG_KB_NAME, record.caseName] },
  ]);
  const user = people!.filter((row) => row.name === semanticUserName(record.caseName) && row.type === PersonType.HUMAN);
  const agent = people!.filter((row) => row.name === RAG_AGENT_NAME && row.type === PersonType.AI);
  if (user.length !== 1 || agent.length !== 1 || bases!.length !== 1)
    throw new ScenarioError("prepared RAG user, Agent, or KB is missing from product data");
  const userId = user[0]!.id;
  const agentId = agent[0]!.id;
  const kbId = bases![0]!.id;
  // The question may only be asked after the document is indexed and ready.
  const [documents] = await readSqlite(dbPath, [
    { sql: "SELECT id, active_revision_id, status FROM documents WHERE knowledge_base_id = ?", params: [kbId] },
  ]);
  if (documents!.length !== 1 || documents![0]!.active_revision_id < FIRST_REVISION_ID ||
      documents![0]!.status !== DocumentStatus.READY)
    throw new ScenarioError("prepared RAG document is missing or not ready in product data");
  const document = documents![0]!;
  // Baseline IDs exclude setup activity from the captured run.
  const baseline = await readBaseline(dbPath);
  const query = new URLSearchParams({ agent_id: String(agentId), message: scenario.question });
  const chat = await request(base, "POST", ChatRoute.CREATE + "?" + query);
  const session: RagSession = { user_id: userId, agent_id: agentId, kb_id: kbId,
    document, chat, messages: [] as DbRow[], agent_messages: [] as DbRow[],
    activities: [] as DbRow[] };
  // Write the session before monitoring so a crash still leaves partial context.
  record.writeDataJson("session.json", session);
  const stopped = await monitorSemanticCase({
    sink: record, dbPath, logPath: path.join(record.dataDir, "server.log"), child,
    agentId, triggerMessageId: chat.message_id, requestText: scenario.question,
    baseline, timeoutSeconds: scenario.limits.answer_timeout_seconds,
  });
  // Freeze the products of the exchange: the exact question and the reply thread.
  const messages = await request<Message[]>(base, "GET", ChatRoute.messages(chat.session_id));
  if (!messages.some((item) => item.id === chat.message_id && item.content === scenario.question))
    throw new ScenarioError("session did not preserve the exact question");
  const activities = await request(base, "GET", ChatRoute.activities(chat.session_id));
  // Complete the session artifact with the frozen reply and activity views.
  session.messages = messages;
  session.agent_messages = stopped.messages;
  session.activities = activities as DbRow[];
  session.stop_reason = stopped.reason;
  record.writeDataJson("session.json", session);
}
