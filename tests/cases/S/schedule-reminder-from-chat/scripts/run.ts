/** Run the chat-to-reminder case against an isolated product server. */
import { fileURLToPath } from "node:url";
import path from "node:path";
import { chatWorkflow } from "../../chat_workflow.ts";
import { runSemanticCase } from "../../semantic_runner.ts";
import { ChatRoute } from "../../product_routes.ts";
import { Result, Scenario } from "./case_contract.ts";
import { prepareData } from "./prepare_data.ts";
import { executeTest, checkDbEvidence } from "./execute_test.ts";

const CASE_DIR = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

// Product constants mirrored from server/internal/model and runtime action code.
const SUBJECT_ENTRY =
  `POST ${ChatRoute.CREATE}; GET ${ChatRoute.MESSAGES}/:id; ` +
  `GET ${ChatRoute.SESSIONS}/:id/activities; ` +
  "read-only SELECT on messages, events, decisions, actions, action_effects, scheduled_events";

/** Run this case's owned executor. */
export async function run() {
  // The shared runner owns lifecycle and evidence; the case supplies only its
  // contract models, product phases, and the tables the check may read.
  return runSemanticCase(chatWorkflow({
    caseDir: CASE_DIR, scenarioModel: Scenario, resultModel: Result,
    subjectEntry: SUBJECT_ENTRY,
    evidenceTables: ["messages", "events", "decisions", "actions", "action_effects", "scheduled_events"],
    failurePrefixes: "`record` (required persistence absent), `state` (product state contradicts the intended Agent or session), `time` (trigger outside the declared future-time tolerance)",
    evidenceExplanation: "Inspect `data/db_evidence.json` for the original message and its event, then for the pending scheduled event linked through action_effects to a create_alarm action and its Agent decision.",
    prepareData,
    executeTest,
    checkEvidence: checkDbEvidence,
  }));
}

// Execute only when invoked directly, so the case can be imported by tooling.
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url))
  process.exitCode = await run();
