/** Scenario and result contract for the chat-to-person relay workflow. */
import { z } from "zod";
import {
  ChatResultSchema, ChatScenarioSchema, validateChatFinal,
  validateChatResultContext, validateChatScenarioContext,
} from "../../chat_contract.ts";

const limits = ["relay_timeout_seconds", "max_model_calls"];

/** A fixed request, named recipient, criteria, and execution limits. */
export const ScenarioSchema = ChatScenarioSchema.safeExtend({
  target_person_name: z.string().min(1).refine((value) => !!value.trim(), "must be nonblank"),
}).superRefine((value, ctx) => {
  const ids = [...value.required, ...value.forbidden].map((item) => item.id);
  if (new Set(ids).size !== ids.length) ctx.addIssue({ code: "custom", message: "duplicate criterion ID" });
  if (Object.keys(value.limits).sort().join() !== limits.sort().join())
    ctx.addIssue({ code: "custom", message: "scenario limits must be exactly " + limits.join(", ") });
  if (!value.request.includes(value.target_person_name))
    ctx.addIssue({ code: "custom", message: "scenario target person must appear in the request" });
});
export type RelayScenario = z.infer<typeof ScenarioSchema>;

/** Check this scenario's location after structural validation. */
export class Scenario {
  static readonly schema = ScenarioSchema;
  readonly value: RelayScenario;
  constructor(value: RelayScenario) { this.value = value; }
  validateContext(scenarioPath: string, caseDir: string) {
    validateChatScenarioContext(this.value, scenarioPath, caseDir);
  }
}

/** A capture record and semantic judgment for one relay run. */
export const ResultSchema = ChatResultSchema.safeExtend({ target_person_name: z.string().min(1) });
export type RelayResult = z.infer<typeof ResultSchema>;

export class Result {
  static readonly schema = ResultSchema;
  readonly value: RelayResult;
  constructor(value: RelayResult) { this.value = value; }
  validateContext(runDir: string, scenario: RelayScenario, caseDir: string) {
    if (this.value.target_person_name !== scenario.target_person_name)
      throw new Error("result changed the scenario input or limits");
    validateChatResultContext(this.value, scenario, runDir, caseDir);
  }
  validateFinal() { validateChatFinal(this.value); }
}
