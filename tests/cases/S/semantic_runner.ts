/**
 * Shared runner for scripted RAG semantic cases.
 *
 * The case supplies its directory, scenario, and fixture. Optional case models
 * add rules to the shared RAG contract without copying the product workflow.
 */
import { randomUUID } from "node:crypto";
import type { ChildProcess } from "node:child_process";
import { fileURLToPath } from "node:url";
import { copyFileSync, existsSync, readFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import {
  HttpRequestError,
  RunRecord,
  buildServer,
  caseCommand,
  freePort,
  gitState,
  httpJson,
  isolatedServerEnv,
  providerEndpoint,
  readSqlite,
  sha256,
  sleep,
  startServer,
  stopServer,
  utcNow,
} from "../tools.ts";
import { errorMessage } from "../tools.ts";
import type { Revision, Status } from "../tools.ts";
import { loadCaseContract, loadScenario, ResultBase, validateResult } from "./base_contracts.ts";
import type { ScenarioData } from "./base_contracts.ts";
import { missingProviderEnv } from "./provider_env.ts";
import {
  ACTIVITIES_PATH,
  CHAT_PATH,
  MESSAGES_PATH,
  SUBJECT_ENTRY,
  Scenario,
} from "./rag_contract.ts";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../..");
// Product model values mirror server/internal/model/document.go and interaction.go.
const DOCUMENT_STATUS_READY = 2;
const DOCUMENT_STATUS_FAILED = 3;
const INTERACTION_TYPE_RESPONSE = 2;
const INDEX_POLL_MS = 1_000;
const ANSWER_POLL_MS = 2_000;
const SERVER_START_TIMEOUT_MS = 60_000;
const SERVER_STOP_TIMEOUT_MS = 10_000;
const ACTIVITY_PAGE_LIMIT = 200;

/** A prerequisite or real product operation failed. */
export class ScenarioError extends Error {}

type ApiObject = Record<string, any>;
type Message = { id: number; person_id: number; content: string };
type DbEvidence = {
  document: ApiObject | null;
  kb_access: ApiObject[];
  works: ApiObject[];
  interactions: ApiObject[];
  kb_usage_traces: ApiObject[];
  active_chunks: ApiObject[];
};
type Metadata = {
  case_name: string;
  scenario_version: number;
  scenario_sha256: string;
  fixture_sha256: string;
  started_at_utc: string;
  environment: { os: string; arch: string; node: string; isolated_data_root: boolean };
  subject_entry_point: string;
  execution_command: string;
  git: Revision;
  run_dir: string;
  llm_endpoint?: { host: string; sha256: string };
  llm_model_id?: string;
  embedding_endpoint?: { host: string; sha256: string };
  embedding_model_id?: string;
};

/** Return product data only when the API business code succeeds. */
export async function request<T = ApiObject>(
  base: string,
  method: string,
  route: string,
  payload: unknown = null,
  contentType: string | null = null,
): Promise<T> {
  let result;
  try {
    result = await httpJson(base, method, route, payload, { contentType });
  } catch (error) {
    if (error instanceof HttpRequestError) throw new ScenarioError(error.message);
    throw error;
  }
  if (result.code !== 0) {
    throw new ScenarioError(
      method + " " + route + ": business code " + result.code + ": " + result.message,
    );
  }
  return result.data as T;
}

export async function uploadDocument(base: string, kbId: number, fixture: string) {
  const boundary = "qingqiu-" + randomUUID().replaceAll("-", "");
  const head =
    "--" +
    boundary +
    '\r\nContent-Disposition: form-data; name="file"; filename="' +
    path.basename(fixture) +
    '"\r\nContent-Type: text/markdown\r\n\r\n';
  const body = Buffer.concat([
    Buffer.from(head),
    readFileSync(fixture),
    Buffer.from("\r\n--" + boundary + "--\r\n"),
  ]);
  return request(
    base,
    "POST",
    "/api/kb/" + kbId + "/documents",
    body,
    "multipart/form-data; boundary=" + boundary,
  );
}

export async function waitReady(
  base: string,
  kbId: number,
  docId: number,
  child: ChildProcess,
  seconds: number,
) {
  const deadline = Date.now() + seconds * 1000;
  while (Date.now() < deadline) {
    if (child.exitCode !== null || child.signalCode !== null) {
      throw new ScenarioError(
        "server exited during document indexing: " + (child.exitCode ?? child.signalCode),
      );
    }
    const doc = await request(base, "GET", "/api/kb/" + kbId + "/documents/" + docId);
    if (doc.status === DOCUMENT_STATUS_FAILED)
      throw new ScenarioError("document processing failed: " + (doc.error_message ?? ""));
    if (doc.status === DOCUMENT_STATUS_READY && doc.active_revision_id > 0) return doc;
    await sleep(INDEX_POLL_MS);
  }
  throw new ScenarioError(
    "document did not reach an active ready revision within " + seconds + " seconds",
  );
}

export async function waitAnswer(
  base: string,
  sessionId: number,
  agentId: number,
  child: ChildProcess,
  seconds: number,
): Promise<[Message[], Message]> {
  const deadline = Date.now() + seconds * 1000;
  while (Date.now() < deadline) {
    if (child.exitCode !== null || child.signalCode !== null) {
      throw new ScenarioError(
        "server exited before answer: " + (child.exitCode ?? child.signalCode),
      );
    }
    const messages = await request<Message[]>(base, "GET", MESSAGES_PATH + "/" + sessionId);
    const answers = messages.filter(
      (item) => item.person_id === agentId && String(item.content ?? "").trim(),
    );
    if (answers.length) return [messages, answers.at(-1)!];
    await sleep(ANSWER_POLL_MS);
  }
  throw new ScenarioError("agent produced no message within " + seconds + " seconds");
}

export async function readDbEvidence(
  dbPath: string,
  sessionId: number,
  docId: number,
  revisionId: number,
): Promise<DbEvidence> {
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

/**
 * Check stable provenance facts; leave answer meaning to the independent judge.
 *
 * Failure prefixes separate causes: `record` is an absent required persistence
 * record, `index` is a missing index product, `retrieval` is a completed lookup
 * that returned nothing, and `state` is product state contradicting the setup.
 */
export function checkDbEvidence(
  evidence: DbEvidence,
  session: ApiObject,
  fixture: string,
): string[] {
  const failures: string[] = [];
  const document = evidence.document;
  const uploaded = session.document;
  const kbId = session.kb_id;
  const agentId = session.agent_id;
  if (
    !document ||
    document.knowledge_base_id !== kbId ||
    document.status !== DOCUMENT_STATUS_READY ||
    document.active_revision_id !== uploaded.active_revision_id
  ) {
    failures.push("state: document is not the active, ready document in the created KB");
  } else if (
    !existsSync(document.file_path) ||
    sha256(readFileSync(document.file_path)) !== sha256(readFileSync(fixture))
  ) {
    failures.push("state: uploaded source bytes differ from the versioned fixture");
  }
  if (
    JSON.stringify(evidence.kb_access) !== JSON.stringify([{ person_id: agentId, kb_id: kbId }])
  ) {
    failures.push("state: agent does not have exactly the intended KB access grant");
  }
  const chunks = new Map(evidence.active_chunks.map((row) => [row.id, row]));
  if (!chunks.size) failures.push("index: the active document has no indexed chunks");
  if (!evidence.works.length)
    failures.push("record: no persisted work records for the chat session");
  const traces = evidence.kb_usage_traces;
  if (!traces.length)
    failures.push("record: no KB retrieval trace was recorded for the chat session");
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
  if (traces.length && !traces.some((trace) => trace.returned_evidence_count > 0)) {
    failures.push("retrieval: KB retrieval returned no source evidence");
  }
  return failures;
}

function writeResult(
  record: RunRecord,
  resultModel: typeof ResultBase,
  scenario: ScenarioData,
  metadata: Metadata,
  status: Status,
  detail: string,
  modelCalls: number | null = null,
  mechanicalFailures: string[] = [],
  mechanicalChecksPerformed = false,
) {
  const limitations =
    status === "INCONCLUSIVE" ? ["Independent semantic judgment has not run"] : [];
  if (status !== "ERROR") {
    limitations.push(
      "observed_model_calls counts only persisted type=2 interactions in this " +
        "session's works; it is not a total provider-call count. max_model_calls " +
        "configures per-process cognitive requests and focused-work iterations, " +
        "not a run-wide call cap",
    );
  }
  if (modelCalls === null && status !== "ERROR") {
    limitations.push(
      "Work-model interaction count could not be derived from persisted works and interactions",
    );
  }
  const result = {
    schema_version: 1,
    scenario_version: scenario.version,
    scenario_sha256: metadata.scenario_sha256,
    track: "semantic",
    status,
    detail,
    execution_command: metadata.execution_command,
    started_at_utc: metadata.started_at_utc,
    ended_at_utc: utcNow(),
    tested_revision: metadata.git,
    finished_revision: gitState(ROOT, true),
    fixture_sha256: metadata.fixture_sha256,
    question: scenario.question,
    subject_entry: SUBJECT_ENTRY,
    model: { llm: metadata.llm_model_id ?? null, embedding: metadata.embedding_model_id ?? null },
    environment: metadata.environment,
    limits: scenario.limits,
    observed_model_calls: modelCalls,
    mechanical_failures: mechanicalFailures,
    mechanical_checks_performed: mechanicalChecksPerformed,
    judge: null,
    criteria: [...scenario.required, ...scenario.forbidden].map((item) => ({
      id: item.id,
      status: "INCONCLUSIVE",
      observed: "",
      evidence: [],
    })),
    limitations,
    deviations: [],
  };
  record.writeResult(result, (value) =>
    validateResult(value, record.root, scenario, record.caseDir, false, resultModel),
  );
}

/** Run the shared scripted RAG workflow for one case directory. */
export async function run(caseDir: string) {
  const record = new RunRecord(caseDir);
  const dataDir = record.dataDir;
  const started = utcNow();
  const command = caseCommand(ROOT, process.argv[1]);
  let scenario;
  let scenarioPath;
  let resultModel: typeof ResultBase = ResultBase;
  try {
    const contract = await loadCaseContract(caseDir);
    resultModel = contract?.Result ?? ResultBase;
    [scenario, scenarioPath] = loadScenario(caseDir, contract?.Scenario ?? Scenario);
  } catch (error) {
    const detail = "Scenario setup failed for " + record.caseName + ": " + errorMessage(error);
    record.writeReport(
      "# " +
        record.caseName +
        " setup\n\nStatus: ERROR\n\n" +
        detail +
        "\n\nCommand: " +
        command +
        "\n\nNo product action ran; the scenario definition could not be validated.\n",
    );
    record.writeResult({
      track: "semantic",
      status: "ERROR",
      detail,
      execution_command: command,
      started_at_utc: started,
      ended_at_utc: utcNow(),
      tested_revision: gitState(ROOT, true),
      limitations: ["Scenario definition could not be validated"],
    });
    console.error(detail + "\nEvidence directory: " + record.root);
    return 2;
  }

  const sourceFixture = path.join(caseDir, scenario.fixture);
  const fixtureBytes = readFileSync(sourceFixture);
  const metadata: Metadata = {
    case_name: record.caseName,
    scenario_version: scenario.version,
    scenario_sha256: sha256(readFileSync(scenarioPath)),
    fixture_sha256: sha256(fixtureBytes),
    started_at_utc: started,
    environment: {
      os: os.type(),
      arch: os.arch(),
      node: process.version,
      isolated_data_root: true,
    },
    subject_entry_point: SUBJECT_ENTRY,
    execution_command: command,
    git: gitState(ROOT, true),
    run_dir: record.root,
  };
  const missing = missingProviderEnv();
  if (missing.length) {
    const detail = "Missing required environment variables: " + missing.join(", ");
    record.writeReport(
      "# " +
        record.caseName +
        " run\n\nStatus: ERROR\n\n" +
        detail +
        "\n\nCommand: " +
        command +
        "\n\nTested revision: " +
        metadata.git.head +
        "\n\nNo product action ran. Inspect data/metadata.json for setup details.\n",
    );
    record.writeDataJson("metadata.json", metadata);
    record.writeDataJson("capture.json", {
      status: "ERROR",
      mechanical_failures: [],
      mechanical_checks_performed: false,
      observed_model_calls: null,
    });
    writeResult(record, resultModel, scenario, metadata, "ERROR", detail);
    console.error(detail + "\nEvidence directory: " + record.root);
    return 2;
  }

  const fixture = path.join(dataDir, path.basename(sourceFixture));
  copyFileSync(sourceFixture, fixture);
  metadata.llm_endpoint = providerEndpoint(process.env.QINGQIU_TEST_LLM_BASE_URL!);
  metadata.llm_model_id = process.env.QINGQIU_TEST_LLM_MODEL_ID!;
  metadata.embedding_endpoint = providerEndpoint(process.env.QINGQIU_TEST_EMBEDDING_BASE_URL!);
  metadata.embedding_model_id = process.env.QINGQIU_TEST_EMBEDDING_MODEL_ID!;
  record.writeDataJson("metadata.json", metadata);
  console.log("Evidence directory: " + record.root);

  let child = null;
  let status: Status = "ERROR";
  let detail = "Scenario did not complete setup";
  let mechanicalFailures: string[] = [];
  let mechanicalChecksPerformed = false;
  try {
    const binary = await buildServer(ROOT, dataDir);
    const port = await freePort();
    const base = "http://127.0.0.1:" + port;
    const serverEnv = isolatedServerEnv(dataDir, port, {
      COGNITIVE_MAX_REQUESTS: String(scenario.limits.max_model_calls),
      FOCUSED_WORK_MAX_ITERATIONS: String(scenario.limits.max_model_calls),
    });
    child = await startServer(binary, dataDir, serverEnv, base, "server.log", SERVER_START_TIMEOUT_MS);
    const user = await request(base, "PUT", "/api/user-profile", { name: "RAG source test user" });
    const llm = await request(base, "POST", "/api/llm-configs", {
      name: "RAG source test LLM",
      model_id: process.env.QINGQIU_TEST_LLM_MODEL_ID,
      base_url: process.env.QINGQIU_TEST_LLM_BASE_URL,
      api_key: process.env.QINGQIU_TEST_LLM_API_KEY,
    });
    await request(base, "PUT", "/api/embedding-config", {
      name: "RAG source test embedding",
      model_id: process.env.QINGQIU_TEST_EMBEDDING_MODEL_ID,
      base_url: process.env.QINGQIU_TEST_EMBEDDING_BASE_URL,
      api_key: process.env.QINGQIU_TEST_EMBEDDING_API_KEY,
    });
    // Background embedding services are initialized at startup, so restart
    // this owned process after initial configuration before Agent work.
    await stopServer(child, SERVER_STOP_TIMEOUT_MS);
    child = null;
    child = await startServer(binary, dataDir, serverEnv, base, "server.log", SERVER_START_TIMEOUT_MS);
    const agent = await request(base, "POST", "/api/agents", {
      name: "RAG source reader",
      description: "Answer from the supplied KB source",
      character_settings: "Use the KB source and explain uncertainty when it is incomplete.",
      llm_config_id: llm.id,
    });
    const kb = await request(base, "POST", "/api/kb", {
      name: "RAG source test KB",
      description: record.caseName,
    });
    let doc = await uploadDocument(base, kb.id, fixture);
    doc = await waitReady(base, kb.id, doc.id, child, scenario.limits.index_timeout_seconds);
    await request(base, "POST", "/api/kb/" + kb.id + "/access", { person_id: agent.id });
    const access = await request<ApiObject[]>(base, "GET", "/api/kb/" + kb.id + "/access");
    if (!access.some((item) => item.id === agent.id))
      throw new ScenarioError("agent KB grant was not visible");
    const query = new URLSearchParams({ agent_id: String(agent.id), message: scenario.question });
    const chat = await request(base, "POST", CHAT_PATH + "?" + query);
    const [messages, answer] = await waitAnswer(
      base,
      chat.session_id,
      agent.id,
      child,
      scenario.limits.answer_timeout_seconds,
    );
    if (
      !messages.some((item) => item.id === chat.message_id && item.content === scenario.question)
    ) {
      throw new ScenarioError("session did not preserve the exact question");
    }
    const activities = await request(
      base,
      "GET",
      ACTIVITIES_PATH + "/" + chat.session_id + "/activities?limit=" + ACTIVITY_PAGE_LIMIT,
    );
    record.writeDataJson("session.json", {
      user_id: user.id,
      agent_id: agent.id,
      kb_id: kb.id,
      document: doc,
      chat,
      messages,
      answer,
      activities,
    });
    status = "INCONCLUSIVE";
    detail = "Real product run completed; independent semantic judgment is required";
  } catch (error) {
    detail = errorMessage(error);
  } finally {
    if (child !== null) {
      try {
        await stopServer(child, SERVER_STOP_TIMEOUT_MS);
      } catch (error) {
        detail += "; cleanup: " + errorMessage(error);
      }
    }
    const sessionPath = path.join(dataDir, "session.json");
    let modelCalls = null;
    if (existsSync(sessionPath)) {
      const session = JSON.parse(readFileSync(sessionPath, "utf8"));
      try {
        const evidence = await readDbEvidence(
          path.join(dataDir, "product", "db", "database.db"),
          session.chat.session_id,
          session.document.id,
          session.document.active_revision_id,
        );
        record.writeDataJson("db_evidence.json", evidence);
        modelCalls = evidence.works.length
          ? evidence.interactions.filter((item) => item.type === INTERACTION_TYPE_RESPONSE).length
          : null;
        const failures = checkDbEvidence(evidence, session, fixture);
        mechanicalChecksPerformed = true;
        if (failures.length) {
          mechanicalFailures = failures;
          status = "FAIL";
          detail = "Mechanical provenance checks failed: " + failures.join("; ");
        }
      } catch (error) {
        status = "INCONCLUSIVE";
        detail += "; read-only DB evidence unavailable: " + errorMessage(error);
      }
    }
    record.writeDataJson("capture.json", {
      status,
      mechanical_failures: mechanicalFailures,
      mechanical_checks_performed: mechanicalChecksPerformed,
      observed_model_calls: modelCalls,
    });
    record.writeReport(
      "# " +
        record.caseName +
        " v" +
        scenario.version +
        " run\n\n" +
        "Status: " +
        status +
        "\n\n" +
        detail +
        "\n\n" +
        "Command: " +
        command +
        "\n\nTested revision: " +
        metadata.git.head +
        "\n\n" +
        "Mechanical checks: " +
        (mechanicalChecksPerformed ? "completed" : "not completed") +
        ".\n\n" +
        "Mechanical failures: " +
        (mechanicalFailures.length ? mechanicalFailures.join("; ") : "none recorded") +
        ".\n\n" +
        "Observed work-model interactions: " +
        (modelCalls ?? "unknown") +
        "; this excludes other model calls. " +
        "max_model_calls configures per-process cognitive requests and focused-work iterations, " +
        "not a run-wide call cap.\n\n" +
        "Failure prefixes: record (required persistence absent), index (index product missing), " +
        "retrieval (lookup returned no source evidence), state (product state contradicts setup).\n\n" +
        "Semantic criteria: awaiting independent judgment.\n\n" +
        "## Artifacts and checks\n\n" +
        "- data/metadata.json: tested Git revision, run time, fixture checksum, and provider models.\n" +
        "- data/capture.json: runner-captured mechanical facts, written before judgment.\n" +
        "- data/" +
        path.basename(fixture) +
        ": exact source uploaded through the API.\n" +
        "- data/session.json: product responses, exact answer, and visible activities, if chat completed.\n" +
        "- data/db_evidence.json: read-only document, access, chunk, trace, and raw interaction records, if available.\n" +
        "- data/build.log and data/server.log: build and owned-process output.\n\n" +
        "The runner checks active document state, uploaded source bytes, the sole KB grant, indexed chunks, " +
        "retrieval scope, and evidence handle provenance. Review the exact answer against the versioned " +
        "scenario criteria and original source. Record the independent semantic verdict, criterion " +
        "reasoning, and evidence locations here.\n",
    );
    writeResult(
      record,
      resultModel,
      scenario,
      metadata,
      status,
      detail,
      modelCalls,
      mechanicalFailures,
      mechanicalChecksPerformed,
    );
  }
  console.log(record.caseName + ": " + status + ": " + detail);
  return { PASS: 0, FAIL: 1, ERROR: 2, INCONCLUSIVE: 3 }[status];
}
