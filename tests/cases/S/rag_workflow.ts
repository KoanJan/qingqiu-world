/**
 * RAG workflow for the scripted semantic cases.
 *
 * The workflow supplies only the RAG product phases and evidence shape; the
 * shared runner owns the run directory, provider preflight, capture, and result.
 */
import path from "node:path";
import { readSqlite } from "../tools.ts";
import type { ScenarioData } from "./rag_contract.ts";
import {
  loadCaseContract,
  loadScenario,
  ResultBase,
  Scenario,
  SUBJECT_ENTRY,
  validateResult,
} from "./rag_contract.ts";
import { prepareRagData } from "./rag_prepare_data.ts";
import { checkDbEvidence, executeRagTest, readDbEvidence } from "./rag_execute_test.ts";
import type { RagSession } from "./rag_execute_test.ts";
import { readSessionArtifact } from "./product_api.ts";
import { InteractionType } from "../product_model.ts";
import { requireThat } from "./semantic_contract.ts";
import type { SemanticWorkflow } from "./semantic_runner.ts";

/** Build the RAG workflow bound to one case directory. */
export function ragWorkflow(caseDir: string): SemanticWorkflow<ScenarioData> {
  return {
    caseDir,
    subjectEntry: SUBJECT_ENTRY,
    failurePrefixes:
      "record (required persistence absent), index (index product missing), " +
      "retrieval (lookup returned no source evidence), state (product state contradicts setup)",
    evidenceDescription:
      "read-only document, access, chunk, trace, and raw interaction records, if available",
    evidenceExplanation:
      "The runner checks active document state, uploaded source bytes, the sole KB grant, " +
      "indexed chunks, retrieval scope, and evidence handle provenance. Review the exact answer " +
      "against the scenario criteria and original source.",

    async loadDefinition(dir) {
      const contract = await loadCaseContract(dir);
      const [scenario, scenarioPath] = loadScenario(dir, contract?.Scenario ?? Scenario);
      const resultModel = contract?.Result ?? ResultBase;
      return {
        scenario,
        scenarioPath,
        validateResult: (value, runDir, caseDirPath) => {
          validateResult(value, runDir, scenario, caseDirPath, false, resultModel);
        },
      };
    },

    fixturePath(scenario, dir) {
      return path.join(dir, scenario.fixture);
    },

    prepareData(context) {
      requireThat(context.fixture !== null, "RAG workflow requires a staged fixture");
      return prepareRagData(
        context.base,
        context.server,
        context.scenario,
        context.record,
        context.fixture,
        context.llmId,
      );
    },

    executeTest: (context) =>
      executeRagTest(context.base, context.server, context.scenario, context.record),

    capturedSession(record) {
      // A case that never wrote a session artifact did not reach capture.
      const session = readSessionArtifact(record) as RagSession | null;
      if (session === null) return null;
      // The monitor always records a stop reason; a missing one is a contract
      // violation that the runner must report rather than silently judge.
      if (session.stop_reason === undefined)
        throw new Error("Captured RAG session has no stop reason");
      return { stopReason: session.stop_reason, evidence: session };
    },

    async captureEvidence({ record, session: rawSession, dbPath, fixture, status }) {
      const session = rawSession as RagSession;
      requireThat(fixture !== null, "RAG workflow requires a staged fixture");
      // Freeze the final message list before shutdown so late writes are
      // visible to the provenance checks and the judge.
      const [latestMessages] = await readSqlite(dbPath, [{
        sql: "SELECT id, session_id, person_id, content FROM messages WHERE session_id = ? ORDER BY id",
        params: [session.chat.session_id],
      }]);
      session.messages = latestMessages;
      record.writeDataJson("session.json", session);
      // Read every declared evidence table in one read-only connection so the
      // snapshot is internally consistent.
      const evidence = await readDbEvidence(
        dbPath,
        session.chat.session_id,
        session.document.id,
        session.document.active_revision_id,
      );
      // ERROR runs keep evidence for diagnosis but never run mechanical checks.
      const checksPerformed = status !== "ERROR";
      return {
        evidence,
        // Only persisted response interactions count; this is not a total
        // provider-call count.
        modelCalls: evidence.works.length
          ? evidence.interactions.filter((item) => item.type === InteractionType.RESPONSE).length
          : null,
        failures: checksPerformed ? checkDbEvidence(evidence, session, fixture) : [],
        checksPerformed,
      };
    },

    resultFields(scenario, metadata) {
      // The staged fixture hash proves which source revision the answer used.
      requireThat(typeof metadata.fixture_sha256 === "string", "RAG result requires a fixture hash");
      return { fixture_sha256: metadata.fixture_sha256, question: scenario.question };
    },

    limitations(_status, modelCalls) {
      const items = [
        "observed_model_calls counts only persisted type=2 interactions in this " +
          "session's works; it is not a total provider-call count. max_model_calls " +
          "configures per-process cognitive requests and focused-work iterations, " +
          "not a run-wide call cap",
      ];
      if (modelCalls === null) {
        items.push(
          "Work-model interaction count could not be derived from persisted works and interactions",
        );
      }
      return items;
    },

    reportSections(_scenario, outcome) {
      return (
        `Observed work-model interactions: ${outcome.modelCalls ?? "unknown"}; ` +
        "this excludes other model calls. max_model_calls configures per-process cognitive " +
        "requests and focused-work iterations, not a run-wide call cap.\n\n"
      );
    },
  };
}
