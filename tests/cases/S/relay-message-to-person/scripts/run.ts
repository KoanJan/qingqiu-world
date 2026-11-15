/** Run the message relay case against an isolated product server. */
import { fileURLToPath } from "node:url";
import path from "node:path";
import { chatWorkflow } from "../../chat_workflow.ts";
import { runSemanticCase } from "../../semantic_runner.ts";
import { AgentRoute, ChatRoute } from "../../product_routes.ts";
import { Result, Scenario } from "./case_contract.ts";
import { prepareData } from "./prepare_data.ts";
import { executeTest, checkDbEvidence } from "./execute_test.ts";

const CASE_DIR = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

// Product constants mirrored from server/internal/model and runtime action code.
const SUBJECT_ENTRY =
  `POST ${ChatRoute.CREATE}; POST ${AgentRoute.CREATE}; GET ${ChatRoute.MESSAGES}/:id; ` +
  "read-only SELECT on messages, events, decisions, actions, action_effects, sessions, " +
  "participant_sessions, persons";

/** Run this case's owned executor. */
export async function run() {
  // The shared runner owns lifecycle and evidence; the case supplies only its
  // contract models, product phases, and the tables the check may read.
  return runSemanticCase(chatWorkflow({
    caseDir: CASE_DIR, scenarioModel: Scenario, resultModel: Result,
    subjectEntry: SUBJECT_ENTRY,
    evidenceTables: ["persons", "sessions", "participant_sessions", "messages", "events", "decisions", "actions", "action_effects"],
    failurePrefixes: "`record` (required persistence or link absent), `state` (product state contradicts the intended recipient, session, or delivery target)",
    evidenceExplanation: "Inspect `data/db_evidence.json` for the original request and its event, the target session's Agent message, then its action_effects link to a chat action and its Agent decision.",
    extraResult: (scenario) => ({ target_person_name: scenario.target_person_name }),
    extraReport: (scenario) => `Target person: \`${scenario.target_person_name}\`\n\n`,
    prepareData,
    executeTest,
    checkEvidence: checkDbEvidence,
  }));
}

// Execute only when invoked directly, so the case can be imported by tooling.
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url))
  process.exitCode = await run();
