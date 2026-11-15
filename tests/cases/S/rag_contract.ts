/**
 * Scenario and result contract for cases using the scripted RAG workflow.
 *
 * RAG-specific input fields, source hashing, and PASS evidence live here. The
 * shared artifact, capture, judge, and CLI invariants come from
 * `semantic_contract.ts`.
 */
import { z } from "zod";
import { existsSync, readFileSync, realpathSync, statSync } from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { inside, sha256 } from "../tools.ts";
import {
  BaseResultSchema,
  BaseScenarioSchema,
  nonblank,
  requireThat,
  resultInstance,
  scenarioInstance,
  loadScenarioInstance,
  validateScenarioLocation,
  validateSemanticFinal,
  validateSemanticResultContext,
} from "./semantic_contract.ts";

import { ChatRoute } from "./product_routes.ts";

export const SUBJECT_ENTRY =
  `POST ${ChatRoute.CREATE}; GET ${ChatRoute.MESSAGES}/:id; GET ${ChatRoute.SESSIONS}/:id/activities`;
// The versioned answer timeout bounds the whole post-request capture, including
// provisional replies, state polls, and stop reviews.
export const LIMIT_KEYS = ["index_timeout_seconds", "answer_timeout_seconds", "max_model_calls"];

/** RAG scenario fields on top of the shared criterion declaration. */
export const ScenarioSchema = BaseScenarioSchema.safeExtend({
  fixture: nonblank,
  question: nonblank,
});
export type ScenarioData = z.infer<typeof ScenarioSchema>;

/** RAG result fields on top of the shared semantic result record. */
export const ResultSchema = BaseResultSchema.safeExtend({
  fixture_sha256: z.string().regex(/^[0-9a-f]{64}$/),
  question: nonblank,
});
export type SemanticResult = z.infer<typeof ResultSchema>;

/** Validate RAG scenario context: case location plus an in-case fixture. */
export class ScenarioBase {
  static readonly schema: z.ZodType<ScenarioData> = ScenarioSchema;
  readonly value: ScenarioData;
  constructor(value: ScenarioData) {
    this.value = value;
  }

  /** Check directory, filename, and fixture rules for one RAG scenario. */
  validateContext(scenarioPath: string, caseDir: string): string {
    validateScenarioLocation(scenarioPath, caseDir);
    // The declared fixture must resolve inside this case and be a real file;
    // a source outside the case would not be reproducible from the repo.
    const resolvedCase = realpathSync(caseDir);
    const resolvedPath = realpathSync(scenarioPath);
    const fixture = path.resolve(resolvedCase, this.value.fixture);
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

/** Require precisely the limits consumed by the shared RAG runner. */
export class Scenario extends ScenarioBase {
  override validateContext(scenarioPath: string, caseDir: string): string {
    const fixture = super.validateContext(scenarioPath, caseDir);
    // The shared runner consumes exactly these limits; extra or missing keys
    // would silently change the capture budget.
    if (
      Object.keys(this.value.limits).length !== LIMIT_KEYS.length ||
      !LIMIT_KEYS.every((key) => key in this.value.limits)
    ) {
      throw new Error("RAG limits must be exactly " + LIMIT_KEYS.join(", "));
    }
    return fixture;
  }
}

/** Validate one RAG scenario with its base and optional case-specific model. */
export function validateScenario(
  value: unknown,
  scenarioPath: string,
  caseDir: string,
  model: typeof ScenarioBase = ScenarioBase,
): ScenarioBase {
  return scenarioInstance(value, scenarioPath, caseDir, model) as ScenarioBase;
}

/** Return the RAG scenario and its path for one case. */
export function loadScenario(
  caseDir: string,
  model: typeof ScenarioBase = ScenarioBase,
): [ScenarioData, string] {
  const [instance, scenarioPath] = loadScenarioInstance(caseDir, model);
  return [instance.value, scenarioPath];
}

/** Validate RAG result identity, source hash, and tracked work calls. */
export class ResultBase {
  static readonly schema: z.ZodType<SemanticResult> = ResultSchema;
  readonly value: SemanticResult;
  constructor(value: SemanticResult) {
    this.value = value;
  }

  /** Check the RAG input, fixture bytes, and shared evidence rules. */
  validateContext(runDir: string, scenario: ScenarioData, caseDir: string): void {
    const result = this.value;
    requireThat(result.question === scenario.question, "result changed the scenario input or limits");
    // When the staged source is listed, its bytes must still hash to the value
    // recorded before the product ran.
    const resolvedRun = realpathSync(runDir);
    const fixtureArtifact = "data/" + path.basename(scenario.fixture);
    if (result.artifacts.includes(fixtureArtifact)) {
      requireThat(
        sha256(readFileSync(path.join(resolvedRun, fixtureArtifact))) === result.fixture_sha256,
        "captured fixture hash mismatch",
      );
    }
    validateSemanticResultContext(result, runDir, scenario, caseDir, (value) => {
      // RAG PASS additionally requires the capture artifacts that prove what
      // was retrieved and that the model actually performed work.
      requireThat(
        [
          "data/session.json",
          "data/db_evidence.json",
          "data/stop_monitor.json",
          "data/server_pre_stop.log",
          fixtureArtifact,
        ].every((item) => value.artifacts.includes(item)),
        "PASS requires pre-stop session, monitor, log, database evidence, and exact source",
      );
      requireThat(
        typeof value.observed_model_calls === "number" &&
          Number.isInteger(value.observed_model_calls) &&
          value.observed_model_calls > 0,
        "PASS requires an observed work-model interaction count",
      );
    });
  }

  /** Require a completed judgment for every evaluated, non-ERROR run. */
  validateFinal(): void {
    validateSemanticFinal(this.value);
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
  return resultInstance(value, runDir, scenario, caseDir, final, model) as ResultBase;
}

/** Load optional RAG case-specific models; return null when none are declared. */
export async function loadCaseContract(caseDir: string): Promise<{
  Scenario?: typeof ScenarioBase;
  Result?: typeof ResultBase;
} | null> {
  const contractPath = path.join(caseDir, "scripts", "case_contract.ts");
  if (!existsSync(contractPath)) return null;
  requireThat(statSync(contractPath).isFile(), "case contract must be a file");
  return await import(pathToFileURL(contractPath).href);
}
