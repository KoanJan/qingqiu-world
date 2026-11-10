/** Shared isolated execution and evidence mechanics for chat-driven S cases. */
import type { ChildProcess } from "node:child_process";
import { copyFileSync, existsSync, readFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import {
  HttpRequestError, RunRecord, buildServer, caseCommand, errorMessage, freePort,
  httpJson, isolatedServerEnv, providerEndpoint, readSqlite, sha256, startServer, stopServer, utcNow,
} from "../tools.ts";
import type { Status } from "../tools.ts";
import { missingProviderEnv } from "./provider_env.ts";
import { loadChatScenario, validateChatResult } from "./chat_contract.ts";
import type { ChatResult, ChatResultModel, ChatScenario, ChatScenarioModel } from "./chat_contract.ts";

export type DbRow = Record<string, any>;
export type DbEvidence = Record<string, DbRow[]>;
export type ChatSession = Record<string, any> & { completed: boolean };
const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../..");
const SERVER_START_TIMEOUT_MS = 60_000;
const SERVER_STOP_TIMEOUT_MS = 10_000;

/** A prerequisite or product request failed during the scenario. */
export class ScenarioError extends Error {}

/** Return product data only when the API business code succeeds. */
export async function request<T = DbRow>(base: string, method: string, route: string, payload: unknown = null): Promise<T> {
  let response;
  try {
    response = await httpJson(base, method, route, payload);
  } catch (error) {
    if (error instanceof HttpRequestError) throw new ScenarioError(error.message);
    throw error;
  }
  if (response.code !== 0)
    throw new ScenarioError(`${method} ${route}: business code ${response.code}: ${response.message}`);
  return response.data as T;
}

/** Read committed SQLite rows through a read-only connection. */
export async function readRows(dbPath: string, sql: string, params: Array<string | number> = []): Promise<DbRow[]> {
  const [rows] = await readSqlite(dbPath, [{ sql, params }]);
  return rows!;
}

/** Capture full rows of the declared evidence tables. */
export async function readTables(dbPath: string, tables: string[]): Promise<DbEvidence> {
  for (const table of tables) {
    if (!/^[a-z_]+$/.test(table)) throw new Error("invalid evidence table name");
  }
  const rows = await readSqlite(dbPath, tables.map((table) => ({ sql: `SELECT * FROM ${table} ORDER BY id` })));
  return Object.fromEntries(tables.map((table, index) => [table, rows[index]!]));
}

type Metadata = {
  case_name: string;
  scenario_sha256: string;
  started_at_utc: string;
  environment: { os: string; arch: string; node: string; isolated_data_root: boolean };
  subject_entry: string;
  execution_command: string;
  run_dir: string;
  llm_endpoint?: { host: string; sha256: string };
  llm_model_id?: string;
  embedding_endpoint?: { host: string; sha256: string };
  embedding_model_id?: string;
};

export type ChatWorkflow<S extends ChatScenario, T extends ChatResult> = {
  caseDir: string;
  scenarioModel: ChatScenarioModel<S>;
  resultModel: ChatResultModel<T, S>;
  subjectEntry: string;
  evidenceTables: string[];
  evidenceExplanation: string;
  failurePrefixes: string;
  /** Create case-specific agents, send the request, wait, and capture visible session data. */
  perform(base: string, userId: number, llmId: number, scenario: S, record: RunRecord, child: ChildProcess): Promise<void>;
  /** Check persisted provenance after a completed product run. */
  checkEvidence(evidence: DbEvidence, session: ChatSession, scenario: S): string[];
  extraResult?(scenario: S): Record<string, unknown>;
  extraReport?(scenario: S): string;
};

/** Run a chat case with its own isolated product and report directory. */
export async function runChatCase<S extends ChatScenario, T extends ChatResult>(workflow: ChatWorkflow<S, T>) {
  const { caseDir, scenarioModel, resultModel } = workflow;
  const record = new RunRecord(caseDir);
  const dataDir = record.dataDir;
  const started = utcNow();
  const command = caseCommand(ROOT, process.argv[1]);
  let scenario: S;
  let scenarioPath: string;
  try {
    [scenario, scenarioPath] = loadChatScenario(caseDir, scenarioModel);
  } catch (error) {
    const detail = `Scenario setup failed for ${record.caseName}: ${errorMessage(error)}`;
    record.writeReport(`# ${record.caseName} setup\n\nStatus: ERROR\n\n${detail}\n\nCommand: \`${command}\`\n\nNo product action ran; the scenario definition could not be validated.\n`);
    record.writeResult({
      track: "semantic", status: "ERROR", detail, execution_command: command,
      started_at_utc: started, ended_at_utc: utcNow(),
      limitations: ["Scenario definition could not be validated"],
    });
    console.error(`${detail}\nEvidence directory: ${record.root}`);
    return 2;
  }

  const metadata: Metadata = {
    case_name: record.caseName,
    scenario_sha256: sha256(readFileSync(scenarioPath)), started_at_utc: started,
    environment: { os: os.type(), arch: os.arch(), node: process.version, isolated_data_root: true },
    subject_entry: workflow.subjectEntry, execution_command: command,
    run_dir: record.root,
  };

  function finalize(status: Status, detail: string, failures: string[] = [], checksPerformed = false) {
    const limitations: string[] = [];
    if (status !== "ERROR") {
      limitations.push(
        "observed_model_calls is null: the chat Decide/Comprehension calls are not persisted as work interactions, so this case does not derive a model-call count; max_model_calls configures per-process cognitive requests, not a run-wide cap",
      );
    }
    if (status === "INCONCLUSIVE") limitations.push("Independent semantic judgment has not run");
    record.writeDataJson("capture.json", {
      status, mechanical_failures: failures, mechanical_checks_performed: checksPerformed,
      observed_model_calls: null,
    });
    const result = {
      schema_version: 1,
      scenario_sha256: metadata.scenario_sha256, track: "semantic", status, detail,
      execution_command: command, started_at_utc: started, ended_at_utc: utcNow(),
      request: scenario.request, subject_entry: workflow.subjectEntry,
      model: { llm: metadata.llm_model_id ?? null, embedding: metadata.embedding_model_id ?? null },
      environment: metadata.environment, limits: scenario.limits,
      observed_model_calls: null, mechanical_failures: failures,
      mechanical_checks_performed: checksPerformed, judge: null,
      criteria: [...scenario.required, ...scenario.forbidden].map((item) => ({
        id: item.id, status: "INCONCLUSIVE", observed: "", evidence: [],
      })),
      limitations, deviations: [], ...workflow.extraResult?.(scenario),
    };
    record.writeReport(
      `# ${record.caseName} run\n\nStatus: ${status}\n\n${detail}\n\n` +
      `Request: \`${scenario.request}\`\n\n${workflow.extraReport?.(scenario) ?? ""}` +
      `Command: \`${command}\`\n\n` +
      `Mechanical checks: ${checksPerformed ? "completed" : "not completed"}.\n\n` +
      `Mechanical failures: ${failures.length ? failures.join("; ") : "none recorded"}.\n\n` +
      `Failure prefixes: ${workflow.failurePrefixes}.\n\n` +
      "Semantic criteria: awaiting independent judgment.\n\n## Evidence\n\n" +
      "- `data/metadata.json`: scenario checksum, provider models, limits.\n" +
      "- `data/capture.json`: runner-captured mechanical facts, written before judgment.\n" +
      "- `data/session.json`: chat and visible session responses, if available.\n" +
      "- `data/stop_monitor.json`: status polls and AI stop reviews, if capture reached monitoring.\n" +
      `- \`data/db_evidence.json\`: read-only ${workflow.evidenceTables.join(", ")} rows, if available.\n` +
      "- `data/server_pre_stop.log`: runtime output before runner shutdown.\n" +
      "- `data/build.log` and `data/server.log`: build and owned-process output.\n\n" +
      workflow.evidenceExplanation + " Record the independent semantic verdict and evidence locations here.\n",
    );
    record.writeResult(result, (value) => {
      validateChatResult(value, record.root, scenario, record.caseDir, resultModel, false);
    });
  }

  const missing = missingProviderEnv();
  if (missing.length) {
    const detail = "Missing required environment variables: " + missing.join(", ");
    record.writeDataJson("metadata.json", metadata);
    finalize("ERROR", detail);
    console.error(`${detail}\nEvidence directory: ${record.root}`);
    return 2;
  }
  metadata.llm_endpoint = providerEndpoint(process.env.QINGQIU_TEST_LLM_BASE_URL!);
  metadata.llm_model_id = process.env.QINGQIU_TEST_LLM_MODEL_ID!;
  metadata.embedding_endpoint = providerEndpoint(process.env.QINGQIU_TEST_EMBEDDING_BASE_URL!);
  metadata.embedding_model_id = process.env.QINGQIU_TEST_EMBEDDING_MODEL_ID!;
  record.writeDataJson("metadata.json", metadata);
  console.log("Evidence directory: " + record.root);

  let child: ChildProcess | null = null;
  let status: Status = "ERROR";
  let detail = "Scenario did not complete setup";
  let failures: string[] = [];
  let checksPerformed = false;
  try {
    const binary = await buildServer(ROOT, dataDir);
    const port = await freePort();
    const base = `http://127.0.0.1:${port}`;
    const env = isolatedServerEnv(dataDir, port, {
      COGNITIVE_MAX_REQUESTS: String(scenario.limits.max_model_calls),
      FOCUSED_WORK_MAX_ITERATIONS: String(scenario.limits.max_model_calls),
    });
    child = await startServer(binary, dataDir, env, base, "server.log", SERVER_START_TIMEOUT_MS);
    const user = await request(base, "PUT", "/api/user-profile", { name: `${record.caseName} request user` });
    const llm = await request(base, "POST", "/api/llm-configs", {
      name: `${record.caseName} test LLM`, model_id: process.env.QINGQIU_TEST_LLM_MODEL_ID,
      base_url: process.env.QINGQIU_TEST_LLM_BASE_URL, api_key: process.env.QINGQIU_TEST_LLM_API_KEY,
    });
    await request(base, "PUT", "/api/embedding-config", {
      name: `${record.caseName} test embedding`, model_id: process.env.QINGQIU_TEST_EMBEDDING_MODEL_ID,
      base_url: process.env.QINGQIU_TEST_EMBEDDING_BASE_URL, api_key: process.env.QINGQIU_TEST_EMBEDDING_API_KEY,
    });
    // Embedding services initialize at startup; restart this owned process.
    await stopServer(child, SERVER_STOP_TIMEOUT_MS);
    child = null;
    child = await startServer(binary, dataDir, env, base, "server.log", SERVER_START_TIMEOUT_MS);
    await workflow.perform(base, user.id, llm.id, scenario, record, child);
    status = "INCONCLUSIVE";
    const completedSession = JSON.parse(readFileSync(path.join(dataDir, "session.json"), "utf8")) as ChatSession;
    detail = completedSession.stop_reason === "timeout"
      ? "Capture reached the scenario timeout; semantic completion is unverified"
      : `Capture stopped (${completedSession.stop_reason}); independent semantic judgment is required`;
  } catch (error) {
    detail = errorMessage(error);
  } finally {
    // Freeze the observed state before shutdown can interrupt an active Work.
    const sessionPath = path.join(dataDir, "session.json");
    if (existsSync(sessionPath)) {
      const session = JSON.parse(readFileSync(sessionPath, "utf8")) as ChatSession;
      try {
        const dbPath = path.join(dataDir, "product", "db", "database.db");
        const [latestMessages] = await readSqlite(dbPath, [{
          sql: "SELECT id, session_id, person_id, content FROM messages " +
            "WHERE session_id = ? OR (person_id = ? AND id >= ?) ORDER BY id",
          params: [session.chat.session_id, session.agent_id, session.chat.message_id],
        }]);
        session.pre_stop_messages = latestMessages;
        record.writeDataJson("session.json", session);
        const evidence = await readTables(dbPath, workflow.evidenceTables);
        record.writeDataJson("db_evidence.json", evidence);
        if (session.completed && status !== "ERROR") {
          failures = workflow.checkEvidence(evidence, session, scenario);
          checksPerformed = true;
          if (failures.length) {
            status = "FAIL";
            detail = "Mechanical provenance checks failed: " + failures.join("; ");
          }
        }
      } catch (error) {
        if (status !== "ERROR") status = "INCONCLUSIVE";
        detail += "; read-only DB evidence unavailable or malformed: " + errorMessage(error);
      }
    }
    try {
      if (existsSync(path.join(dataDir, "server.log")))
        copyFileSync(path.join(dataDir, "server.log"), path.join(dataDir, "server_pre_stop.log"));
    } catch (error) {
      detail += "; pre-stop log capture failed: " + errorMessage(error);
    }
    if (child !== null) {
      try {
        await stopServer(child, SERVER_STOP_TIMEOUT_MS);
      } catch (error) {
        detail += "; cleanup: " + errorMessage(error);
      }
    }
    finalize(status, detail, failures, checksPerformed);
  }
  console.log(`${record.caseName}: ${status}: ${detail}`);
  return { PASS: 0, FAIL: 1, ERROR: 2, INCONCLUSIVE: 3 }[status];
}
