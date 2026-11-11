/** Verify background upload, restart persistence, readback, and deletion. */
import { randomUUID } from "node:crypto";
import type { ChildProcess } from "node:child_process";
import { existsSync, readFileSync, readdirSync, statSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import {
  CASE_HTTP_TIMEOUT_MS, CheckFailure, HttpShapeError, HttpStatusError, HttpTransportError, RunRecord, SetupError,
  buildServer, callJson, caseCommand, check, errorMessage, freePort, httpJson, inside,
  isolatedServerEnv, sha256, startServer, stopServer, utcNow,
} from "../../../tools.ts";
import type { CheckRecord, HttpCall, Status } from "../../../tools.ts";

const CASE_DIR = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const ROOT = path.resolve(CASE_DIR, "../../../..");
const ASSET_PNG = path.join(CASE_DIR, "assets", "sample-background.png");
const ASSET_INVALID = path.join(CASE_DIR, "assets", "invalid-background.txt");
const BACKGROUNDS_PATH = "/api/appearance/backgrounds";
const PNG_MAGIC = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);

/** Build a one-field multipart/form-data body for the `file` form field. */
function multipartBody(filename: string, content: Buffer): [Buffer, string] {
  const boundary = "----qingqiu-case-" + randomUUID().replaceAll("-", "");
  const head = `--${boundary}\r\nContent-Disposition: form-data; name="file"; filename="${filename}"\r\nContent-Type: application/octet-stream\r\n\r\n`;
  const tail = `\r\n--${boundary}--\r\n`;
  return [Buffer.concat([Buffer.from(head), content, Buffer.from(tail)]),
    "multipart/form-data; boundary=" + boundary];
}

/** Send one raw request and return its HTTP status and response bytes. */
async function httpBytes(base: string, method: string, route: string): Promise<[number, Buffer]> {
  try {
    const response = await fetch(base + route, { method, signal: AbortSignal.timeout(CASE_HTTP_TIMEOUT_MS) });
    return [response.status, Buffer.from(await response.arrayBuffer())];
  } catch (error) {
    throw new HttpTransportError(method + " " + route + " failed: " + errorMessage(error));
  }
}

/** Upload one file through the real multipart endpoint and record evidence. */
async function uploadBackground(base: string, filename: string, content: Buffer,
  responses: HttpCall[]) {
  const [body, contentType] = multipartBody(filename, content);
  const envelope = await httpJson(base, "POST", BACKGROUNDS_PATH, body,
    { contentType, timeout: CASE_HTTP_TIMEOUT_MS });
  responses.push({ method: "POST", path: BACKGROUNDS_PATH,
    request: { field: "file", filename, size: content.length, sha256: sha256(content) },
    response: envelope });
  return envelope;
}

/** List uploaded backgrounds through the product API and return filenames. */
async function backgroundNames(base: string, responses: HttpCall[]): Promise<string[]> {
  const envelope = await callJson(base, "GET", BACKGROUNDS_PATH, null, responses);
  if (envelope.code !== 0) {
    throw new CheckFailure("list backgrounds returned business code " + JSON.stringify(envelope.code));
  }
  if (envelope.data === null) {
    // The handler emits a nil slice as JSON null once the directory exists
    // but holds no uploads; treat it as the same empty listing.
    return [];
  }
  if (!Array.isArray(envelope.data)) {
    throw new CheckFailure("list backgrounds returned non-list data: " + JSON.stringify(envelope.data));
  }
  return envelope.data.filter((item: unknown) => item && typeof item === "object")
    .map((item: Record<string, any>) => item.filename);
}

/** Read one uploaded background as raw bytes and record transport evidence. */
async function readBackground(base: string, filename: string, responses: HttpCall[],
  expectedStatus = 200): Promise<Buffer> {
  const route = BACKGROUNDS_PATH + "/" + encodeURIComponent(filename);
  const [status, body] = await httpBytes(base, "GET", route);
  responses.push({ method: "GET", path: route, request: null,
    response: { http_status: status, size: body.length, sha256: sha256(body) } });
  if (status !== expectedStatus) {
    throw new CheckFailure(`GET ${route} returned HTTP ${status}; expected ${expectedStatus}`);
  }
  return body;
}

/** Return regular file names in a directory, or an empty list if absent. */
function diskFiles(directory: string): string[] {
  if (!existsSync(directory) || !statSync(directory).isDirectory()) return [];
  return readdirSync(directory, { withFileTypes: true })
    .filter(item => item.isFile()).map(item => item.name).sort();
}

export async function main(): Promise<number> {
  const started = utcNow();
  const record = new RunRecord(CASE_DIR);
  const dataDir = record.dataDir;
  const command = caseCommand(ROOT, fileURLToPath(import.meta.url));
  const backgroundsDir = path.join(dataDir, "product", "settings", "backgrounds");
  const developerBackgrounds = path.join(ROOT, "data", "settings", "backgrounds");
  const responses: HttpCall[] = [];
  const checks: CheckRecord[] = [];
  let processHandle: ChildProcess | null = null;
  let status: Status = "ERROR";
  let detail = "Case did not start";
  let binaryEvidence: Record<string, unknown> = {};
  console.log("Evidence directory: " + record.root);
  try {
    if (!existsSync(ASSET_PNG) || !existsSync(ASSET_INVALID)) {
      throw new SetupError("missing committed asset under " + path.join(CASE_DIR, "assets"));
    }
    const sourceBytes = readFileSync(ASSET_PNG);
    const invalidBytes = readFileSync(ASSET_INVALID);
    if (!sourceBytes.subarray(0, PNG_MAGIC.length).equals(PNG_MAGIC)) {
      throw new SetupError("sample-background.png is not a PNG file");
    }
    if (path.extname(ASSET_INVALID) !== ".txt") {
      throw new SetupError("invalid-background.txt must use the .txt suffix");
    }
    record.writeDataJson("binary_evidence.json", {
      source: { asset: path.relative(CASE_DIR, ASSET_PNG), size: sourceBytes.length,
        sha256: sha256(sourceBytes) },
      invalid: { asset: path.relative(CASE_DIR, ASSET_INVALID), size: invalidBytes.length,
        sha256: sha256(invalidBytes) },
    });
    writeFileSync(path.join(dataDir, "upload-source.png"), sourceBytes);

    const binary = await buildServer(ROOT, dataDir);
    const port = await freePort();
    const base = "http://127.0.0.1:" + port;
    const env = isolatedServerEnv(dataDir, port);
    processHandle = await startServer(binary, dataDir, env, base, "server-1.log");

    const empty = await callJson(base, "GET", BACKGROUNDS_PATH, null, responses);
    check(checks, "fresh data list response", empty.code, 0);
    check(checks, "fresh data has no backgrounds", empty.data, []);

    const uploaded = await uploadBackground(base, path.basename(ASSET_PNG), sourceBytes, responses);
    check(checks, "upload response", uploaded.code, 0);
    const filename = uploaded.data?.filename;
    if (typeof filename !== "string" || !filename) {
      throw new CheckFailure("upload returned invalid filename: " + JSON.stringify(filename));
    }
    check(checks, "uploaded filename keeps the source extension", filename.endsWith(".png"), true);
    check(checks, "uploaded filename is a bare basename",
      !filename.includes("/") && !filename.includes("\\") && !filename.includes(".."), true);
    check(checks, "backgrounds directory lives under isolated DATA_ROOT",
      inside(path.resolve(backgroundsDir), path.resolve(dataDir, "product")), true);
    const stored = path.join(backgroundsDir, filename);
    check(checks, "uploaded file exists in the isolated backgrounds directory", existsSync(stored), true);
    check(checks, "uploaded file absent from developer data directory",
      existsSync(path.join(developerBackgrounds, filename)), false);
    const storedBytes = readFileSync(stored);
    check(checks, "stored file size matches the asset", storedBytes.length, sourceBytes.length);
    check(checks, "stored file sha256 matches the asset", sha256(storedBytes), sha256(sourceBytes));

    const namesBeforeRestart = await backgroundNames(base, responses);
    check(checks, "list contains the uploaded file", namesBeforeRestart, [filename]);
    const readBefore = await readBackground(base, filename, responses);
    writeFileSync(path.join(dataDir, "readback-before-restart.png"), readBefore);
    check(checks, "read before restart byte length", readBefore.length, sourceBytes.length);
    check(checks, "read before restart matches upload bytes", sha256(readBefore), sha256(sourceBytes));

    const rejected = await uploadBackground(base, path.basename(ASSET_INVALID), invalidBytes, responses);
    check(checks, "invalid extension upload is rejected", rejected.code, 1);
    check(checks, "invalid extension response carries no data", rejected.data ?? null, null);
    const namesAfterInvalid = await backgroundNames(base, responses);
    check(checks, "invalid upload did not change the listing", namesAfterInvalid, namesBeforeRestart);
    const filesAfterInvalid = diskFiles(backgroundsDir);
    check(checks, "invalid upload produced no file", filesAfterInvalid, [filename]);
    check(checks, "invalid upload left no .txt file",
      filesAfterInvalid.filter(name => name.endsWith(".txt")), []);

    const firstExit = await stopServer(processHandle);
    processHandle = null;
    check(checks, "first process stopped cleanly", firstExit, 0);
    processHandle = await startServer(binary, dataDir, env, base, "server-2.log");

    const namesAfterRestart = await backgroundNames(base, responses);
    check(checks, "uploaded file is still listed after restart", namesAfterRestart, [filename]);
    const readAfter = await readBackground(base, filename, responses);
    writeFileSync(path.join(dataDir, "readback-after-restart.png"), readAfter);
    check(checks, "read after restart byte length", readAfter.length, sourceBytes.length);
    check(checks, "read after restart matches upload bytes", sha256(readAfter), sha256(sourceBytes));
    binaryEvidence = {
      filename,
      source: { size: sourceBytes.length, sha256: sha256(sourceBytes) },
      readback_before_restart: { size: readBefore.length, sha256: sha256(readBefore) },
      readback_after_restart: { size: readAfter.length, sha256: sha256(readAfter) },
    };
    record.writeDataJson("readback_evidence.json", binaryEvidence);

    const deleted = await callJson(base, "DELETE", BACKGROUNDS_PATH + "/" +
      encodeURIComponent(filename), null, responses);
    check(checks, "delete response", deleted.code, 0);
    const namesAfterDelete = await backgroundNames(base, responses);
    check(checks, "listing is empty after delete", namesAfterDelete, []);
    check(checks, "deleted file is gone from disk", existsSync(stored), false);
    check(checks, "directory holds no files after delete", diskFiles(backgroundsDir), []);
    const readAfterDelete = await readBackground(base, filename, responses, 404);
    check(checks, "read after delete byte length", readAfterDelete.length, 0);

    const secondExit = await stopServer(processHandle);
    processHandle = null;
    check(checks, "second process stopped cleanly", secondExit, 0);
    status = "PASS";
    detail = "Uploaded bytes survived a restart and deletion removed the file";
  } catch (error) {
    status = error instanceof CheckFailure || error instanceof HttpStatusError ||
      error instanceof HttpShapeError ? "FAIL" : "ERROR";
    detail = errorMessage(error);
  } finally {
    if (processHandle !== null) {
      try { await stopServer(processHandle); }
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
  return { PASS: 0, FAIL: 1, ERROR: 2, INCONCLUSIVE: 3 }[status];
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  process.exitCode = await main();
}
