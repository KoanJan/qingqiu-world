/**
 * Shared lifecycle for scripted semantic (S-track) cases.
 *
 * A workflow owns its scenario/result contract, product setup, product action,
 * evidence checks, and track-specific report fields. This module owns the run
 * directory, provider preflight, isolated server lifecycle, capture freezing,
 * result assembly, and exit codes so every track records evidence the same way.
 */
import { copyFileSync, existsSync, readFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import {
  EXIT_CODE,
  IsolatedServer,
  RunRecord,
  caseCommand,
  errorMessage,
  providerEndpoint,
  sha256,
  utcNow,
} from "../tools.ts";
import type { ServerStatus, Status } from "../tools.ts";
import { RESULT_SCHEMA_VERSION } from "./semantic_contract.ts";
import type { BaseScenario, Environment } from "./semantic_contract.ts";
import { TIMEOUT_STOP_REASON } from "./capture_monitor.ts";
import type { StopReason } from "./capture_monitor.ts";
import { missingProviderEnv } from "./provider_env.ts";
import {
  configureAndRestartSemanticServer,
  prepareSemanticServer,
  SEMANTIC_SERVER_STOP_TIMEOUT_MS,
} from "./semantic_environment.ts";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../..");

/** Fields every S run records before the product starts. */
export type RunMetadata = {
  case_name: string;
  scenario_sha256: string;
  started_at_utc: string;
  environment: Environment;
  subject_entry: string;
  execution_command: string;
  run_dir: string;
  fixture_sha256?: string;
  llm_endpoint?: { host: string; sha256: string };
  llm_model_id?: string;
  embedding_endpoint?: { host: string; sha256: string };
  embedding_model_id?: string;
};

/** Mechanical facts gathered by the runner before semantic judgment. */
export type RunOutcome = {
  status: Status;
  detail: string;
  modelCalls: number | null;
  mechanicalFailures: string[];
  mechanicalChecksPerformed: boolean;
};

/** Everything a workflow needs to prepare or execute its product action. */
export type WorkflowContext<S extends BaseScenario> = {
  record: RunRecord;
  scenario: S;
  base: string;
  server: ServerStatus;
  llmId: number;
  /** Staged copy of the declared source, or null when the workflow has no fixture. */
  fixture: string | null;
};

/** Inputs for freezing the case's declared evidence tables before shutdown. */
export type EvidenceContext<S extends BaseScenario> = {
  record: RunRecord;
  scenario: S;
  /** Capture state owned and shaped by the workflow; the runner never inspects it. */
  session: unknown;
  dbPath: string;
  fixture: string | null;
  /** Status before capture; ERROR runs record evidence without mechanical checks. */
  status: Status;
};

/** Capture state returned by a workflow for the report and evidence phases. */
export type CapturedSession = {
  stopReason: StopReason;
  evidence: unknown;
};

/** Evidence written to `data/db_evidence.json` plus its mechanical checks. */
export type EvidenceCapture = {
  evidence: unknown;
  modelCalls: number | null;
  failures: string[];
  checksPerformed: boolean;
};

/** A parsed scenario plus the result validator bound to the same case models. */
export type LoadedDefinition<S extends BaseScenario> = {
  scenario: S;
  scenarioPath: string;
  validateResult(value: unknown, runDir: string, caseDir: string): void;
};

/** One S-track product workflow; the runner owns everything outside these hooks. */
export type SemanticWorkflow<S extends BaseScenario> = {
  caseDir: string;
  subjectEntry: string;
  failurePrefixes: string;
  evidenceDescription: string;
  evidenceExplanation: string;
  /** Parse the case definition before any product action. */
  loadDefinition(caseDir: string): Promise<LoadedDefinition<S>>;
  /** Absolute source path staged into the run, or undefined when unused. */
  fixturePath?(scenario: S, caseDir: string): string;
  prepareData(context: WorkflowContext<S>): Promise<void>;
  executeTest(context: WorkflowContext<S>): Promise<void>;
  /** Read the session this workflow captured, or null when it wrote none. */
  capturedSession(record: RunRecord): CapturedSession | null;
  captureEvidence(context: EvidenceContext<S>): Promise<EvidenceCapture>;
  /** Track-specific result fields, such as the declared input or source hash. */
  resultFields(scenario: S, metadata: RunMetadata): Record<string, unknown>;
  /** Track-specific limitations recorded for non-ERROR runs. */
  limitations(status: Status, modelCalls: number | null): string[];
  /** Track-specific report sections placed before the command. */
  reportSections(scenario: S, outcome: RunOutcome): string;
};

type RunInput = { fixture: string | null; metadata: RunMetadata };

/** The ERROR outcome shared by setup failures and the pre-product default. */
function errorOutcome(detail: string): RunOutcome {
  return {
    status: "ERROR",
    detail,
    modelCalls: null,
    mechanicalFailures: [],
    mechanicalChecksPerformed: false,
  };
}

/** The capture record shape shared by setup diagnostics and final runs. */
function captureFacts(outcome: RunOutcome): Record<string, unknown> {
  return {
    status: outcome.status,
    mechanical_failures: outcome.mechanicalFailures,
    mechanical_checks_performed: outcome.mechanicalChecksPerformed,
    observed_model_calls: outcome.modelCalls,
  };
}

/** Report heading shared by setup diagnostics and final runs. */
function reportHeader(
  record: RunRecord,
  title: string,
  outcome: RunOutcome,
  body: string,
): string {
  return `# ${record.caseName} ${title}\n\nStatus: ${outcome.status}\n\n${outcome.detail}\n\n${body}`;
}

/** Write the diagnostic capture for a run that never reached a product action. */
function writeErrorCapture(record: RunRecord, detail: string): void {
  record.writeDataJson("capture.json", captureFacts(errorOutcome(detail)));
}

/**
 * Record a setup failure that happened before any product action.
 *
 * A run whose definition loaded can satisfy the semantic result contract, so it
 * emits a diagnostic ERROR result. A run without scenario identity or checksum
 * cannot, and keeps the diagnosis in the report and capture record instead.
 */
function failSetup<S extends BaseScenario>(
  record: RunRecord,
  workflow: SemanticWorkflow<S>,
  definition: LoadedDefinition<S>,
  metadata: RunMetadata | null,
  command: string,
  detail: string,
): null {
  const failure = errorOutcome(detail);
  if (metadata !== null) record.writeDataJson("metadata.json", metadata);
  writeErrorCapture(record, detail);
  record.writeReport(
    reportHeader(
      record,
      "run",
      failure,
      `Command: \`${command}\`\n\nNo product action ran. Inspect ` +
        (metadata !== null ? "data/metadata.json" : "data/capture.json") +
        " for setup details.\n",
    ),
  );
  if (metadata !== null) writeResult(record, workflow, definition, metadata, failure);
  console.error(`${detail}\nEvidence directory: ${record.root}`);
  return null;
}

/** Build the metadata known before provider endpoints and fixtures are staged. */
function buildRunMetadata<S extends BaseScenario>(
  record: RunRecord,
  workflow: SemanticWorkflow<S>,
  definition: LoadedDefinition<S>,
  command: string,
  started: string,
): RunMetadata {
  return {
    case_name: record.caseName,
    scenario_sha256: sha256(readFileSync(definition.scenarioPath)),
    started_at_utc: started,
    environment: { os: os.type(), arch: os.arch(), node: process.version, isolated_data_root: true },
    subject_entry: workflow.subjectEntry,
    execution_command: command,
    run_dir: record.root,
  };
}

/** Validate the case definition before any product action. */
async function loadDefinition<S extends BaseScenario>(
  record: RunRecord,
  workflow: SemanticWorkflow<S>,
  command: string,
): Promise<LoadedDefinition<S> | null> {
  try {
    return await workflow.loadDefinition(record.caseDir);
  } catch (error) {
    const detail = `Scenario setup failed for ${record.caseName}: ${errorMessage(error)}`;
    record.writeReport(
      reportHeader(
        record,
        "setup",
        errorOutcome(detail),
        `Command: \`${command}\`\n\n` +
          "No product action ran; the scenario definition could not be validated.\n",
      ),
    );
    // A definition failure has no scenario identity or criteria, so it cannot
    // satisfy the semantic result contract. Keep the diagnostic in the report
    // and capture record instead of emitting a partial result.json.
    writeErrorCapture(record, detail);
    console.error(`${detail}\nEvidence directory: ${record.root}`);
    return null;
  }
}

/** Stage the declared source and record run metadata after provider preflight. */
function prepareRunInput<S extends BaseScenario>(
  record: RunRecord,
  workflow: SemanticWorkflow<S>,
  definition: LoadedDefinition<S>,
  command: string,
  started: string,
): RunInput | null {
  const { scenario } = definition;
  let metadata: RunMetadata;
  try {
    metadata = buildRunMetadata(record, workflow, definition, command, started);
  } catch (error) {
    return failSetup(
      record,
      workflow,
      definition,
      null,
      command,
      "Run metadata could not be recorded: " + errorMessage(error),
    );
  }
  // Provider preflight: reject an unrunnable configuration before staging input
  // or building the server, so the diagnostic points at the environment.
  const missing = missingProviderEnv();
  if (missing.length) {
    return failSetup(
      record,
      workflow,
      definition,
      metadata,
      command,
      "Missing required environment variables: " + missing.join(", "),
    );
  }
  let fixture: string | null = null;
  // Stage the case source inside the run and record the credential-free provider
  // identity. Any failure here still finalizes a diagnostic run.
  try {
    const sourceFixture = workflow.fixturePath?.(scenario, record.caseDir) ?? null;
    if (sourceFixture) {
      fixture = path.join(record.dataDir, path.basename(sourceFixture));
      copyFileSync(sourceFixture, fixture);
      metadata.fixture_sha256 = sha256(readFileSync(fixture));
    }
    metadata.llm_endpoint = providerEndpoint(process.env.QINGQIU_TEST_LLM_BASE_URL!);
    metadata.llm_model_id = process.env.QINGQIU_TEST_LLM_MODEL_ID!;
    metadata.embedding_endpoint = providerEndpoint(process.env.QINGQIU_TEST_EMBEDDING_BASE_URL!);
    metadata.embedding_model_id = process.env.QINGQIU_TEST_EMBEDDING_MODEL_ID!;
  } catch (error) {
    return failSetup(
      record,
      workflow,
      definition,
      metadata,
      command,
      "Run input could not be prepared: " + errorMessage(error),
    );
  }
  // Metadata is persisted only once every required input is known, so its
  // presence always means the run passed preflight.
  record.writeDataJson("metadata.json", metadata);
  console.log("Evidence directory: " + record.root);
  return { fixture, metadata };
}

/** Freeze declared evidence and apply only mechanical checks before shutdown. */
async function captureEvidence<S extends BaseScenario>(
  record: RunRecord,
  workflow: SemanticWorkflow<S>,
  scenario: S,
  fixture: string | null,
  outcome: RunOutcome,
): Promise<RunOutcome> {
  let capturedSession: CapturedSession | null;
  // A workflow owns the meaning of its session artifact; the runner only reads
  // it through this hook and treats a read failure as infrastructure.
  try {
    capturedSession = workflow.capturedSession(record);
  } catch (error) {
    const detail = outcome.detail + "; captured session could not be read: " + errorMessage(error);
    console.error("Captured session could not be read: " + errorMessage(error));
    return {
      ...outcome,
      status: statusAfterInfrastructureFailure(outcome.status),
      detail,
    };
  }
  if (capturedSession === null) return outcome;
  const next = { ...outcome };
  // Evidence is frozen before shutdown so a later process failure cannot hide
  // what the product had already persisted.
  try {
    const dbPath = path.join(record.dataDir, "product", "db", "database.db");
    const captured = await workflow.captureEvidence({
      record, scenario, session: capturedSession.evidence, dbPath, fixture, status: next.status,
    });
    record.writeDataJson("db_evidence.json", captured.evidence);
    next.modelCalls = captured.modelCalls;
    // Only a completed, non-ERROR capture runs mechanical checks; their
    // contradictions map to FAIL, never to a judgeable PASS.
    if (captured.checksPerformed && next.status !== "ERROR") {
      next.mechanicalFailures = captured.failures;
      next.mechanicalChecksPerformed = true;
      if (next.mechanicalFailures.length) {
        next.status = "FAIL";
        next.detail = "Mechanical provenance checks failed: " + next.mechanicalFailures.join("; ");
      }
    }
  } catch (error) {
    // Evidence could not be frozen, so a mechanical pass cannot be claimed.
    // Keep an ERROR diagnostic, otherwise report the run as inconclusive.
    if (next.status !== "ERROR") next.status = "INCONCLUSIVE";
    next.detail += "; read-only DB evidence unavailable or malformed: " + errorMessage(error);
  }
  return next;
}

/**
 * Keep an observed mechanical contradiction when only infrastructure fails.
 *
 * The result contract pins mechanical failures to FAIL, so escalating such a run
 * to ERROR would make the record unvalidatable.
 */
export function statusAfterInfrastructureFailure(status: Status): Status {
  return status === "FAIL" ? "FAIL" : "ERROR";
}

/** Save the live log before shutdown and stop only the server owned by this run. */
async function closeRun(
  record: RunRecord,
  server: IsolatedServer | null,
  outcome: RunOutcome,
): Promise<RunOutcome> {
  const next = { ...outcome };
  // Preserve the live log before stopping: shutdown output would otherwise
  // overwrite the context that explains the captured behavior.
  try {
    const log = path.join(record.dataDir, "server.log");
    if (existsSync(log)) copyFileSync(log, path.join(record.dataDir, "server_pre_stop.log"));
  } catch (error) {
    next.detail += "; pre-stop log capture failed: " + errorMessage(error);
    console.error("Pre-stop log capture failed: " + errorMessage(error));
  }
  if (server?.hasProcess) {
    // Only the server started for this run is stopped; an absent process has
    // no cleanup to perform.
    try {
      await server.stop(SEMANTIC_SERVER_STOP_TIMEOUT_MS);
    } catch (error) {
      next.status = statusAfterInfrastructureFailure(next.status);
      next.detail += "; cleanup: " + errorMessage(error);
      console.error("Server cleanup failed: " + errorMessage(error));
    }
  }
  return next;
}

/** Assemble and validate the machine result after the report exists. */
function writeResult<S extends BaseScenario>(
  record: RunRecord,
  workflow: SemanticWorkflow<S>,
  definition: LoadedDefinition<S>,
  metadata: RunMetadata,
  outcome: RunOutcome,
): void {
  const { status, detail, modelCalls, mechanicalFailures, mechanicalChecksPerformed } = outcome;
  // Inconclusive runs have not been judged yet; evaluated runs may still carry
  // track-specific limits for the judge to consider.
  const limitations =
    status === "INCONCLUSIVE" ? ["Independent semantic judgment has not run"] : [];
  if (status !== "ERROR") limitations.push(...workflow.limitations(status, modelCalls));
  const result = {
    schema_version: RESULT_SCHEMA_VERSION,
    scenario_sha256: metadata.scenario_sha256,
    track: "semantic",
    status,
    detail,
    execution_command: metadata.execution_command,
    started_at_utc: metadata.started_at_utc,
    ended_at_utc: utcNow(),
    subject_entry: workflow.subjectEntry,
    model: { llm: metadata.llm_model_id ?? null, embedding: metadata.embedding_model_id ?? null },
    environment: metadata.environment,
    limits: definition.scenario.limits,
    observed_model_calls: modelCalls,
    mechanical_failures: mechanicalFailures,
    mechanical_checks_performed: mechanicalChecksPerformed,
    judge: null,
    criteria: [...definition.scenario.required, ...definition.scenario.forbidden].map((item) => ({
      id: item.id,
      status: "INCONCLUSIVE",
      observed: "",
      evidence: [],
    })),
    limitations,
    deviations: [],
    ...workflow.resultFields(definition.scenario, metadata),
  };
  record.writeResult(result, (value) =>
    definition.validateResult(value, record.root, record.caseDir),
  );
}

/** Write the final capture, human report, and validated machine result. */
function finalizeRun<S extends BaseScenario>(
  record: RunRecord,
  workflow: SemanticWorkflow<S>,
  definition: LoadedDefinition<S>,
  metadata: RunMetadata,
  outcome: RunOutcome,
): void {
  const { mechanicalFailures, mechanicalChecksPerformed } = outcome;
  // The capture record must exist before the report that references it; the
  // report is human-facing while the result is validator-checked.
  record.writeDataJson("capture.json", captureFacts(outcome));
  record.writeReport(
    reportHeader(
      record,
      "run",
      outcome,
      workflow.reportSections(definition.scenario, outcome) +
        `Command: \`${metadata.execution_command}\`\n\n` +
        `Mechanical checks: ${mechanicalChecksPerformed ? "completed" : "not completed"}.\n\n` +
        `Mechanical failures: ${mechanicalFailures.length ? mechanicalFailures.join("; ") : "none recorded"}.\n\n` +
        `Failure prefixes: ${workflow.failurePrefixes}.\n\n` +
        "Semantic criteria: awaiting independent judgment.\n\n## Evidence\n\n" +
        "- `data/metadata.json`: run time, scenario checksum, provider models, and limits.\n" +
        "- `data/capture.json`: runner-captured mechanical facts, written before judgment.\n" +
        "- `data/session.json`: observed session responses, if available.\n" +
        "- `data/stop_monitor.json`: status polls and AI stop reviews, if capture reached monitoring.\n" +
        `- \`data/db_evidence.json\`: ${workflow.evidenceDescription}.\n` +
        "- `data/server_pre_stop.log`: runtime output before runner shutdown.\n" +
        "- `data/build.log` and `data/server.log`: build and owned-process output.\n\n" +
        workflow.evidenceExplanation +
        " Record the independent semantic verdict and evidence locations here.\n",
    ),
  );
  writeResult(record, workflow, definition, metadata, outcome);
}

/** Coordinate one S case; each phase owns its state and failure handling. */
export async function runSemanticCase<S extends BaseScenario>(
  workflow: SemanticWorkflow<S>,
): Promise<number> {
  const record = new RunRecord(workflow.caseDir);
  const started = utcNow();
  const command = caseCommand(ROOT, process.argv[1]);
  // Phase 1: validate the definition without touching the product. A failure
  // here has no scenario identity, so no result record can be emitted.
  const definition = await loadDefinition(record, workflow, command);
  if (definition === null) return EXIT_CODE.ERROR;
  // Phase 2: provider preflight, fixture staging, and run metadata.
  const input = prepareRunInput(record, workflow, definition, command, started);
  if (input === null) return EXIT_CODE.ERROR;

  let server: IsolatedServer | null = null;
  let outcome: RunOutcome = errorOutcome("Scenario did not complete setup");
  // Phase 3: own the isolated server, prepare product state, run the action,
  // and read back the capture state through the workflow hook.
  try {
    const instance = await prepareSemanticServer(
      ROOT,
      record,
      definition.scenario.limits.max_model_calls,
    );
    server = instance;
    await instance.start();
    const llmId = await configureAndRestartSemanticServer(instance, record);
    const context: WorkflowContext<S> = {
      record,
      scenario: definition.scenario,
      base: instance.base,
      server: instance,
      llmId,
      fixture: input.fixture,
    };
    await workflow.prepareData(context);
    await workflow.executeTest(context);
    const captured = workflow.capturedSession(record);
    if (captured === null) throw new Error("The case finished without a captured session");
    outcome = {
      ...outcome,
      status: "INCONCLUSIVE",
      detail:
        captured.stopReason === TIMEOUT_STOP_REASON
          ? "Capture reached the scenario timeout; semantic completion is unverified"
          : `Capture stopped (${captured.stopReason}); independent semantic judgment is required`,
    };
  } catch (error) {
    outcome.detail = errorMessage(error);
  } finally {
    // Phase 4: freeze evidence, stop the owned server, and always emit the
    // diagnostic report so a failed run never leaves an unexplained directory.
    outcome = await captureEvidence(record, workflow, definition.scenario, input.fixture, outcome);
    outcome = await closeRun(record, server, outcome);
    finalizeRun(record, workflow, definition, input.metadata, outcome);
  }
  console.log(`${record.caseName}: ${outcome.status}: ${outcome.detail}`);
  return EXIT_CODE[outcome.status];
}
