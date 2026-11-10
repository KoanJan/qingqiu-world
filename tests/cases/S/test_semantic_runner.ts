/** Check that RAG capture distinguishes missing traces from empty retrievals. */
import assert from "node:assert/strict";
import { appendFileSync, existsSync, mkdtempSync, readFileSync, renameSync, rmSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";
import type { TestContext } from "node:test";
import sqlite3 from "sqlite3";
import type { ChildProcess } from "node:child_process";
import type { RunRecord } from "../tools.ts";
import { classifyState, monitorSemanticCase } from "./capture_monitor.ts";
import { checkDbEvidence } from "./semantic_runner.ts";

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
  const record = {
    dataDir: dir,
    writeDataJson(filename: string, value: unknown) {
      writeFileSync(path.join(dir, filename), JSON.stringify(value));
    },
  } as RunRecord;
  const child = { exitCode: null, signalCode: null } as ChildProcess;
  return { dir, dbPath, logPath, record, child };
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
