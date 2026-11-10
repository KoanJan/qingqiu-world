/** Check user-profile persistence through real HTTP calls and a server restart. */
import { fileURLToPath } from "node:url";
import path from "node:path";
import { unlinkSync } from "node:fs";
import {
  CheckFailure,
  HttpShapeError,
  HttpStatusError,
  RunRecord,
  buildServer,
  caseCommand,
  callJson,
  check,
  freePort,
  isolatedServerEnv,
  readSqlite,
  startServer,
  stopServer,
  utcNow,
  errorMessage,
} from "../../../tools.ts";
import type { CheckRecord, HttpCall, Status } from "../../../tools.ts";

const CASE_DIR = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const ROOT = path.resolve(CASE_DIR, "../../../..");
const NAME = "profile-restart-user";
const ORIGINAL_BIO = "Created before restart";
const UPDATED_BIO = "Updated before restart";
const PERSON_TYPE_HUMAN = 2;

export async function main() {
  const started = utcNow();
  const record = new RunRecord(CASE_DIR);
  const dataDir = record.dataDir;
  const command = caseCommand(ROOT, fileURLToPath(import.meta.url));
  const responses: HttpCall[] = [];
  const checks: CheckRecord[] = [];
  let processHandle = null;
  let status: Status = "ERROR";
  let detail = "Case did not start";
  console.log("Evidence directory: " + record.root);
  try {
    const binary = await buildServer(ROOT, dataDir);
    const port = await freePort();
    const base = "http://127.0.0.1:" + port;
    const env = isolatedServerEnv(dataDir, port);
    processHandle = await startServer(binary, dataDir, env, base, "server-1.log");

    const initial = await callJson(base, "GET", "/api/user-profile", null, responses);
    check(checks, "initial profile response", initial.code, 0);
    check(checks, "fresh data has no user", initial.data, {});

    const created = await callJson(
      base,
      "PUT",
      "/api/user-profile",
      { name: NAME, bio: ORIGINAL_BIO },
      responses,
    );
    check(checks, "create response", created.code, 0);
    const personId = created.data?.id;
    check(checks, "created profile name", created.data?.name, NAME);
    check(checks, "created profile bio", created.data?.bio, ORIGINAL_BIO);
    check(checks, "created profile type", created.data?.type, 2);
    if (!Number.isInteger(personId) || personId <= 0) {
      throw new CheckFailure("created profile has invalid ID: " + JSON.stringify(personId));
    }

    const updated = await callJson(
      base,
      "PUT",
      "/api/user-profile",
      { name: "attempted-rename", bio: UPDATED_BIO },
      responses,
    );
    check(checks, "update response", updated.code, 0);
    check(checks, "user ID is stable", updated.data?.id, personId);
    check(checks, "user name is immutable", updated.data?.name, NAME);
    check(checks, "bio update took effect", updated.data?.bio, UPDATED_BIO);

    const firstExit = await stopServer(processHandle);
    processHandle = null;
    check(checks, "first process stopped cleanly", firstExit, 0);
    processHandle = await startServer(binary, dataDir, env, base, "server-2.log");
    const restored = await callJson(base, "GET", "/api/user-profile", null, responses);
    check(checks, "read after restart", restored.code, 0);
    for (const [field, expected] of [
      ["id", personId],
      ["name", NAME],
      ["bio", UPDATED_BIO],
      ["type", PERSON_TYPE_HUMAN],
    ]) {
      check(checks, "persisted " + field, restored.data?.[field], expected);
    }
    const secondExit = await stopServer(processHandle);
    processHandle = null;
    check(checks, "second process stopped cleanly", secondExit, 0);
    const [profiles] = await readSqlite(path.join(dataDir, "product", "db", "database.db"), [
      { sql: "SELECT id, name, bio, type FROM persons WHERE type = ?", params: [PERSON_TYPE_HUMAN] },
    ]);
    const rows = profiles!.map((row) => [row.id, row.name, row.bio, row.type]);
    record.writeDataJson("db_state.json", rows);
    check(checks, "exactly one persisted human profile", rows, [[personId, NAME, UPDATED_BIO, PERSON_TYPE_HUMAN]]);
    status = "PASS";
    detail = "Profile update and immutable name survived a real server restart";
  } catch (error) {
    status =
      error instanceof CheckFailure ||
      error instanceof HttpStatusError ||
      error instanceof HttpShapeError
        ? "FAIL"
        : "ERROR";
    detail = errorMessage(error);
  } finally {
    if (processHandle !== null) {
      try {
        await stopServer(processHandle);
      } catch (error) {
        status = "ERROR";
        detail += "; cleanup: " + errorMessage(error);
      }
    }
    if (status === "PASS") {
      try {
        unlinkSync(path.join(dataDir, "qingqiu-server"));
      } catch {}
    }
    record.writeDataJson("responses.json", responses);
    record.writeCase({
      track: "deterministic",
      status,
      detail,
      executionCommand: command,
      startedAtUtc: started,
      checks,
      notes:
        "The runner used a fresh data root and two owned server processes. " +
        "Inspect result.json, then data/responses.json, data/db_state.json, " +
        "data/server-1.log, and data/server-2.log in this directory.",
    });
  }
  console.log(record.caseName + ": " + status + ": " + detail);
  return { PASS: 0, FAIL: 1, ERROR: 2 }[status];
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  process.exitCode = await main();
}
