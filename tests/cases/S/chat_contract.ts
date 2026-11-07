/** Shared evidence contract for chat-driven S cases, independent of RAG. */
import { existsSync, readFileSync, readdirSync, realpathSync, statSync } from "node:fs";
import path from "node:path";
import { isDeepStrictEqual } from "node:util";
import { z } from "zod";
import { inside, sha256, validateReportStatus } from "../tools.ts";

const nonblank = z.string().min(1).refine((value) => !!value.trim(), "must be nonblank");
const status = z.enum(["PASS", "FAIL", "INCONCLUSIVE", "ERROR"]);
const criterion = z.strictObject({ id: nonblank, condition: nonblank });
const criterionResult = z.strictObject({
  id: nonblank,
  status,
  observed: z.string(),
  evidence: z.array(z.strictObject({ artifact: nonblank, location: nonblank })),
});
const revision = z.strictObject({
  head: nonblank,
  status: z.string(),
  tracked_diff_sha256: z.string(),
});
const environment = z.strictObject({
  os: nonblank,
  arch: nonblank,
  node: nonblank,
  isolated_data_root: z.boolean(),
});
const positiveInt = z.number().int().positive();

/** Fields shared by versioned chat scenarios; each case extends this schema. */
export const ChatScenarioSchema = z.strictObject({
  version: positiveInt,
  lifecycle: z.enum(["proposed", "reviewed", "active", "deprecated"]),
  request: nonblank,
  required: z.array(criterion).min(1),
  forbidden: z.array(criterion).min(1),
  limits: z.record(z.string(), positiveInt),
});
export type ChatScenario = z.infer<typeof ChatScenarioSchema>;

/** Fields shared by chat result records; each case extends this schema. */
export const ChatResultSchema = z.strictObject({
  schema_version: z.literal(1),
  run_id: nonblank,
  case_name: nonblank,
  scenario_version: positiveInt,
  scenario_sha256: z.string().regex(/^[0-9a-f]{64}$/),
  track: z.literal("semantic"),
  status,
  detail: z.string(),
  execution_command: nonblank,
  started_at_utc: nonblank,
  ended_at_utc: nonblank,
  tested_revision: revision,
  finished_revision: revision,
  request: nonblank,
  subject_entry: nonblank,
  model: z.record(z.string(), z.unknown()),
  environment,
  limits: z.record(z.string(), positiveInt),
  observed_model_calls: z.number().int().nullable(),
  mechanical_failures: z.array(z.string()),
  mechanical_checks_performed: z.boolean(),
  judge: z.record(z.string(), z.unknown()).nullable(),
  criteria: z.array(criterionResult),
  artifacts: z.array(z.string()),
  limitations: z.array(z.string()),
  deviations: z.array(z.string()).default([]),
});
export type ChatResult = z.infer<typeof ChatResultSchema>;

function requireThat(value: unknown, message: string): asserts value {
  if (!value) throw new Error(message);
}

/** Check the directory and versioned filename of a chat scenario. */
export function validateChatScenarioContext(scenario: ChatScenario, scenarioPath: string, caseDir: string) {
  const resolvedCase = realpathSync(caseDir);
  const resolvedPath = realpathSync(scenarioPath);
  requireThat(
    path.dirname(resolvedPath) === resolvedCase && path.basename(path.dirname(resolvedCase)) === "S" &&
      /^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$/.test(path.basename(resolvedCase)),
    resolvedPath + ": invalid semantic case directory",
  );
  requireThat(path.basename(resolvedPath) === `scenario-v${scenario.version}.json`,
    resolvedPath + ": scenario filename must match its version");
}

export type ChatScenarioModel<T extends ChatScenario> = {
  schema: z.ZodType<T>;
  new (value: T): { value: T; validateContext(scenarioPath: string, caseDir: string): void };
};
export type ChatResultModel<T extends ChatResult, S extends ChatScenario> = {
  schema: z.ZodType<T>;
  new (value: T): { value: T; validateContext(runDir: string, scenario: S, caseDir: string): void; validateFinal(): void };
};

/** Return the one active scenario after case-specific schema and context checks. */
export function loadChatScenario<T extends ChatScenario>(caseDir: string, model: ChatScenarioModel<T>): [T, string] {
  const matches: Array<[T, string]> = [];
  for (const filename of readdirSync(caseDir).filter((name) => /^scenario-v.*\.json$/.test(name))) {
    const scenarioPath = path.join(caseDir, filename);
    const scenario = new model(model.schema.parse(JSON.parse(readFileSync(scenarioPath, "utf8"))));
    scenario.validateContext(scenarioPath, caseDir);
    if (scenario.value.lifecycle === "active") matches.push([scenario.value, scenarioPath]);
  }
  requireThat(matches.length === 1,
    `expected one active scenario in ${path.basename(caseDir)}, found ${matches.length}`);
  return matches[0];
}

type Judge = { kind: string; model: string; rationale: string; judgement: string };
function validJudge(value: Record<string, unknown> | null): value is Judge {
  return !!value && ["independent_agent", "same_session_agent"].includes(String(value.kind)) &&
    typeof value.model === "string" && !!value.model.trim() &&
    typeof value.rationale === "string" && !!value.rationale.trim();
}

/** Validate the invariant evidence layout and captured facts for chat cases. */
export function validateChatResultContext(result: ChatResult, scenario: ChatScenario, runDir: string, caseDir: string) {
  const resolvedRun = realpathSync(runDir);
  const resolvedCase = realpathSync(caseDir);
  requireThat(result.case_name === path.basename(resolvedCase) && result.scenario_version === scenario.version &&
    result.request === scenario.request && isDeepStrictEqual(result.limits, scenario.limits),
    "result changed the scenario input or limits");
  requireThat(result.run_id === path.basename(resolvedRun), "result run ID mismatch");
  requireThat(result.artifacts.includes("data/metadata.json") && result.artifacts.includes("report.md"),
    "metadata and report must be listed as artifacts");
  const dataRoot = path.join(resolvedRun, "data");
  requireThat(existsSync(dataRoot) && statSync(dataRoot).isDirectory() &&
    readdirSync(resolvedRun).every((name) => ["result.json", "report.md", "data"].includes(name)),
    "run root must contain only result.json, report.md, and data/");
  for (const artifact of result.artifacts) {
    requireThat(artifact && !path.isAbsolute(artifact), "artifact path must be relative");
    const target = path.resolve(resolvedRun, artifact);
    requireThat(inside(target, resolvedRun) && existsSync(target) && statSync(target).isFile() &&
      inside(realpathSync(target), resolvedRun), "artifact missing or outside this run: " + artifact);
    requireThat(artifact === "report.md" || inside(target, dataRoot),
      "artifact outside data directory: " + artifact);
  }
  const expected = new Set([...scenario.required, ...scenario.forbidden].map((item) => item.id));
  requireThat(result.criteria.length === expected.size &&
    result.criteria.every((item) => expected.has(item.id)) &&
    new Set(result.criteria.map((item) => item.id)).size === expected.size,
    "result criteria do not match scenario");
  for (const item of result.criteria) for (const reference of item.evidence)
    requireThat(result.artifacts.includes(reference.artifact), "invalid evidence reference in " + item.id);
  requireThat(!result.mechanical_failures.length || result.status === "FAIL",
    "recorded mechanical failures require FAIL status");
  requireThat(result.artifacts.includes("data/capture.json"), "result must list the runner capture record");
  const capture = JSON.parse(readFileSync(path.join(dataRoot, "capture.json"), "utf8"));
  for (const field of ["status", "mechanical_failures", "mechanical_checks_performed", "observed_model_calls"])
    requireThat(field in capture, "capture record is incomplete");
  for (const field of ["mechanical_failures", "mechanical_checks_performed", "observed_model_calls"] as const)
    requireThat(JSON.stringify(result[field]) === JSON.stringify(capture[field]),
      "judge changed captured field: " + field);
  requireThat(!["FAIL", "ERROR"].includes(capture.status) || result.status === capture.status,
    "judge cannot change the capture status");
  if (result.status === "PASS") {
    requireThat(validJudge(result.judge) && result.judge.judgement === "supported",
      "PASS requires an explained, supported semantic judgment");
    requireThat(result.criteria.every((item) => item.status === "PASS" && item.evidence.length),
      "PASS requires evidence for every criterion");
    requireThat(!result.mechanical_failures.length, "PASS cannot have mechanical failures");
    requireThat(result.mechanical_checks_performed, "PASS requires mechanical checks");
    requireThat(["data/session.json", "data/db_evidence.json"].every((item) => result.artifacts.includes(item)),
      "PASS requires session and database evidence");
    if (result.artifacts.includes("data/calibration.json")) {
      const calibration = JSON.parse(readFileSync(path.join(dataRoot, "calibration.json"), "utf8"));
      requireThat(Array.isArray(calibration) && calibration.length &&
        calibration.every((item: Record<string, unknown>) => typeof item?.claim === "string" && !!item.claim.trim()),
        "each calibration entry needs the reference claim text");
      const labels = new Set(calibration.filter((item: Record<string, unknown>) => item.expected === item.observed)
        .map((item: Record<string, unknown>) => item.expected));
      requireThat(["supported", "contradicted", "insufficient_evidence"].every((label) => labels.has(label)),
        "PASS with calibration requires correct reference judgments in all three categories");
    }
  }
}

/** Require completed semantic judgment for any final, non-ERROR result. */
export function validateChatFinal(result: ChatResult) {
  if (result.status === "ERROR") return;
  requireThat(validJudge(result.judge) &&
    ["supported", "contradicted", "insufficient_evidence"].includes(result.judge.judgement),
    "final result requires a completed semantic judgment");
  requireThat(result.criteria.every((item) => item.observed.trim()),
    "final result requires an observation for every criterion");
}

/** Parse and validate a chat result; capture checks defer the final judgment. */
export function validateChatResult<S extends ChatScenario, T extends ChatResult>(
  value: unknown, runDir: string, scenario: S, caseDir: string, model: ChatResultModel<T, S>, final = true,
) {
  const result = new model(model.schema.parse(value));
  result.validateContext(runDir, scenario, caseDir);
  validateReportStatus(path.join(realpathSync(runDir), "report.md"), result.value.status);
  if (final) result.validateFinal();
  return result;
}

/** CLI for a case-specific scenario validator. */
export function chatScenarioCli<T extends ChatScenario>(caseDir: string, model: ChatScenarioModel<T>, args = process.argv.slice(2)) {
  try {
    let scenario: T, scenarioPath: string;
    if (!args.length) [scenario, scenarioPath] = loadChatScenario(caseDir, model);
    else if (args.length === 2 && args[0] === "--file") {
      scenarioPath = path.resolve(args[1]);
      requireThat(path.dirname(scenarioPath) === path.resolve(caseDir), "scenario file must be inside this case directory");
      const instance = new model(model.schema.parse(JSON.parse(readFileSync(scenarioPath, "utf8"))));
      instance.validateContext(scenarioPath, caseDir);
      scenario = instance.value;
    } else throw new Error("usage: validate_scenario.ts [--file path]");
    console.log(`VALID: ${path.basename(caseDir)} v${scenario.version} ${scenario.lifecycle} (${scenarioPath})`);
    return 0;
  } catch (error) {
    console.error("INVALID: " + (error instanceof Error ? error.message : String(error)));
    return 2;
  }
}

/** CLI for a case-specific result validator. */
export function chatResultCli<S extends ChatScenario, T extends ChatResult>(
  caseDir: string, scenarioModel: ChatScenarioModel<S>, resultModel: ChatResultModel<T, S>, args = process.argv.slice(2),
) {
  try {
    const capture = args[0] === "--capture";
    const remaining = capture ? args.slice(1) : args;
    requireThat(remaining.length === 1, "usage: validate_result.ts [--capture] run_dir");
    const runDir = path.resolve(remaining[0]);
    const result = resultModel.schema.parse(JSON.parse(readFileSync(path.join(runDir, "result.json"), "utf8")));
    const [scenario, scenarioPath] = loadChatScenario(caseDir, scenarioModel);
    requireThat(sha256(readFileSync(scenarioPath)) === result.scenario_sha256, "scenario file changed since the run");
    requireThat(!capture || result.status !== "PASS", "a capture result cannot be PASS");
    validateChatResult(result, runDir, scenario, caseDir, resultModel, !capture);
    console.log(`${capture ? "CAPTURE VALID" : result.status === "ERROR" ? "DIAGNOSTIC VALID" : "FINAL VALID"}: ${result.case_name} ${result.status} (${runDir})`);
    return !capture && result.status === "PASS" ? 0 : 1;
  } catch (error) {
    console.error("INVALID: " + (error instanceof Error ? error.message : String(error)));
    return 2;
  }
}
