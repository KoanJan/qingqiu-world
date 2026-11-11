/** Scenario and result contract for the chat-to-reminder workflow. */
import { z } from "zod";
import {
  ChatResultSchema, ChatScenarioSchema, validateChatFinal,
  validateChatResultContext, validateChatScenarioContext,
} from "../../chat_contract.ts";

const limits = ["reminder_timeout_seconds", "max_model_calls"];

/** A fixed request, expected delay, criteria, and execution limits. */
export const ScenarioSchema = ChatScenarioSchema.safeExtend({
  expected_delay_seconds: z.number().int().positive(),
  delay_tolerance_seconds: z.number().int().positive(),
}).superRefine((value, ctx) => {
  const ids = [...value.required, ...value.forbidden].map((item) => item.id);
  if (new Set(ids).size !== ids.length) ctx.addIssue({ code: "custom", message: "duplicate criterion ID" });
  if (Object.keys(value.limits).sort().join() !== limits.sort().join())
    ctx.addIssue({ code: "custom", message: "scenario limits must be exactly " + limits.join(", ") });
  if (value.delay_tolerance_seconds >= value.expected_delay_seconds)
    ctx.addIssue({ code: "custom", message: "delay tolerance must be smaller than the expected delay" });
});
export type ScheduleScenario = z.infer<typeof ScenarioSchema>;

/** Check this scenario's location after structural validation. */
export class Scenario {
  static readonly schema = ScenarioSchema;
  readonly value: ScheduleScenario;
  constructor(value: ScheduleScenario) { this.value = value; }
  validateContext(scenarioPath: string, caseDir: string) {
    validateChatScenarioContext(scenarioPath, caseDir);
  }
}

/** A capture record and semantic judgment for one reminder run. */
export const ResultSchema = ChatResultSchema;
export type ScheduleResult = z.infer<typeof ResultSchema>;

export class Result {
  static readonly schema = ResultSchema;
  readonly value: ScheduleResult;
  constructor(value: ScheduleResult) { this.value = value; }
  validateContext(runDir: string, scenario: ScheduleScenario, caseDir: string) {
    validateChatResultContext(this.value, scenario, runDir, caseDir);
  }
  validateFinal() { validateChatFinal(this.value); }
}
