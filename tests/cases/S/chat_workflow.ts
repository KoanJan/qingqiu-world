/**
 * Chat-driven workflow for the semantic cases.
 *
 * Each case supplies its own scenario/result models, product phases, and
 * evidence tables; this module turns them into the shared runner interface.
 */
import { readSqlite } from "../tools.ts";
import type { RunRecord, ServerStatus } from "../tools.ts";
import { validateChatResult, loadChatScenario } from "./chat_contract.ts";
import type { ChatResult, ChatResultModel, ChatScenario, ChatScenarioModel } from "./chat_contract.ts";
import { readSessionArtifact, readTables } from "./product_api.ts";
import type { DbEvidence, DbRow } from "./product_api.ts";
import type { StopReason } from "./capture_monitor.ts";
import type { SemanticWorkflow } from "./semantic_runner.ts";

/** One chat capture session; `completed` gates mechanical evidence checks. */
export type ChatSession = {
  user_id: number;
  agent_id: number;
  chat: { session_id: number; message_id: number };
  messages: DbRow[];
  activities: DbRow[];
  agent_messages?: DbRow[];
  source_messages?: DbRow[];
  target_sessions?: Array<{ session_id: number; messages: DbRow[] }>;
  target_person_id?: number;
  pre_stop_messages?: DbRow[];
  stop_reason?: StopReason;
  completed: boolean;
};

/** Case-owned chat phases and evidence description. */
export type ChatWorkflowConfig<S extends ChatScenario, T extends ChatResult> = {
  caseDir: string;
  scenarioModel: ChatScenarioModel<S>;
  resultModel: ChatResultModel<T, S>;
  subjectEntry: string;
  evidenceTables: string[];
  evidenceExplanation: string;
  failurePrefixes: string;
  /** Build the case's starting product state before sending its test request. */
  prepareData(base: string, llmId: number, scenario: S): Promise<void>;
  /** Trigger the request and capture the resulting product behavior. */
  executeTest(base: string, scenario: S, record: RunRecord, server: ServerStatus): Promise<void>;
  /** Check persisted provenance after a completed product run. */
  checkEvidence(evidence: DbEvidence, session: ChatSession, scenario: S): string[];
  extraResult?(scenario: S): Record<string, unknown>;
  extraReport?(scenario: S): string;
};

/** Build the shared runner interface for one chat case. */
export function chatWorkflow<S extends ChatScenario, T extends ChatResult>(
  config: ChatWorkflowConfig<S, T>,
): SemanticWorkflow<S> {
  return {
    caseDir: config.caseDir,
    subjectEntry: config.subjectEntry,
    failurePrefixes: config.failurePrefixes,
    evidenceDescription: `read-only ${config.evidenceTables.join(", ")} rows, if available`,
    evidenceExplanation: config.evidenceExplanation,

    async loadDefinition(caseDir) {
      const [scenario, scenarioPath] = loadChatScenario(caseDir, config.scenarioModel);
      return {
        scenario,
        scenarioPath,
        validateResult: (value, runDir, caseDirPath) => {
          validateChatResult(value, runDir, scenario, caseDirPath, config.resultModel, false);
        },
      };
    },

    prepareData: (context) =>
      config.prepareData(context.base, context.llmId, context.scenario),

    executeTest: (context) =>
      config.executeTest(context.base, context.scenario, context.record, context.server),

    capturedSession(record) {
      // A case that never wrote a session artifact did not reach capture.
      const session = readSessionArtifact(record) as ChatSession | null;
      if (session === null) return null;
      // The monitor always records a stop reason; a missing one is a contract
      // violation that the runner must report rather than silently judge.
      if (session.stop_reason === undefined)
        throw new Error("Captured chat session has no stop reason");
      return { stopReason: session.stop_reason, evidence: session };
    },

    async captureEvidence({ record, scenario, session: rawSession, dbPath, status }) {
      const session = rawSession as ChatSession;
      // Re-read messages at freeze time so writes after the final poll are
      // included; this is the snapshot the evidence checks and judge will see.
      const [latestMessages] = await readSqlite(dbPath, [{
        sql: "SELECT id, session_id, person_id, content FROM messages " +
          "WHERE session_id = ? OR (person_id = ? AND id >= ?) ORDER BY id",
        params: [session.chat.session_id, session.agent_id, session.chat.message_id],
      }]);
      session.pre_stop_messages = latestMessages;
      record.writeDataJson("session.json", session);
      // Declared evidence tables are read after the snapshot, so both describe
      // the same pre-shutdown product state.
      const evidence = await readTables(dbPath, config.evidenceTables);
      // Mechanical checks require a completed capture and skip ERROR runs,
      // which record evidence for diagnosis only.
      const checksPerformed = Boolean(session.completed) && status !== "ERROR";
      return {
        evidence,
        modelCalls: null,
        failures: checksPerformed
          ? config.checkEvidence(evidence, session, scenario)
          : [],
        checksPerformed,
      };
    },

    resultFields: (scenario) => ({
      request: scenario.request,
      ...config.extraResult?.(scenario),
    }),

    limitations: () => [
      "observed_model_calls is null: the chat Decide/Comprehension calls are not persisted as " +
        "work interactions, so this case does not derive a model-call count; max_model_calls " +
        "configures per-process cognitive requests, not a run-wide cap",
    ],

    reportSections: (scenario) =>
      `Request: \`${scenario.request}\`\n\n${config.extraReport?.(scenario) ?? ""}`,
  };
}
