/** Offline checks for semantic capture, runner boundaries, and RAG evidence. */
import assert from "node:assert/strict";
import { appendFileSync, existsSync, mkdtempSync, readFileSync, readdirSync, renameSync, rmSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";
import type { TestContext } from "node:test";
import sqlite3 from "sqlite3";
import type { ServerStatus } from "../tools.ts";
import { classifyState, monitorSemanticCase } from "./capture_monitor.ts";
import type { EvidenceSink } from "./capture_monitor.ts";
import { checkDbEvidence } from "./rag_execute_test.ts";
import { runSemanticCase, statusAfterInfrastructureFailure } from "./semantic_runner.ts";
import type { SemanticWorkflow } from "./semantic_runner.ts";
import type { BaseScenario } from "./semantic_contract.ts";
import { REQUIRED_PROVIDER_ENV } from "./provider_env.ts";

test("missing retrieval trace differs from a completed lookup that returned no evidence", (t) => {
  const temp = mkdtempSync(path.join(os.tmpdir(), "qingqiu-semantic-"));
  t.after(() => rmSync(temp, { recursive: true, force: true }));
  const fixture = path.join(temp, "source.md");
  writeFileSync(fixture, "source passage");
  const session = {
    document: { id: 7, active_revision_id: 3 },
    kb_id: 1,
    agent_id: 2,
  };
  const evidence: Parameters<typeof checkDbEvidence>[0] = {
    document: { knowledge_base_id: 1, status: 2, active_revision_id: 3, file_path: fixture },
    kb_access: [{ person_id: 2, kb_id: 1 }],
    active_chunks: [{ id: 9 }],
    works: [],
    interactions: [],
    kb_usage_traces: [],
  };
  assert.deepEqual(checkDbEvidence(evidence, session, fixture), [
    "record: no persisted work records for the chat session",
    "record: no KB retrieval trace was recorded for the chat session",
  ]);

  evidence.works = [{ id: 4 }];
  evidence.kb_usage_traces = [
    {
      id: 5,
      authorized_kb_ids_json: JSON.stringify([1]),
      evidence_handles_json: "[]",
      returned_evidence_count: 0,
    },
  ];
  assert.deepEqual(checkDbEvidence(evidence, session, fixture), [
    "retrieval: KB retrieval returned no source evidence",
  ]);
  evidence.kb_usage_traces[0]!.returned_evidence_count = 1;
  assert.deepEqual(checkDbEvidence(evidence, session, fixture), []);
});

test("event observation alone does not close an in-flight decision or Work", () => {
  const trigger = { id: 1, event_type: 1, ref_id: 10, agent_id: 2,
    observation_id: 1, decision_id: null, buffer_id: null,
    message_person_id: 1, agent_is_participant: 1, last_read_message_id: 0 };
  const open = classifyState([trigger], [], [], [], 10);
  assert.equal(open.settled, false);
  assert.deepEqual(open.observed_without_decision_ids, [1]);
  const decided = { ...trigger, decision_id: 3 };
  const working = classifyState([decided], [{ id: 3 }], [{ id: 4, status: 1 }],
    [{ id: 5, status: 0 }], 10);
  assert.equal(working.settled, false);
  assert.deepEqual(working.running_action_ids, [4]);
  assert.deepEqual(working.running_work_ids, [5]);
  const finished = classifyState([
    decided,
    { id: 6, event_type: 4, ref_id: 5, agent_id: 2, work_person_id: 2,
      observation_id: 7, decision_id: 8, buffer_id: null },
  ], [{ id: 3 }, { id: 8 }], [{ id: 4, status: 2 }], [{ id: 5, status: 1 }], 10);
  assert.equal(finished.settled, true);
});

async function monitorFixture(t: TestContext, withReply: boolean) {
  const dir = mkdtempSync(path.join(os.tmpdir(), "qingqiu-stop-monitor-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const dbPath = path.join(dir, "database.db");
  const db = new sqlite3.Database(dbPath);
  await new Promise<void>((resolve, reject) => db.exec(`
    CREATE TABLE events (id INTEGER PRIMARY KEY, event_type INTEGER, ref_id INTEGER);
    CREATE TABLE messages (id INTEGER PRIMARY KEY, session_id INTEGER, person_id INTEGER, content TEXT);
    CREATE TABLE participant_sessions (id INTEGER PRIMARY KEY, session_id INTEGER, participant_id INTEGER, last_read_message_id INTEGER);
    CREATE TABLE agent_observations (id INTEGER PRIMARY KEY, event_id INTEGER, person_id INTEGER);
    CREATE TABLE decisions (id INTEGER PRIMARY KEY, event_id INTEGER, person_id INTEGER);
    CREATE TABLE actions (id INTEGER PRIMARY KEY, decision_id INTEGER, status INTEGER);
    CREATE TABLE works (id INTEGER PRIMARY KEY, person_id INTEGER, session_id INTEGER, status INTEGER);
    CREATE TABLE agent_event_buffers (id INTEGER PRIMARY KEY, event_id INTEGER, person_id INTEGER);
    INSERT INTO messages VALUES (10, 1, 1, 'request');
    INSERT INTO events VALUES (1, 1, 10);
    INSERT INTO participant_sessions VALUES (1, 1, 2, 0);
    ${withReply ? "INSERT INTO messages VALUES (11, 1, 2, 'I have finished.');" : ""}
  `, (error) => error ? reject(error) : resolve()));
  await new Promise<void>((resolve, reject) => db.close((error) => error ? reject(error) : resolve()));
  const logPath = path.join(dir, "server.log");
  writeFileSync(logPath, "startup\n");
  const sink: EvidenceSink = {
    dataDir: dir,
    writeDataJson(filename: string, value: unknown) {
      writeFileSync(path.join(dir, filename), JSON.stringify(value));
    },
  };
  const child: ServerStatus = { exitCode: null, signalCode: null };
  return { dir, dbPath, logPath, sink, child };
}

async function answerReview(dir: string, expectedPhase: string, requestId = 1,
  decision: "stop" | "continue" | "uncertain" = "stop") {
  const requestPath = path.join(dir, "stop_review_request.json");
  const responsePath = path.join(dir, "stop_review_response.json");
  const deadline = Date.now() + 2_000;
  let request: Record<string, any> | null = null;
  while (Date.now() < deadline) {
    if (existsSync(requestPath)) {
      request = JSON.parse(readFileSync(requestPath, "utf8"));
      if (request?.request_id === requestId) break;
    }
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  assert.ok(request && request.request_id === requestId, "review request should arrive");
  assert.equal(request.phase, expectedPhase);
  const temporary = responsePath + ".tmp";
  writeFileSync(temporary, JSON.stringify({ request_id: request.request_id, decision, reason: "reviewed" }));
  renameSync(temporary, responsePath);
  return request;
}

test("a completed reply stops before the first state poll", async (t) => {
  const fixture = await monitorFixture(t, true);
  const running = monitorSemanticCase({ ...fixture, agentId: 2, triggerMessageId: 10,
    requestText: "request", baseline: { event_id: 0, decision_id: 0, work_id: 0, message_id: 10 },
    timeoutSeconds: 3, messagePollMs: 5, statePollMs: 200 });
  await answerReview(fixture.dir, "reply");
  assert.equal((await running).reason, "reply_complete");
  assert.equal(existsSync(path.join(fixture.dir, "state_polls.json")), false);
});

test("a provisional reply can continue to a later completed reply", async (t) => {
  const fixture = await monitorFixture(t, true);
  const running = monitorSemanticCase({ ...fixture, agentId: 2, triggerMessageId: 10,
    requestText: "request", baseline: { event_id: 0, decision_id: 0, work_id: 0, message_id: 10 },
    timeoutSeconds: 4, messagePollMs: 5, statePollMs: 3_000 });
  await answerReview(fixture.dir, "reply", 1, "continue");
  const db = new sqlite3.Database(fixture.dbPath);
  await new Promise<void>((resolve, reject) => db.exec(
    "INSERT INTO messages VALUES (12, 1, 2, 'Now finished.');",
    (error) => error ? reject(error) : resolve()));
  await new Promise<void>((resolve, reject) => db.close((error) => error ? reject(error) : resolve()));
  await answerReview(fixture.dir, "reply", 2);
  const result = await running;
  assert.equal(result.reason, "reply_complete");
  assert.deepEqual(result.messages.map((item) => item.id), [11, 12]);
  const reviews = JSON.parse(readFileSync(path.join(fixture.dir, "stop_review_history.json"), "utf8"));
  assert.deepEqual(reviews.map((item: Record<string, any>) => item.decision), ["continue", "stop"]);
});

test("settled persisted state stops a silent exchange without AI review", async (t) => {
  const fixture = await monitorFixture(t, false);
  const db = new sqlite3.Database(fixture.dbPath);
  await new Promise<void>((resolve, reject) => db.exec(
    "INSERT INTO agent_observations VALUES (1, 1, 2); " +
    "INSERT INTO decisions VALUES (1, 1, 2);",
    (error) => error ? reject(error) : resolve()));
  await new Promise<void>((resolve, reject) => db.close((error) => error ? reject(error) : resolve()));
  const result = await monitorSemanticCase({ ...fixture, agentId: 2, triggerMessageId: 10,
    requestText: "request", baseline: { event_id: 0, decision_id: 0, work_id: 0, message_id: 10 },
    timeoutSeconds: 2, messagePollMs: 5, statePollMs: 20 });
  assert.equal(result.reason, "state_settled");
  assert.equal(existsSync(path.join(fixture.dir, "stop_review_request.json")), false);
});

test("unchanged ongoing state sends new runtime logs to the AI tester", async (t) => {
  const fixture = await monitorFixture(t, false);
  const running = monitorSemanticCase({ ...fixture, agentId: 2, triggerMessageId: 10,
    requestText: "request", baseline: { event_id: 0, decision_id: 0, work_id: 0, message_id: 10 },
    timeoutSeconds: 3, messagePollMs: 5, statePollMs: 30 });
  await new Promise((resolve) => setTimeout(resolve, 15));
  appendFileSync(fixture.logPath, "runtime error while processing\n");
  const review = await answerReview(fixture.dir, "stalled");
  assert.match(review.new_log.text, /runtime error/);
  assert.equal((await running).reason, "tester_stopped");
});

test("a definition failure returns the setup exit code without a judgeable result", async (t) => {
  const caseDir = mkdtempSync(path.join(os.tmpdir(), "qingqiu-definition-"));
  t.after(() => rmSync(caseDir, { recursive: true, force: true }));
  // Every product hook asserts it was not reached: a definition failure must
  // stop before setup, capture, or judgment.
  const unreachable = async () => { throw new Error("product phase must not run"); };
  const workflow: SemanticWorkflow<BaseScenario> = {
    caseDir,
    subjectEntry: "none",
    failurePrefixes: "none",
    evidenceDescription: "none",
    evidenceExplanation: "none",
    async loadDefinition() { throw new Error("invalid scenario definition"); },
    prepareData: unreachable,
    executeTest: unreachable,
    capturedSession: () => null,
    async captureEvidence() { throw new Error("product phase must not run"); },
    resultFields: () => ({}),
    limitations: () => [],
    reportSections: () => "",
  };
  assert.equal(await runSemanticCase(workflow), 2);
  const runs = readdirSync(path.join(caseDir, "reports"));
  assert.equal(runs.length, 1);
  const runDir = path.join(caseDir, "reports", runs[0]!);
  assert.match(readFileSync(path.join(runDir, "report.md"), "utf8"), /Status: ERROR/);
  const capture = JSON.parse(readFileSync(path.join(runDir, "data", "capture.json"), "utf8"));
  assert.equal(capture.status, "ERROR");
  assert.equal(capture.mechanical_checks_performed, false);
  // A pre-definition failure has no scenario identity or criteria, so it must
  // not emit a result.json that violates the semantic result contract.
  assert.equal(existsSync(path.join(runDir, "result.json")), false);
});

test("an unusable provider endpoint still finalizes a diagnostic ERROR run", async (t) => {
  const caseDir = mkdtempSync(path.join(os.tmpdir(), "qingqiu-input-"));
  t.after(() => rmSync(caseDir, { recursive: true, force: true }));
  const scenarioPath = path.join(caseDir, "scenario.json");
  writeFileSync(scenarioPath, "{}");
  const scenario: BaseScenario = {
    required: [{ id: "required-check", condition: "criterion" }],
    forbidden: [{ id: "forbidden-check", condition: "criterion" }],
    limits: { answer_timeout_seconds: 1 },
  };
  const unreachable = async () => { throw new Error("product phase must not run"); };
  const workflow: SemanticWorkflow<BaseScenario> = {
    caseDir,
    subjectEntry: "none",
    failurePrefixes: "none",
    evidenceDescription: "none",
    evidenceExplanation: "none",
    async loadDefinition() {
      return { scenario, scenarioPath, validateResult() {} };
    },
    prepareData: unreachable,
    executeTest: unreachable,
    capturedSession: () => null,
    captureEvidence: unreachable,
    resultFields: () => ({}),
    limitations: () => [],
    reportSections: () => "",
  };
  const saved = REQUIRED_PROVIDER_ENV.map((name) => [name, process.env[name]] as const);
  // Restore the caller's provider environment after the test mutates it.
  t.after(() => {
    for (const [name, value] of saved) {
      if (value === undefined) delete process.env[name];
      else process.env[name] = value;
    }
  });
  process.env.QINGQIU_TEST_LLM_BASE_URL = "not a url";
  // The remaining inputs are valid, so the only failure is URL parsing during
  // input preparation; the run must still finalize with a result.
  process.env.QINGQIU_TEST_LLM_MODEL_ID = "model";
  process.env.QINGQIU_TEST_LLM_API_KEY = "key";
  process.env.QINGQIU_TEST_EMBEDDING_BASE_URL = "https://embedding.invalid";
  process.env.QINGQIU_TEST_EMBEDDING_MODEL_ID = "model";
  process.env.QINGQIU_TEST_EMBEDDING_API_KEY = "key";

  assert.equal(await runSemanticCase(workflow), 2);
  const runDir = path.join(caseDir, "reports", readdirSync(path.join(caseDir, "reports"))[0]!);
  assert.match(readFileSync(path.join(runDir, "report.md"), "utf8"), /Status: ERROR/);
  const result = JSON.parse(readFileSync(path.join(runDir, "result.json"), "utf8"));
  assert.equal(result.status, "ERROR");
  assert.match(result.detail, /Run input could not be prepared/);
  assert.equal(result.model.llm, null);
  assert.ok(result.artifacts.includes("data/metadata.json"));
  assert.ok(result.artifacts.includes("data/capture.json"));
});

test("an infrastructure failure cannot turn a mechanical FAIL into ERROR", () => {
  assert.equal(statusAfterInfrastructureFailure("FAIL"), "FAIL");
  assert.equal(statusAfterInfrastructureFailure("INCONCLUSIVE"), "ERROR");
  assert.equal(statusAfterInfrastructureFailure("ERROR"), "ERROR");
});
