/** Verify shared report, HTTP, and isolation mechanics used by case runners. */
import assert from "node:assert/strict";
import {
  chmodSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";
import type { TestContext } from "node:test";
import sqlite3 from "sqlite3";
import {
  CheckFailure,
  HttpShapeError,
  HttpStatusError,
  HttpTransportError,
  IsolatedServer,
  RunRecord,
  SetupError,
  buildServer,
  callJson,
  check,
  freePort,
  httpJson,
  isolatedBuildEnv,
  isolatedServerEnv,
  providerEndpoint,
  readSqlite,
} from "./tools.ts";
import type { CheckRecord, HttpCall } from "./tools.ts";

function caseDirectory(t: TestContext) {
  const temp = mkdtempSync(path.join(os.tmpdir(), "qingqiu-tools-"));
  t.after(() => rmSync(temp, { recursive: true, force: true }));
  const caseDir = path.join(temp, "D", "example-case");
  mkdirSync(caseDir, { recursive: true });
  return { temp, caseDir };
}

function sampleResult() {
  return {
    track: "deterministic",
    status: "PASS",
    detail: "Checks passed",
    execution_command: "npm --prefix tests run case -- cases/D/example-case/scripts/run.ts",
    started_at_utc: "2026-01-01T00:00:00Z",
    ended_at_utc: "2026-01-01T00:00:01Z",
    limitations: [],
  };
}

function mockFetch(
  t: TestContext,
  handler: (url: string, init: RequestInit) => Response | Promise<Response>,
) {
  const original = globalThis.fetch;
  globalThis.fetch = ((input: string | URL | Request, init?: RequestInit) =>
    Promise.resolve(handler(String(input), init ?? {}))) as typeof fetch;
  t.after(() => {
    globalThis.fetch = original;
  });
}

test("RunRecord isolates runs and generates the evidence index", (t) => {
  const { caseDir } = caseDirectory(t);
  const first = new RunRecord(caseDir);
  const second = new RunRecord(caseDir);
  assert.notEqual(first.root, second.root);
  for (const record of [first, second]) {
    assert.match(path.basename(record.root), /^\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}Z-[A-Za-z0-9]{6}$/);
  }
  first.writeDataJson("observed.json", { value: 1 });
  first.writeReport("# Result\n\nStatus: PASS\n");
  first.writeResult(sampleResult());
  const result = JSON.parse(readFileSync(path.join(first.root, "result.json"), "utf8"));
  assert.equal(result.case_name, "example-case");
  assert.equal(result.run_id, path.basename(first.root));
  assert.deepEqual(result.artifacts, ["report.md", "data/observed.json"]);
  assert.deepEqual(readdirSync(first.root).sort(), ["data", "report.md", "result.json"]);
  assert.deepEqual(readdirSync(second.dataDir), []);
});

test("RunRecord rejects scattered output and invokes validators before writing", (t) => {
  const { caseDir } = caseDirectory(t);
  const record = new RunRecord(caseDir);
  assert.throws(() => record.writeDataJson("../escape.json", {}), /direct .json file/);
  record.writeReport("Status: PASS\n");
  writeFileSync(path.join(record.root, "stray.log"), "stray");
  assert.throws(() => record.writeResult(sampleResult()), /unexpected run-root/);
  assert.equal(existsSync(path.join(record.root, "result.json")), false);
  rmSync(path.join(record.root, "stray.log"));
  assert.throws(
    () =>
      record.writeResult(sampleResult(), () => {
        throw new Error("case criterion failed");
      }),
    /case criterion failed/,
  );
  assert.equal(existsSync(path.join(record.root, "result.json")), false);
});

test("writeCase assembles a standard report and result", (t) => {
  const { caseDir } = caseDirectory(t);
  const record = new RunRecord(caseDir);
  const checks: CheckRecord[] = [{ name: "sample", passed: true, observed: 1, expected: 1 }];
  record.writeDataJson("observed.json", { value: 1 });
  record.writeCase({
    track: "deterministic",
    status: "PASS",
    detail: "Checks passed",
    executionCommand: "npm --prefix tests run case -- cases/D/example-case/scripts/run.ts",
    startedAtUtc: "2026-01-01T00:00:00Z",
    checks,
    notes: "Inspect data/observed.json.",
  });
  const result = JSON.parse(readFileSync(path.join(record.root, "result.json"), "utf8"));
  assert.equal(result.track, "deterministic");
  assert.deepEqual(result.checks, checks);
  assert.equal(result.environment.node, process.version);
  const report = readFileSync(path.join(record.root, "report.md"), "utf8");
  assert.match(report, /## Checks/);
  assert.match(report, /- PASS: sample/);
  assert.match(report, /Inspect data\/observed.json/);
});

test("RunRecord rejects false identity and external evidence", (t) => {
  const { temp, caseDir } = caseDirectory(t);
  const record = new RunRecord(caseDir);
  record.writeReport("Status: PASS\n");
  assert.throws(
    () => record.writeResult({ ...sampleResult(), case_name: "another-case" }),
    /case_name/,
  );
  const outside = path.join(temp, "outside.json");
  writeFileSync(outside, "{}");
  symlinkSync(outside, path.join(record.dataDir, "outside.json"));
  assert.throws(() => record.writeResult(sampleResult()), /evidence files must belong/);
  assert.equal(existsSync(path.join(record.root, "result.json")), false);
});

test("RunRecord rejects missing, duplicate, and stale report statuses", (t) => {
  const { caseDir } = caseDirectory(t);
  const record = new RunRecord(caseDir);
  for (const content of ["# Result\n", "Status: PASS\nStatus: PASS\n", "Status: INCONCLUSIVE\n"]) {
    record.writeReport(content);
    assert.throws(() => record.writeResult(sampleResult()), /report.md status must match/);
    assert.equal(existsSync(path.join(record.root, "result.json")), false);
  }
});

test("httpJson sends JSON and raw bytes and preserves the full envelope", async (t) => {
  const calls: Array<{ url: string; init: RequestInit }> = [];
  mockFetch(t, (url, init) => {
    calls.push({ url, init });
    return new Response('{"code":0,"data":{}}', { status: 200 });
  });
  assert.deepEqual(
    await httpJson("http://local", "PUT", "/profile", { name: "青丘" }, { expectedStatus: 200 }),
    { code: 0, data: {} },
  );
  assert.deepEqual(JSON.parse(calls[0]!.init.body as string), { name: "青丘" });
  assert.equal(
    (calls[0]!.init.headers as Record<string, string>)["Content-Type"],
    "application/json",
  );
  await httpJson("http://local", "POST", "/upload", Buffer.from("raw"), {
    contentType: "text/plain",
  });
  assert.equal(Buffer.from(calls[1]!.init.body as Uint8Array).toString(), "raw");
  assert.equal((calls[1]!.init.headers as Record<string, string>)["Content-Type"], "text/plain");
});

test("HTTP status, transport, and shape failures have distinct types", async (t) => {
  mockFetch(t, async () => new Response("{}", { status: 201 }));
  await assert.rejects(
    httpJson("http://local", "GET", "/item", null, { expectedStatus: 200 }),
    HttpStatusError,
  );
  globalThis.fetch = (async () => new Response("not JSON")) as typeof fetch;
  await assert.rejects(httpJson("http://local", "GET", "/item"), HttpTransportError);
  globalThis.fetch = (async () => new Response("[]")) as typeof fetch;
  await assert.rejects(httpJson("http://local", "GET", "/item"), HttpShapeError);
});

test("isolated build and server environments own all product paths", (t) => {
  const { temp } = caseDirectory(t);
  const dataDir = path.join(temp, "data");
  mkdirSync(dataDir);
  const build = isolatedBuildEnv(dataDir);
  const server = isolatedServerEnv(dataDir, 12345, { CASE_LIMIT: "4" });
  assert.equal(build.GOCACHE, path.join(dataDir, "go-cache"));
  assert.equal(server.DATA_ROOT, path.join(dataDir, "product"));
  assert.equal(server.PORT, "12345");
  assert.equal(server.LOG_LEVEL, "DEBUG");
  assert.equal(isolatedServerEnv(dataDir, 12345, { LOG_LEVEL: "INFO" }).LOG_LEVEL, "DEBUG");
  assert.equal(server.CASE_LIMIT, "4");
  assert.equal(existsSync(path.join(dataDir, "tmp")), true);
  assert.equal(existsSync(path.join(dataDir, "logs")), true);
  assert.throws(
    () => isolatedServerEnv(dataDir, 12345, { DATA_ROOT: "/outside" }),
    /overrides isolation/,
  );
});

test("readSqlite sees committed WAL rows without modifying product state", async (t) => {
  const { temp } = caseDirectory(t);
  const dbPath = path.join(temp, "evidence.db");
  const writer = new sqlite3.Database(dbPath);
  try {
    await new Promise<void>((resolve, reject) => {
      writer.exec("PRAGMA journal_mode=WAL; CREATE TABLE evidence (id INTEGER, label TEXT); " +
        "INSERT INTO evidence VALUES (1, 'committed');", (error) => error ? reject(error) : resolve());
    });
    const [rows, count] = await readSqlite(dbPath, [
      { sql: "SELECT * FROM evidence WHERE id = ?", params: [1] },
      { sql: "SELECT count(*) AS total FROM evidence" },
    ]);
    assert.deepEqual(rows, [{ id: 1, label: "committed" }]);
    assert.deepEqual(count, [{ total: 1 }]);
    await assert.rejects(readSqlite(dbPath, [{ sql: "INSERT INTO evidence VALUES (2, 'forbidden')" }]));
  } finally {
    await new Promise<void>((resolve, reject) => writer.close((error) => error ? reject(error) : resolve()));
  }
});

test("provider endpoint records host and URL hash without credentials", () => {
  const endpoint = providerEndpoint("https://secret@example.test/v1");
  assert.equal(endpoint.host, "example.test");
  assert.equal(JSON.stringify(endpoint).includes("secret"), false);
});

test("check captures comparisons and stops on mismatch", () => {
  const checks: CheckRecord[] = [];
  check(checks, "same", 1, 1);
  assert.deepEqual(checks, [{ name: "same", passed: true, observed: 1, expected: 1 }]);
  assert.throws(() => check(checks, "different", 2, 1), CheckFailure);
  assert.equal(checks.at(-1)?.passed, false);
});

test("callJson retains request and response evidence", async (t) => {
  mockFetch(t, async () => new Response('{"code":0}'));
  const calls: HttpCall[] = [];
  const envelope = await callJson("http://local", "PUT", "/profile", { name: "青丘" }, calls);
  assert.deepEqual(envelope, { code: 0 });
  assert.deepEqual(calls, [
    {
      method: "PUT",
      path: "/profile",
      request: { name: "青丘" },
      response: { code: 0 },
    },
  ]);
});

test("buildServer uses isolated Go state and reports a build failure", async (t) => {
  const { temp } = caseDirectory(t);
  const serverDir = path.join(temp, "server");
  const dataDir = path.join(temp, "data");
  const binDir = path.join(temp, "fake-bin");
  mkdirSync(serverDir);
  mkdirSync(dataDir);
  mkdirSync(binDir);
  const fakeGo = path.join(binDir, "go");
  writeFileSync(fakeGo, "#!/bin/sh\nprintf '%s\\n' \"$GOCACHE\"\nexit 1\n");
  chmodSync(fakeGo, 0o755);
  const oldPath = process.env.PATH;
  process.env.PATH = binDir + path.delimiter + oldPath;
  t.after(() => {
    process.env.PATH = oldPath;
  });
  await assert.rejects(buildServer(temp, dataDir), SetupError);
  assert.equal(
    readFileSync(path.join(dataDir, "build.log"), "utf8").trim(),
    path.join(dataDir, "go-cache"),
  );
});

test("isolated server owns startup, stop, and restart", async (t) => {
  const { temp } = caseDirectory(t);
  const dataDir = path.join(temp, "data");
  mkdirSync(dataDir);
  const fakeServer = path.join(temp, "fake-server");
  writeFileSync(
    fakeServer,
    "#!/usr/bin/env node\n" +
      "const http=require('node:http');\n" +
      "const server=http.createServer((req,res)=>{res.setHeader('Content-Type','application/json');" +
      "res.end(JSON.stringify({code:0}));});\n" +
      "server.listen(Number(process.env.PORT),'127.0.0.1');\n" +
      "process.on('SIGTERM',()=>server.close(()=>process.exit(0)));\n",
  );
  chmodSync(fakeServer, 0o755);
  const port = await freePort();
  const server = new IsolatedServer(
    fakeServer,
    dataDir,
    { ...process.env, PORT: String(port) },
    "http://127.0.0.1:" + port,
    5000,
  );
  t.after(async () => {
    if (server.hasProcess) await server.stop();
  });
  assert.equal(server.hasProcess, false);
  await assert.rejects(server.stop(), SetupError);
  await server.start("server.log");
  assert.equal(server.hasProcess, true);
  await assert.rejects(server.start("server-2.log"), SetupError);
  assert.equal(existsSync(path.join(dataDir, "server.log")), true);
  assert.equal(await server.stop(), 0);
  assert.equal(server.hasProcess, false);
  await server.start("server-2.log");
  assert.equal(existsSync(path.join(dataDir, "server-2.log")), true);
  assert.equal(await server.stop(), 0);
});
