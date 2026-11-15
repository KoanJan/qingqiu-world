/**
 * Shared protocol for semantic (S-track) case definitions and result records.
 *
 * This module owns the rules that do not vary by product workflow: scenario
 * location, criterion identity, artifact containment, capture immutability,
 * judge validity, and the common PASS gate. Track workflows supply their own
 * input fields and track-specific PASS conditions instead of reimplementing
 * these invariants.
 */
import { z } from "zod";
import { existsSync, readFileSync, readdirSync, realpathSync, statSync } from "node:fs";
import path from "node:path";
import { isDeepStrictEqual } from "node:util";
import { errorMessage, inside, sha256, validateReportStatus, STATUSES } from "../tools.ts";

/** Version of the machine result schema; the runner and validators share it. */
export const RESULT_SCHEMA_VERSION = 1;

/**
 * Assert a rule that every caller of this module relies on. The message becomes
 * part of the diagnostic output produced by the validation CLIs.
 */
export function requireThat(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message);
}

/** A required textual value that cannot be empty or whitespace-only. */
export const nonblank = z
  .string()
  .min(1)
  .refine((value) => value.trim().length > 0, "must be nonblank");

/** Semantic run status shared by every track. */
export const status = z.enum(STATUSES);

const positiveInt = z.number().int().positive();

/** A criterion declared before the run; IDs identify it in the result. */
export const criterionSchema = z.strictObject({
  id: nonblank,
  condition: nonblank,
});
export type Criterion = z.infer<typeof criterionSchema>;

/** A pointer from a criterion judgment to an auditable run artifact. */
export const evidenceReferenceSchema = z.strictObject({
  artifact: nonblank,
  location: nonblank,
});
export type EvidenceReference = z.infer<typeof evidenceReferenceSchema>;

/** One criterion's outcome recorded by the judge. */
export const criterionResultSchema = z.strictObject({
  id: nonblank,
  status,
  observed: z.string(),
  evidence: z.array(evidenceReferenceSchema),
});
export type CriterionResult = z.infer<typeof criterionResultSchema>;

/** Environment fields every semantic result must record. */
export const environmentSchema = z.strictObject({
  os: nonblank,
  arch: nonblank,
  node: nonblank,
  isolated_data_root: z.boolean(),
});
export type Environment = z.infer<typeof environmentSchema>;

/** Fields shared by every semantic scenario definition. */
export const BaseScenarioSchema = z.strictObject({
  required: z.array(criterionSchema).min(1),
  forbidden: z.array(criterionSchema).min(1),
  limits: z
    .record(z.string(), positiveInt)
    .refine((value) => Object.keys(value).length > 0, "limits must be nonempty"),
});
export type BaseScenario = z.infer<typeof BaseScenarioSchema>;

/** Fields shared by every semantic result record. */
export const BaseResultSchema = z.strictObject({
  schema_version: z.literal(RESULT_SCHEMA_VERSION),
  run_id: nonblank,
  case_name: nonblank,
  scenario_sha256: z.string().regex(/^[0-9a-f]{64}$/),
  track: z.literal("semantic"),
  status,
  detail: z.string(),
  execution_command: nonblank,
  started_at_utc: nonblank,
  ended_at_utc: nonblank,
  subject_entry: nonblank,
  model: z.record(z.string(), z.unknown()),
  environment: environmentSchema,
  limits: z
    .record(z.string(), positiveInt)
    .refine((value) => Object.keys(value).length > 0, "limits must be nonempty"),
  observed_model_calls: z.number().int().nullable(),
  mechanical_failures: z.array(z.string()),
  mechanical_checks_performed: z.boolean(),
  judge: z.record(z.string(), z.unknown()).nullable(),
  criteria: z.array(criterionResultSchema),
  artifacts: z.array(z.string()),
  limitations: z.array(z.string()),
  deviations: z.array(z.string()).default([]),
});
export type BaseResult = z.infer<typeof BaseResultSchema>;

/** A semantic judgment that carries the evidence needed to audit it. */
export type Judge = { kind: string; model: string; rationale: string; judgement: string };

/** Narrow an arbitrary recorded judge object to the supported shape. */
export function validJudge(judge: Record<string, unknown> | null): judge is Judge {
  return (
    judge !== null &&
    ["independent_agent", "same_session_agent"].includes(String(judge.kind)) &&
    typeof judge.model === "string" &&
    judge.model.trim().length > 0 &&
    typeof judge.rationale === "string" &&
    judge.rationale.trim().length > 0 &&
    typeof judge.judgement === "string" &&
    ["supported", "contradicted", "insufficient_evidence"].includes(judge.judgement as string)
  );
}

/**
 * A scenario must declare distinct criterion IDs; otherwise evidence cannot be
 * attributed to exactly one criterion. This is a definition invariant, so every
 * loader applies it rather than each track repeating the rule.
 */
export function requireUniqueCriteria(scenario: Pick<BaseScenario, "required" | "forbidden">): void {
  const ids = [...scenario.required, ...scenario.forbidden].map((item) => item.id);
  requireThat(new Set(ids).size === ids.length, "duplicate criterion ID");
}

/** Validate that scenario.json lives at the root of a real S case directory. */
export function validateScenarioLocation(scenarioPath: string, caseDir: string): void {
  const resolvedCase = realpathSync(caseDir);
  const resolvedPath = realpathSync(scenarioPath);
  requireThat(
    path.dirname(resolvedPath) === resolvedCase &&
      path.basename(path.dirname(resolvedCase)) === "S" &&
      /^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$/.test(path.basename(resolvedCase)),
    resolvedPath + ": invalid semantic case directory",
  );
  requireThat(path.basename(resolvedPath) === "scenario.json", resolvedPath + ": expected scenario.json");
}

/** A scenario model is the class that parses and validates one track's schema. */
export type ScenarioModel<S extends BaseScenario> = {
  schema: z.ZodType<S>;
  new (value: S): { value: S; validateContext(scenarioPath: string, caseDir: string): unknown };
};

/** A scenario instance exposes its parsed value and context validation. */
export type ScenarioInstance<S extends BaseScenario> = {
  value: S;
  validateContext(scenarioPath: string, caseDir: string): unknown;
};

/** A result model validates a parsed result against its run and scenario. */
export type ResultModel<S extends BaseScenario, R extends BaseResult> = {
  schema: z.ZodType<R>;
  new (value: R): {
    value: R;
    validateContext(runDir: string, scenario: S, caseDir: string): void;
    validateFinal(): void;
  };
};

/** A result instance exposes its parsed value and final validation. */
export type ResultInstance<S extends BaseScenario, R extends BaseResult> = {
  value: R;
  validateContext(runDir: string, scenario: S, caseDir: string): void;
  validateFinal(): void;
};

/** Parse, validate, and return a scenario model instance. */
export function scenarioInstance<S extends BaseScenario>(
  value: unknown,
  scenarioPath: string,
  caseDir: string,
  model: ScenarioModel<S>,
): ScenarioInstance<S> {
  const instance = new model(model.schema.parse(value));
  instance.validateContext(scenarioPath, caseDir);
  requireUniqueCriteria(instance.value);
  return instance;
}

/** Read scenario.json from a case directory and return its validated instance. */
export function loadScenarioInstance<S extends BaseScenario>(
  caseDir: string,
  model: ScenarioModel<S>,
): [ScenarioInstance<S>, string] {
  const scenarioPath = path.join(caseDir, "scenario.json");
  const value = JSON.parse(readFileSync(scenarioPath, "utf8"));
  return [scenarioInstance(value, scenarioPath, caseDir, model), scenarioPath];
}

/**
 * Validate the facts shared by every semantic result: layout, artifacts,
 * criterion identity, capture immutability, and the common PASS gate.
 * Input-field equality remains the track model's responsibility.
 */
export function validateSemanticResultContext(
  result: BaseResult,
  runDir: string,
  scenario: BaseScenario,
  caseDir: string,
  passExtra?: (result: BaseResult, dataRoot: string) => void,
): void {
  const resolvedRun = realpathSync(runDir);
  const resolvedCase = realpathSync(caseDir);
  // Identity: the result must describe this exact case, run, and input.
  requireThat(
    result.case_name === path.basename(resolvedCase) && isDeepStrictEqual(result.limits, scenario.limits),
    "result changed the scenario input or limits",
  );
  requireThat(result.run_id === path.basename(resolvedRun), "result run ID mismatch");
  // Layout: the run root may only hold the result, report, and evidence data.
  requireThat(
    result.artifacts.includes("data/metadata.json") && result.artifacts.includes("report.md"),
    "metadata and report must be listed as artifacts",
  );
  const dataRoot = path.join(resolvedRun, "data");
  requireThat(
    existsSync(dataRoot) &&
      statSync(dataRoot).isDirectory() &&
      readdirSync(resolvedRun).every((name) => ["result.json", "report.md", "data"].includes(name)),
    "run root must contain only result.json, report.md, and data/",
  );
  for (const artifact of result.artifacts) {
    // Every artifact must be a relative path that resolves inside this run and,
    // except for the report, inside the evidence directory.
    requireThat(Boolean(artifact) && !path.isAbsolute(artifact), "artifact path must be relative");
    const target = path.resolve(resolvedRun, artifact);
    requireThat(
      inside(target, resolvedRun) &&
        existsSync(target) &&
        statSync(target).isFile() &&
        inside(realpathSync(target), resolvedRun),
      "artifact missing or outside this run: " + artifact,
    );
    requireThat(
      artifact === "report.md" || inside(target, dataRoot),
      "artifact outside data directory: " + artifact,
    );
  }
  // Criteria: exactly one result per declared criterion, with no extras.
  const expected = new Set([...scenario.required, ...scenario.forbidden].map((item) => item.id));
  requireThat(
    result.criteria.length === expected.size &&
      result.criteria.every((item) => expected.has(item.id)) &&
      new Set(result.criteria.map((item) => item.id)).size === expected.size,
    "result criteria do not match scenario",
  );
  for (const item of result.criteria) {
    // A judgment may only cite artifacts the run actually recorded.
    for (const reference of item.evidence) {
      requireThat(result.artifacts.includes(reference.artifact), "invalid evidence reference in " + item.id);
    }
  }
  // Capture immutability: mechanical facts are written before judgment and
  // cannot be edited by the judge; mechanical failures pin the status to FAIL.
  requireThat(
    !result.mechanical_failures.length || result.status === "FAIL",
    "recorded mechanical failures require FAIL status",
  );
  requireThat(result.artifacts.includes("data/capture.json"), "result must list the runner capture record");
  const capture = JSON.parse(readFileSync(path.join(dataRoot, "capture.json"), "utf8"));
  for (const field of [
    "status",
    "mechanical_failures",
    "mechanical_checks_performed",
    "observed_model_calls",
  ]) {
    requireThat(field in capture, "capture record is incomplete");
  }
  for (const field of [
    "mechanical_failures",
    "mechanical_checks_performed",
    "observed_model_calls",
  ] as const) {
    requireThat(
      JSON.stringify(result[field]) === JSON.stringify(capture[field]),
      "judge changed captured field: " + field,
    );
  }
  requireThat(
    !["FAIL", "ERROR"].includes(capture.status) || result.status === capture.status,
    "judge cannot change the capture status",
  );
  if (result.status === "PASS") validatePassGate(result, dataRoot, passExtra);
}

/** Common PASS gates; track-specific evidence requirements run afterwards. */
function validatePassGate(
  result: BaseResult,
  dataRoot: string,
  passExtra?: (result: BaseResult, dataRoot: string) => void,
): void {
  // PASS needs a supported, explained judgment and evidence for every criterion.
  requireThat(
    validJudge(result.judge) && result.judge.judgement === "supported",
    "PASS requires an explained, supported semantic judgment",
  );
  requireThat(
    result.criteria.every((item) => item.status === "PASS" && item.evidence.length > 0),
    "PASS requires evidence for every criterion",
  );
  requireThat(!result.mechanical_failures.length, "PASS cannot have mechanical failures");
  requireThat(result.mechanical_checks_performed, "PASS requires mechanical checks");
  requireThat(result.artifacts.includes("data/stop_monitor.json"), "PASS requires the capture monitor record");
  // A timeout means the exchange never completed, so it cannot support PASS.
  const stopMonitor = JSON.parse(readFileSync(path.join(dataRoot, "stop_monitor.json"), "utf8"));
  requireThat(
    stopMonitor.reason && stopMonitor.reason !== "timeout",
    "PASS requires a completed capture before the scenario timeout",
  );
  validateCalibration(result, dataRoot);
  passExtra?.(result, dataRoot);
}

/** Optional calibration must contain one correct reference per judgment class. */
function validateCalibration(result: BaseResult, dataRoot: string): void {
  if (!result.artifacts.includes("data/calibration.json")) return;
  const calibration = JSON.parse(readFileSync(path.join(dataRoot, "calibration.json"), "utf8")) as Array<
    Record<string, unknown>
  >;
  requireThat(
    Array.isArray(calibration) &&
      calibration.length > 0 &&
      calibration.every((item) => item && typeof item.claim === "string" && item.claim.trim().length > 0),
    "each calibration entry needs the reference claim text",
  );
  const labels = new Set(
    calibration.filter((item) => item.expected === item.observed).map((item) => item.expected),
  );
  requireThat(
    ["supported", "contradicted", "insufficient_evidence"].every((label) => labels.has(label as string)),
    "PASS with calibration requires correct reference judgments in all three categories",
  );
}

/** Final validation requires a completed judgment and an observation per criterion. */
export function validateSemanticFinal(result: BaseResult): void {
  if (result.status === "ERROR") return;
  requireThat(
    validJudge(result.judge) && ["supported", "contradicted", "insufficient_evidence"].includes(result.judge.judgement),
    "final result requires a completed semantic judgment",
  );
  requireThat(
    result.criteria.every((item) => item.observed.trim().length > 0),
    "final result requires an observation for every criterion",
  );
}

/** Parse a result, validate it, and confirm its report status matches. */
export function resultInstance<S extends BaseScenario, R extends BaseResult>(
  value: unknown,
  runDir: string,
  scenario: S,
  caseDir: string,
  final: boolean,
  model: ResultModel<S, R>,
): ResultInstance<S, R> {
  const instance = new model(model.schema.parse(value));
  instance.validateContext(runDir, scenario, caseDir);
  validateReportStatus(path.join(realpathSync(runDir), "report.md"), instance.value.status);
  if (final) instance.validateFinal();
  return instance;
}

/** Validate one case's scenario.json and print the stable CLI result. */
export function scenarioCli<S extends BaseScenario>(
  caseDir: string,
  model: ScenarioModel<S>,
  args: string[] = process.argv.slice(2),
): number {
  try {
    requireThat(!args.length, "usage: validate_scenario.ts");
    const [, scenarioPath] = loadScenarioInstance(caseDir, model);
    console.log("VALID: " + path.basename(caseDir) + " (" + scenarioPath + ")");
    return 0;
  } catch (error) {
    console.error("INVALID: " + errorMessage(error));
    return 2;
  }
}

/**
 * Validate a run directory through its scenario and result models. Capture-only
 * runs cannot be PASS; definition or tooling failures remain diagnostic ERROR.
 */
export function resultCli<S extends BaseScenario, R extends BaseResult>(
  caseDir: string,
  scenarioModel: ScenarioModel<S>,
  resultModel: ResultModel<S, R>,
  args: string[] = process.argv.slice(2),
): number {
  try {
    const capture = args[0] === "--capture";
    const remaining = capture ? args.slice(1) : args;
    requireThat(remaining.length === 1, "usage: validate_result.ts [--capture] run_dir");
    const runDir = path.resolve(remaining[0]);
    const raw = resultModel.schema.parse(JSON.parse(readFileSync(path.join(runDir, "result.json"), "utf8")));
    const [scenarioInstanceValue, scenarioPath] = loadScenarioInstance(caseDir, scenarioModel);
    requireThat(sha256(readFileSync(scenarioPath)) === raw.scenario_sha256, "scenario file changed since the run");
    requireThat(!capture || raw.status !== "PASS", "a capture result cannot be PASS");
    resultInstance(raw, runDir, scenarioInstanceValue.value, caseDir, !capture, resultModel);
    const label = capture ? "CAPTURE VALID" : raw.status === "ERROR" ? "DIAGNOSTIC VALID" : "FINAL VALID";
    console.log(label + ": " + raw.case_name + " " + raw.status + " (" + runDir + ")");
    return !capture && raw.status === "PASS" ? 0 : 1;
  } catch (error) {
    console.error("INVALID: " + errorMessage(error));
    return 2;
  }
}
