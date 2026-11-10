/**
 * Contracts for the current scripted RAG semantic cases.
 *
 * Case runners supply their case directory and, optionally, a case-specific
 * schema and validator. These contracts describe the scripted RAG result format;
 * other S workflows can define contracts suited to their own evidence.
 */
import { z } from "zod";
import { existsSync, readFileSync, readdirSync, realpathSync, statSync } from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { isDeepStrictEqual } from "node:util";
import { errorMessage, inside, sha256, validateReportStatus } from "../tools.ts";

function requireThat(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message);
}

const nonblank = z
  .string()
  .min(1)
  .refine((value) => !!value.trim(), "must be nonblank");
const status = z.enum(["PASS", "FAIL", "INCONCLUSIVE", "ERROR"]);
const positiveInt = z.number().int().positive();

/** One required or forbidden outcome declared by a scenario. */
const criterionSchema = z.strictObject({ id: nonblank, condition: nonblank });
export type Criterion = z.infer<typeof criterionSchema>;

/** Fields in the current scripted RAG scenario format. */
export const ScenarioSchema = z
  .strictObject({
    fixture: nonblank,
    question: nonblank,
    required: z.array(criterionSchema).min(1),
    forbidden: z.array(criterionSchema).min(1),
    limits: z
      .record(z.string(), positiveInt)
      .refine((value) => Object.keys(value).length > 0, "limits must be nonempty"),
  })
  .superRefine((value, ctx) => {
    const ids = [...value.required, ...value.forbidden].map((item) => item.id);
    if (new Set(ids).size !== ids.length) {
      ctx.addIssue({ code: "custom", message: "duplicate criterion ID" });
    }
  });
export type ScenarioData = z.infer<typeof ScenarioSchema>;

/** One artifact location supporting a criterion verdict. */
const evidenceReferenceSchema = z.strictObject({ artifact: nonblank, location: nonblank });
export type EvidenceReference = z.infer<typeof evidenceReferenceSchema>;

/**
 * The recorded verdict and evidence for one scenario criterion.
 *
 * Status is per criterion, not a second copy of the run status. A forbidden
 * criterion that did not occur is PASS with `observed` stating that the
 * prohibited behavior did not happen; the run can still be FAIL elsewhere.
 */
const criterionResultSchema = z.strictObject({
  id: nonblank,
  status,
  observed: z.string(),
  evidence: z.array(evidenceReferenceSchema),
});
export type CriterionResult = z.infer<typeof criterionResultSchema>;

/** Platform identity of the process that executed the case. */
const environmentSchema = z.strictObject({
  os: nonblank,
  arch: nonblank,
  node: nonblank,
  isolated_data_root: z.boolean(),
});
export type Environment = z.infer<typeof environmentSchema>;

/** Fields in the current scripted RAG result format. */
export const ResultSchema = z.strictObject({
  schema_version: z.literal(1),
  run_id: nonblank,
  case_name: nonblank,
  scenario_sha256: z.string().regex(/^[0-9a-f]{64}$/),
  track: z.literal("semantic"),
  status,
  detail: z.string(),
  execution_command: nonblank,
  started_at_utc: nonblank,
  ended_at_utc: nonblank,
  fixture_sha256: z.string().regex(/^[0-9a-f]{64}$/),
  question: nonblank,
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
export type SemanticResult = z.infer<typeof ResultSchema>;

/** Validate a scenario's case context after schema parsing. */
export class ScenarioBase {
  static readonly schema: z.ZodType<ScenarioData> = ScenarioSchema;
  readonly value: ScenarioData;
  constructor(value: ScenarioData) {
    this.value = value;
  }

  /** Check directory, filename, and fixture rules for one scenario. */
  validateContext(scenarioPath: string, caseDir: string): string {
    const scenario = this.value;
    const resolvedCase = realpathSync(caseDir);
    const resolvedPath = realpathSync(scenarioPath);
    requireThat(
      path.dirname(resolvedPath) === resolvedCase &&
        path.basename(path.dirname(resolvedCase)) === "S" &&
        /^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$/.test(path.basename(resolvedCase)),
      resolvedPath + ": invalid semantic case directory",
    );
    requireThat(path.basename(resolvedPath) === "scenario.json", resolvedPath + ": expected scenario.json");
    const fixture = path.resolve(resolvedCase, scenario.fixture);
    requireThat(
      inside(fixture, resolvedCase) &&
        existsSync(fixture) &&
        inside(realpathSync(fixture), resolvedCase) &&
        statSync(fixture).isFile(),
      resolvedPath + ": fixture missing or outside its case directory",
    );
    return fixture;
  }
}

/** Validate one scenario with its base and optional case-specific model. */
export function validateScenario(
  value: unknown,
  scenarioPath: string,
  caseDir: string,
  model: typeof ScenarioBase = ScenarioBase,
): ScenarioBase {
  const instance = new model(model.schema.parse(value));
  instance.validateContext(scenarioPath, caseDir);
  return instance;
}

/** Return the scenario and its path for one case. */
export function loadScenario(
  caseDir: string,
  model: typeof ScenarioBase = ScenarioBase,
): [ScenarioData, string] {
  const scenarioPath = path.join(caseDir, "scenario.json");
  const value = JSON.parse(readFileSync(scenarioPath, "utf8"));
  return [validateScenario(value, scenarioPath, caseDir, model).value, scenarioPath];
}

/** Validate a result's run context after schema parsing. */
export class ResultBase {
  static readonly schema: z.ZodType<SemanticResult> = ResultSchema;
  readonly value: SemanticResult;
  constructor(value: SemanticResult) {
    this.value = value;
  }

  /** Check scenario identity, evidence boundaries, and PASS conditions. */
  validateContext(runDir: string, scenario: ScenarioData, caseDir: string): void {
    const result = this.value;
    const resolvedRun = realpathSync(runDir);
    const resolvedCase = realpathSync(caseDir);
    requireThat(
      result.case_name === path.basename(resolvedCase) &&
        result.question === scenario.question &&
        isDeepStrictEqual(result.limits, scenario.limits),
      "result changed the scenario input or limits",
    );
    requireThat(result.run_id === path.basename(resolvedRun), "result run ID mismatch");
    requireThat(
      result.artifacts.includes("data/metadata.json") && result.artifacts.includes("report.md"),
      "metadata and report must be listed as artifacts",
    );
    const dataRoot = path.join(resolvedRun, "data");
    requireThat(
      existsSync(dataRoot) &&
        statSync(dataRoot).isDirectory() &&
        readdirSync(resolvedRun).every((name) =>
          ["result.json", "report.md", "data"].includes(name),
        ),
      "run root must contain only result.json, report.md, and data/",
    );
    for (const artifact of result.artifacts) {
      requireThat(artifact && !path.isAbsolute(artifact), "artifact path must be relative");
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
    const fixtureArtifact = "data/" + path.basename(scenario.fixture);
    if (result.artifacts.includes(fixtureArtifact)) {
      requireThat(
        sha256(readFileSync(path.join(resolvedRun, fixtureArtifact))) === result.fixture_sha256,
        "captured fixture hash mismatch",
      );
    }
    const expected = new Set([...scenario.required, ...scenario.forbidden].map((item) => item.id));
    requireThat(
      result.criteria.length === expected.size &&
        result.criteria.every((item) => expected.has(item.id)) &&
        new Set(result.criteria.map((item) => item.id)).size === expected.size,
      "result criteria do not match scenario",
    );
    for (const item of result.criteria) {
      for (const reference of item.evidence) {
        requireThat(
          result.artifacts.includes(reference.artifact),
          "invalid evidence reference in " + item.id,
        );
      }
    }
    requireThat(
      !result.mechanical_failures.length || result.status === "FAIL",
      "recorded mechanical failures require FAIL status",
    );
    // The runner writes capture facts before judgment; a judge may append but
    // must not rewrite them, so the result is cross-checked against that record.
    requireThat(
      result.artifacts.includes("data/capture.json"),
      "result must list the runner capture record",
    );
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
    if (result.status === "PASS") {
      const judge = result.judge;
      requireThat(
        validJudge(judge) && judge.judgement === "supported",
        "PASS requires an explained, supported semantic judgment",
      );
      requireThat(
        result.criteria.every((item) => item.status === "PASS" && item.evidence.length),
        "PASS requires evidence for every criterion",
      );
      requireThat(!result.mechanical_failures.length, "PASS cannot have mechanical failures");
      requireThat(result.mechanical_checks_performed, "PASS requires mechanical checks");
      requireThat(
        ["data/session.json", "data/db_evidence.json", "data/stop_monitor.json",
          "data/server_pre_stop.log", fixtureArtifact].every((item) =>
          result.artifacts.includes(item),
        ),
        "PASS requires pre-stop session, monitor, log, database evidence, and exact source",
      );
      const stopMonitor = JSON.parse(readFileSync(path.join(dataRoot, "stop_monitor.json"), "utf8"));
      requireThat(stopMonitor.reason && stopMonitor.reason !== "timeout",
        "PASS requires a completed capture before the scenario timeout");
      requireThat(
        typeof result.observed_model_calls === "number" &&
          Number.isInteger(result.observed_model_calls) &&
          result.observed_model_calls > 0,
        "PASS requires an observed work-model interaction count",
      );
      if (result.artifacts.includes("data/calibration.json")) {
        const calibration = JSON.parse(
          readFileSync(path.join(dataRoot, "calibration.json"), "utf8"),
        ) as Array<Record<string, unknown>>;
        requireThat(
          Array.isArray(calibration) &&
            calibration.length &&
            calibration.every(
              (item) => item && typeof item.claim === "string" && item.claim.trim(),
            ),
          "each calibration entry needs the reference claim text",
        );
        const labels = new Set(
          calibration
            .filter((item) => item.expected === item.observed)
            .map((item) => item.expected),
        );
        requireThat(
          ["supported", "contradicted", "insufficient_evidence"].every((label) =>
            labels.has(label),
          ),
          "PASS with calibration requires correct reference judgments in all three categories",
        );
      }
    }
  }

  /** Require a completed judgment for every evaluated, non-ERROR run. */
  validateFinal(): void {
    const result = this.value;
    if (result.status === "ERROR") return;
    requireThat(
      validJudge(result.judge) &&
        ["supported", "contradicted", "insufficient_evidence"].includes(result.judge.judgement),
      "final result requires a completed semantic judgment",
    );
    requireThat(
      result.criteria.every((item) => item.observed.trim()),
      "final result requires an observation for every criterion",
    );
  }
}

/** Validate capture facts, and require judgment unless checking a capture. */
export function validateResult(
  value: unknown,
  runDir: string,
  scenario: ScenarioData,
  caseDir: string,
  final = true,
  model: typeof ResultBase = ResultBase,
): ResultBase {
  const instance = new model(model.schema.parse(value));
  instance.validateContext(runDir, scenario, caseDir);
  validateReportStatus(path.join(realpathSync(runDir), "report.md"), instance.value.status);
  if (final) instance.validateFinal();
  return instance;
}

/** Load optional case-specific models; return null when none are declared. */
export async function loadCaseContract(caseDir: string): Promise<{
  Scenario?: typeof ScenarioBase;
  Result?: typeof ResultBase;
} | null> {
  const contractPath = path.join(caseDir, "scripts", "case_contract.ts");
  if (!existsSync(contractPath)) return null;
  requireThat(statSync(contractPath).isFile(), "case contract must be a file");
  return await import(pathToFileURL(contractPath).href);
}

type Judge = { kind: string; model: string; rationale: string; judgement: string };
function validJudge(judge: Record<string, unknown> | null): judge is Judge {
  return (
    !!judge &&
    ["independent_agent", "same_session_agent"].includes(String(judge.kind)) &&
    typeof judge.model === "string" &&
    !!judge.model.trim() &&
    typeof judge.rationale === "string" &&
    !!judge.rationale.trim()
  );
}

/** Run the scenario validator CLI for one case directory. */
export function scenarioCli(
  caseDir: string,
  model: typeof ScenarioBase = ScenarioBase,
  args = process.argv.slice(2),
) {
  try {
    requireThat(!args.length, "usage: validate_scenario.ts");
    const [, scenarioPath] = loadScenario(caseDir, model);
    console.log("VALID: " + path.basename(caseDir) + " (" + scenarioPath + ")");
    return 0;
  } catch (error) {
    console.error("INVALID: " + errorMessage(error));
    return 2;
  }
}

/** Run the result validator CLI for one case directory. */
export function resultCli(
  caseDir: string,
  scenarioModel: typeof ScenarioBase = ScenarioBase,
  resultModel: typeof ResultBase = ResultBase,
  args = process.argv.slice(2),
) {
  try {
    const capture = args[0] === "--capture";
    const remaining = capture ? args.slice(1) : args;
    requireThat(remaining.length === 1, "usage: validate_result.ts [--capture] run_dir");
    const runDir = path.resolve(remaining[0]);
    const result = resultModel.schema.parse(
      JSON.parse(readFileSync(path.join(runDir, "result.json"), "utf8")),
    );
    const [scenario, scenarioPath] = loadScenario(caseDir, scenarioModel);
    requireThat(
      sha256(readFileSync(scenarioPath)) === result.scenario_sha256,
      "scenario file changed since the run",
    );
    requireThat(!capture || result.status !== "PASS", "a capture result cannot be PASS");
    validateResult(result, runDir, scenario, caseDir, !capture, resultModel);
    const label = capture
      ? "CAPTURE VALID"
      : result.status === "ERROR"
        ? "DIAGNOSTIC VALID"
        : "FINAL VALID";
    console.log(label + ": " + result.case_name + " " + result.status + " (" + runDir + ")");
    return !capture && result.status === "PASS" ? 0 : 1;
  } catch (error) {
    console.error("INVALID: " + errorMessage(error));
    return 2;
  }
}
