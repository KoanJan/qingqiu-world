/** Verify background upload, restart persistence, readback, and deletion. */
import path from "node:path";
import { fileURLToPath } from "node:url";
import {
  CheckFailure, EXIT_CODE, HttpShapeError, HttpStatusError, IsolatedServer, RunRecord,
  caseCommand, check, errorMessage, utcNow,
} from "../../../tools.ts";
import type { CheckRecord, HttpCall, Status } from "../../../tools.ts";
import { prepareData } from "./prepare_data.ts";
import { executeTest } from "./execute_test.ts";

const CASE_DIR = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const ROOT = path.resolve(CASE_DIR, "../../../..");

export async function main(): Promise<number> {
  const started = utcNow();
  const record = new RunRecord(CASE_DIR);
  const dataDir = record.dataDir;
  const command = caseCommand(ROOT, fileURLToPath(import.meta.url));
  const responses: HttpCall[] = [];
  const checks: CheckRecord[] = [];
  let server: IsolatedServer | null = null;
  let status: Status = "ERROR";
  let detail = "Case did not start";
  let binaryEvidence: Record<string, unknown> = {};
  console.log("Evidence directory: " + record.root);
  try {
    // Phase 1: build and start an owned server against a fresh data root.
    const instance = await IsolatedServer.prepare(ROOT, dataDir);
    server = instance;
    await instance.start("server-1.log");
    // Phase 2: upload and inspect before the restart.
    await prepareData(instance.base, record, responses, checks);
    // Phase 3: the case itself restarts the process through these callbacks,
    // then checks persistence, invalid input, and deletion.
    binaryEvidence = await executeTest(instance.base, ROOT, record, responses, checks,
      async () => {
        const firstExit = await instance.stop();
        check(checks, "first process stopped cleanly", firstExit, 0);
        await instance.start("server-2.log");
      },
      async () => {
        const secondExit = await instance.stop();
        check(checks, "second process stopped cleanly", secondExit, 0);
      });
    status = "PASS";
    detail = "Uploaded bytes survived a restart and deletion removed the file";
  } catch (error) {
    status = error instanceof CheckFailure || error instanceof HttpStatusError ||
      error instanceof HttpShapeError ? "FAIL" : "ERROR";
    detail = errorMessage(error);
  } finally {
    if (server?.hasProcess) {
      try { await server.stop(); }
      catch (error) { status = "ERROR"; detail += "; cleanup: " + errorMessage(error); }
    }
    record.writeDataJson("responses.json", responses);
    record.writeCase({ track: "deterministic", status, detail, executionCommand: command,
      startedAtUtc: started, checks, extra: { binary_evidence: binaryEvidence },
      limitations: [
        "Covers the server process and HTTP API only; it does not inspect browser rendering or Electron packaging.",
        "Uses committed assets rather than real user photos.",
      ],
      notes: "The runner used a fresh data root and two owned server processes. " +
        "Inspect result.json, then data/responses.json, data/readback_evidence.json, " +
        "data/upload-source.png, data/readback-before-restart.png, " +
        "data/readback-after-restart.png, data/server-1.log, and data/server-2.log in this directory.",
    });
  }
  console.log(record.caseName + ": " + status + ": " + detail);
  return EXIT_CODE[status];
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  process.exitCode = await main();
}
