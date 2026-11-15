/** Check user-profile persistence through real HTTP calls and a server restart. */
import { fileURLToPath } from "node:url";
import path from "node:path";
import { rmSync } from "node:fs";
import {
  CheckFailure,
  EXIT_CODE,
  HttpShapeError,
  HttpStatusError,
  IsolatedServer,
  RunRecord,
  caseCommand,
  check,
  utcNow,
  errorMessage,
} from "../../../tools.ts";
import type { CheckRecord, HttpCall, Status } from "../../../tools.ts";
import { prepareData } from "./prepare_data.ts";
import { executeTest } from "./execute_test.ts";

const CASE_DIR = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const ROOT = path.resolve(CASE_DIR, "../../../..");

export async function main() {
  const started = utcNow();
  const record = new RunRecord(CASE_DIR);
  const dataDir = record.dataDir;
  const command = caseCommand(ROOT, fileURLToPath(import.meta.url));
  const responses: HttpCall[] = [];
  const checks: CheckRecord[] = [];
  let server: IsolatedServer | null = null;
  let status: Status = "ERROR";
  let detail = "Case did not start";
  console.log("Evidence directory: " + record.root);
  try {
    // Phase 1: build and start an owned server against a fresh data root.
    const instance = await IsolatedServer.prepare(ROOT, dataDir);
    server = instance;
    await instance.start("server-1.log");

    // Phase 2: create and update the profile, then stop the process.
    await prepareData(instance.base, responses, checks);

    const firstExit = await instance.stop();
    check(checks, "first process stopped cleanly", firstExit, 0);
    // Phase 3: restart and verify the persisted profile through HTTP and SQLite.
    await instance.start("server-2.log");
    await executeTest(instance.base, record, responses, checks);
    const secondExit = await instance.stop();
    check(checks, "second process stopped cleanly", secondExit, 0);
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
    // Cleanup failures are infrastructure, so they override a product PASS.
    if (server?.hasProcess) {
      try {
        await server.stop();
      } catch (error) {
        status = "ERROR";
        detail += "; cleanup: " + errorMessage(error);
      }
    }
    // Keep run evidence small; a successful build no longer needs its executable.
    if (status === "PASS") {
      try { rmSync(path.join(dataDir, "bin"), { recursive: true, force: true }); }
      catch (error) { status = "ERROR"; detail += "; binary cleanup: " + errorMessage(error); }
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
  return EXIT_CODE[status];
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  process.exitCode = await main();
}
