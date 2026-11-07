/** Shared scenario contract for cases using the scripted RAG workflow. */
import { ScenarioBase } from "./base_contracts.ts";

export const CHAT_PATH = "/api/chat/new";
export const MESSAGES_PATH = "/api/messages";
export const ACTIVITIES_PATH = "/api/sessions";
export const SUBJECT_ENTRY =
  "POST /api/chat/new; GET /api/messages/:id; GET /api/sessions/:id/activities";
export const LIMIT_KEYS = ["index_timeout_seconds", "answer_timeout_seconds", "max_model_calls"];

/** Require precisely the limits consumed by the shared RAG runner. */
export class Scenario extends ScenarioBase {
  override validateContext(scenarioPath: string, caseDir: string): string {
    const fixture = super.validateContext(scenarioPath, caseDir);
    if (
      Object.keys(this.value.limits).length !== LIMIT_KEYS.length ||
      !LIMIT_KEYS.every((key) => key in this.value.limits)
    ) {
      throw new Error("RAG limits must be exactly " + LIMIT_KEYS.join(", "));
    }
    return fixture;
  }
}
