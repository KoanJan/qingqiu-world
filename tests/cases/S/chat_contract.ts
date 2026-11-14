/**
 * Scenario and result contract for chat-driven S cases, independent of RAG.
 *
 * Chat cases share the criterion declaration, artifact layout, capture, judge,
 * and CLI rules from `semantic_contract.ts`; this module only adds the request
 * input and the chat evidence required for PASS.
 */
import { z } from "zod";
import {
  BaseResultSchema,
  BaseScenarioSchema,
  nonblank,
  requireThat,
  resultCli,
  resultInstance,
  scenarioCli,
  loadScenarioInstance,
  validateScenarioLocation,
  validateSemanticFinal,
  validateSemanticResultContext,
  type ResultInstance,
  type ResultModel,
  type ScenarioModel,
} from "./semantic_contract.ts";

/** Fields shared by chat scenarios; each case extends this schema. */
export const ChatScenarioSchema = BaseScenarioSchema.safeExtend({
  request: nonblank,
});
export type ChatScenario = z.infer<typeof ChatScenarioSchema>;

/** Fields shared by chat result records; each case extends this schema. */
export const ChatResultSchema = BaseResultSchema.safeExtend({
  request: nonblank,
});
export type ChatResult = z.infer<typeof ChatResultSchema>;

/** Check the directory and filename of a chat scenario. */
export function validateChatScenarioContext(scenarioPath: string, caseDir: string): void {
  validateScenarioLocation(scenarioPath, caseDir);
}

export type ChatScenarioModel<T extends ChatScenario> = ScenarioModel<T>;
export type ChatResultModel<T extends ChatResult, S extends ChatScenario> = ResultModel<S, T>;

/** Return the scenario after case-specific schema and context checks. */
export function loadChatScenario<T extends ChatScenario>(
  caseDir: string,
  model: ChatScenarioModel<T>,
): [T, string] {
  const [instance, scenarioPath] = loadScenarioInstance(caseDir, model);
  return [instance.value, scenarioPath];
}

/** Validate chat input identity plus the shared invariant evidence layout. */
export function validateChatResultContext(
  result: ChatResult,
  scenario: ChatScenario,
  runDir: string,
  caseDir: string,
): void {
  requireThat(result.request === scenario.request, "result changed the scenario input or limits");
  validateSemanticResultContext(result, runDir, scenario, caseDir, (value) => {
    requireThat(
      [
        "data/session.json",
        "data/db_evidence.json",
        "data/stop_monitor.json",
        "data/server_pre_stop.log",
      ].every((item) => value.artifacts.includes(item)),
      "PASS requires pre-stop session, monitor, log, and database evidence",
    );
  });
}

/** Require completed semantic judgment for any final, non-ERROR result. */
export function validateChatFinal(result: ChatResult): void {
  validateSemanticFinal(result);
}

/** Parse and validate a chat result; capture checks defer the final judgment. */
export function validateChatResult<S extends ChatScenario, T extends ChatResult>(
  value: unknown,
  runDir: string,
  scenario: S,
  caseDir: string,
  model: ChatResultModel<T, S>,
  final = true,
): ResultInstance<S, T> {
  return resultInstance(value, runDir, scenario, caseDir, final, model);
}

/** CLI for a case-specific scenario validator. */
export function chatScenarioCli<T extends ChatScenario>(
  caseDir: string,
  model: ChatScenarioModel<T>,
  args = process.argv.slice(2),
): number {
  return scenarioCli(caseDir, model, args);
}

/** CLI for a case-specific result validator. */
export function chatResultCli<S extends ChatScenario, T extends ChatResult>(
  caseDir: string,
  scenarioModel: ChatScenarioModel<S>,
  resultModel: ChatResultModel<T, S>,
  args = process.argv.slice(2),
): number {
  return resultCli(caseDir, scenarioModel, resultModel, args);
}
