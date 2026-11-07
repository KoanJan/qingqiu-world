/**
 * Regression checks for scripted RAG report and artifact boundaries.
 *
 * Both RAG cases use the same contract; other S workflows own contract tests
 * for their own definitions and evidence formats.
 */
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { test } from "node:test";
import type { TestContext } from "node:test";
import { sha256 } from "../tools.ts";
import type { Status } from "../tools.ts";
import { loadScenario, validateResult, validateScenario } from "./base_contracts.ts";
import type { ScenarioData as Scenario, SemanticResult } from "./base_contracts.ts";
import { Scenario as RagScenario, SUBJECT_ENTRY } from "./rag_contract.ts";

for (const caseName of ["rag-source-order", "rag-insufficient-evidence"]) {
const CASE_DIR = path.join(path.dirname(fileURLToPath(import.meta.url)), caseName);

/** Build the contract test fixture for one semantic case directory. */
function setup(t: TestContext) {
  const temp = mkdtempSync(path.join(os.tmpdir(), "qingqiu-contract-"));
  t.after(() => rmSync(temp, { recursive: true, force: true }));
  const runDir = mkdtempSync(path.join(temp, "run-"));
  const dataDir = path.join(runDir, "data");
  mkdirSync(dataDir);
  const [scenario, scenarioPath] = loadScenario(CASE_DIR);
  const fixtureName = path.basename(scenario.fixture);
  const fixtureBytes = readFileSync(path.join(CASE_DIR, scenario.fixture));
  const artifacts = [
    "data/metadata.json",
    "data/capture.json",
    "data/calibration.json",
    "report.md",
    "data/session.json",
    "data/db_evidence.json",
    "data/" + fixtureName,
  ];
  for (const name of artifacts) {
    const file = path.join(runDir, name);
    mkdirSync(path.dirname(file), { recursive: true });
    writeFileSync(file, "evidence");
  }
  copyFileSync(path.join(CASE_DIR, scenario.fixture), path.join(dataDir, fixtureName));

  /** Write the runner capture record that judgment may not rewrite. */
  const writeCapture = (overrides: Record<string, unknown> = {}) => {
    writeFileSync(
      path.join(dataDir, "capture.json"),
      JSON.stringify({
        status: "INCONCLUSIVE",
        mechanical_failures: [],
        mechanical_checks_performed: true,
        observed_model_calls: 2,
        ...overrides,
      }),
    );
  };
  const writeCalibration = (labels = ["supported", "contradicted", "insufficient_evidence"]) => {
    writeFileSync(
      path.join(dataDir, "calibration.json"),
      JSON.stringify(
        labels.map((label) => ({
          claim: "reference claim " + label,
          expected: label,
          observed: label,
        })),
      ),
    );
  };
  const writeReport = (status: Status) => {
    writeFileSync(path.join(runDir, "report.md"), "# Result\n\nStatus: " + status + "\n");
  };
  writeCapture();
  writeCalibration();
  writeReport("PASS");
  const result: SemanticResult = {
    schema_version: 1,
    run_id: path.basename(runDir),
    case_name: path.basename(CASE_DIR),
    scenario_version: scenario.version,
    scenario_sha256: sha256(readFileSync(scenarioPath)),
    track: "semantic",
    status: "PASS",
    detail: "All criteria supported",
    execution_command: `npm --prefix tests run case -- cases/S/${caseName}/scripts/run.ts`,
    started_at_utc: "2026-11-05T00:00:00Z",
    ended_at_utc: "2026-11-05T00:01:00Z",
    question: scenario.question,
    subject_entry: SUBJECT_ENTRY,
    model: { llm: "subject-model", embedding: "embedding-model" },
    environment: { os: "TestOS", arch: "arm64", node: "v26", isolated_data_root: true },
    limits: scenario.limits,
    fixture_sha256: sha256(fixtureBytes),
    tested_revision: { head: "test-sha", status: "", tracked_diff_sha256: "test" },
    finished_revision: { head: "test-sha", status: "", tracked_diff_sha256: "test" },
    mechanical_failures: [],
    observed_model_calls: 2,
    mechanical_checks_performed: true,
    judge: {
      kind: "independent_agent",
      model: "judge-model",
      judgement: "supported",
      rationale: "Original admission order supports the claim.",
    },
    criteria: [...scenario.required, ...scenario.forbidden].map((item) => ({
      id: item.id,
      status: "PASS",
      observed: "checked",
      evidence: [{ artifact: "data/db_evidence.json", location: "active_chunks[0]" }],
    })),
    artifacts,
    limitations: [],
    deviations: [],
  };
  const validate = (value: unknown, final = true) =>
    validateResult(value, runDir, scenario, CASE_DIR, final);
  const writeResult = (value: SemanticResult) =>
    writeFileSync(path.join(runDir, "result.json"), JSON.stringify(value));
  return {
    runDir,
    dataDir,
    scenario,
    scenarioPath,
    result,
    validate,
    writeCapture,
    writeCalibration,
    writeReport,
    writeResult,
    fixtureName,
  };
}

test(caseName + ": complete, artifact-backed PASS result is accepted", (t) => {
  const fixture = setup(t);
  fixture.validate(fixture.result);
});

test(caseName + ": final CLI rejects stale report status", (t) => {
  const fixture = setup(t);
  fixture.writeReport("INCONCLUSIVE");
  fixture.writeResult(fixture.result);
  const completed = spawnSync(
    process.execPath,
    ["--import", "tsx", path.join(CASE_DIR, "scripts", "validate_result.ts"), fixture.runDir],
    { encoding: "utf8" },
  );
  assert.equal(completed.status, 2);
  assert.match(completed.stderr, /report.md status must match result.json status/);
});

test(caseName + ": delivery fields are outside the result schema", (t) => {
  const fixture = setup(t);
  assert.throws(
    () => fixture.validate({ ...fixture.result, acceptance_owner: "someone" }),
    /unrecognized|Unrecognized/,
  );
});

test(caseName + ": PASS does not require identical or clean worktrees", (t) => {
  const fixture = setup(t);
  const result = structuredClone(fixture.result);
  result.tested_revision.status = " M tested.ts";
  result.finished_revision.head = "later-sha";
  fixture.validate(result);
  fixture.writeResult(result);
  const completed = spawnSync(
    process.execPath,
    ["--import", "tsx", path.join(CASE_DIR, "scripts", "validate_result.ts"), fixture.runDir],
    { encoding: "utf8" },
  );
  assert.equal(completed.status, 0, completed.stderr);
  assert.match(completed.stdout, /FINAL VALID/);
});

test(caseName + ": same-session judgment can pass", (t) => {
  const fixture = setup(t);
  const result = structuredClone(fixture.result);
  result.judge = { ...result.judge, kind: "same_session_agent" };
  result.limitations.push("Judgment was formed in the capture session.");
  fixture.validate(result);
});

test(caseName + ": capture scaffold is valid but is not a final result", (t) => {
  const fixture = setup(t);
  const result = structuredClone(fixture.result);
  result.status = "INCONCLUSIVE";
  result.judge = null;
  fixture.writeReport("INCONCLUSIVE");
  result.criteria = result.criteria.map((item) => ({
    ...item,
    status: "INCONCLUSIVE",
    observed: "",
    evidence: [],
  }));
  fixture.validate(result, false);
  assert.throws(() => fixture.validate(result), /completed semantic judgment/);
});

test(caseName + ": mechanical FAIL scaffold still needs final judgment", (t) => {
  const fixture = setup(t);
  const result = structuredClone(fixture.result);
  result.status = "FAIL";
  result.mechanical_failures = ["record: no KB retrieval trace"];
  result.judge = null;
  fixture.writeReport("FAIL");
  fixture.writeCapture({ status: "FAIL", mechanical_failures: result.mechanical_failures });
  fixture.validate(result, false);
  assert.throws(() => fixture.validate(result), /completed semantic judgment/);
});

test(caseName + ": result CLI distinguishes capture from judged FAIL", (t) => {
  const fixture = setup(t);
  const result = structuredClone(fixture.result);
  result.status = "FAIL";
  result.mechanical_failures = ["record: no KB retrieval trace"];
  result.judge = null;
  result.criteria = result.criteria.map((item) => ({
    ...item,
    status: "INCONCLUSIVE",
    observed: "",
    evidence: [],
  }));
  fixture.writeReport("FAIL");
  fixture.writeCapture({ status: "FAIL", mechanical_failures: result.mechanical_failures });
  fixture.writeResult(result);
  const command = path.join(CASE_DIR, "scripts", "validate_result.ts");
  const capture = spawnSync(process.execPath, ["--import", "tsx", command, "--capture", fixture.runDir], {
    encoding: "utf8",
  });
  assert.equal(capture.status, 1, capture.stderr);
  assert.match(capture.stdout, /CAPTURE VALID/);
  const unfinished = spawnSync(process.execPath, ["--import", "tsx", command, fixture.runDir], { encoding: "utf8" });
  assert.equal(unfinished.status, 2);
  assert.match(unfinished.stderr, /completed semantic judgment/);

  result.judge = {
    kind: "independent_agent",
    model: "judge-model",
    judgement: "contradicted",
    rationale: "The retrieval trace is absent.",
  };
  result.criteria = result.criteria.map((item) => ({
    ...item,
    status: "PASS",
    observed: "Checked the session and database evidence.",
    evidence: [{ artifact: "data/db_evidence.json", location: "kb_usage_traces" }],
  }));
  result.criteria[0]!.status = "FAIL";
  fixture.writeResult(result);
  const final = spawnSync(process.execPath, ["--import", "tsx", command, fixture.runDir], { encoding: "utf8" });
  assert.equal(final.status, 1, final.stderr);
  assert.match(final.stdout, /FINAL VALID/);
});

test(caseName + ": mechanical failures cannot be softened", (t) => {
  const fixture = setup(t);
  const result = structuredClone(fixture.result);
  result.mechanical_failures = ["record: no KB retrieval trace"];
  result.status = "INCONCLUSIVE";
  assert.throws(() => fixture.validate(result), /mechanical failures require FAIL status/);
  result.status = "FAIL";
  fixture.writeCapture({ status: "FAIL", mechanical_failures: result.mechanical_failures });
  fixture.writeReport("FAIL");
  fixture.validate(result);
});

test(caseName + ": judge cannot rewrite the captured mechanical facts", (t) => {
  const fixture = setup(t);
  fixture.writeCapture({ status: "FAIL", mechanical_failures: ["record: no KB retrieval trace"] });
  assert.throws(() => fixture.validate(fixture.result), /judge changed captured field/);
});

test(caseName + ": PASS needs evidence for each criterion and a supported judgment", (t) => {
  const fixture = setup(t);
  const result = structuredClone(fixture.result);
  result.criteria[0]!.evidence = [];
  assert.throws(() => fixture.validate(result), /evidence for every criterion/);
  result.criteria[0]!.evidence = [
    { artifact: "data/db_evidence.json", location: "active_chunks[0]" },
  ];
  result.judge = { ...result.judge, judgement: "insufficient_evidence" };
  assert.throws(() => fixture.validate(result), /supported semantic judgment/);
});

test(caseName + ": artifacts cannot escape the run or data directory", (t) => {
  const fixture = setup(t);
  const outside = structuredClone(fixture.result);
  outside.artifacts.push("../outside.txt");
  assert.throws(() => fixture.validate(outside), /outside this run/);
  const scattered = structuredClone(fixture.result);
  writeFileSync(path.join(fixture.runDir, "unexpected.log"), "stray");
  assert.throws(() => fixture.validate(scattered), /run root must contain only/);
  rmSync(path.join(fixture.runDir, "unexpected.log"));
  const escaped = structuredClone(fixture.result);
  escaped.artifacts.push("data/../report.md");
  assert.throws(() => fixture.validate(escaped), /artifact outside data directory/);
});

test(caseName + ": calibration is optional, but recorded calibration must be correct", (t) => {
  const fixture = setup(t);
  const result = structuredClone(fixture.result);
  result.artifacts = result.artifacts.filter((name) => name !== "data/calibration.json");
  rmSync(path.join(fixture.dataDir, "calibration.json"));
  fixture.validate(result);
  fixture.writeCalibration();
  result.artifacts.push("data/calibration.json");
  const calibrationPath = path.join(fixture.dataDir, "calibration.json");
  const calibration = JSON.parse(readFileSync(calibrationPath, "utf8"));
  calibration[0].observed = "contradicted";
  writeFileSync(calibrationPath, JSON.stringify(calibration));
  assert.throws(() => fixture.validate(result), /correct reference judgments/);
});

test(caseName + ": observed work calls need not be below a per-phase limit, but must be positive", (t) => {
  const fixture = setup(t);
  const result = structuredClone(fixture.result);
  result.observed_model_calls = fixture.scenario.limits.max_model_calls + 1;
  fixture.writeCapture({ observed_model_calls: result.observed_model_calls });
  fixture.validate(result);
  result.observed_model_calls = null;
  fixture.writeCapture({ observed_model_calls: null });
  assert.throws(() => fixture.validate(result), /observed work-model interaction count/);
});

test(caseName + ": captured fixture must match the recorded checksum", (t) => {
  const fixture = setup(t);
  writeFileSync(path.join(fixture.dataDir, fixture.fixtureName), "tampered");
  assert.throws(() => fixture.validate(fixture.result), /captured fixture hash mismatch/);
});

test(caseName + ": scenario rejects outside fixtures and unused fields", (t) => {
  const fixture = setup(t);
  const outside: Scenario = { ...fixture.scenario, fixture: "../../README.md" };
  assert.throws(
    () => validateScenario(outside, fixture.scenarioPath, CASE_DIR),
    /fixture missing or outside/,
  );
  assert.throws(
    () =>
      validateScenario(
        { ...fixture.scenario, source_facts: "duplicate" },
        fixture.scenarioPath,
        CASE_DIR,
      ),
    /unrecognized|Unrecognized/,
  );
});

test(caseName + ": RAG limit rules belong to the RAG scenario subclass", (t) => {
  const fixture = setup(t);
  const otherWorkflow = { ...fixture.scenario, limits: { response_seconds: 30 } };
  assert.equal(
    validateScenario(otherWorkflow, fixture.scenarioPath, CASE_DIR).value.limits.response_seconds,
    30,
  );
  assert.throws(
    () => validateScenario(otherWorkflow, fixture.scenarioPath, CASE_DIR, RagScenario),
    /RAG limits must be exactly/,
  );
  assert.ok(
    validateScenario(fixture.scenario, fixture.scenarioPath, CASE_DIR, RagScenario) instanceof
      RagScenario,
  );
});

test(caseName + ": proposed scenario can be checked before activation", (t) => {
  const fixture = setup(t);
  validateScenario({ ...fixture.scenario, lifecycle: "proposed" }, fixture.scenarioPath, CASE_DIR);
  const completed = spawnSync(
    process.execPath,
    ["--import", "tsx", path.join(CASE_DIR, "scripts", "validate_scenario.ts"), "--file", fixture.scenarioPath],
    { encoding: "utf8" },
  );
  assert.equal(completed.status, 0, completed.stderr);
  assert.match(completed.stdout, new RegExp(caseName + " v1 active"));
});

}
