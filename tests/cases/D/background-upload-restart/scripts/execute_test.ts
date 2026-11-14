/** Exercise upload persistence, invalid input, deletion, and the server restart. */
import { existsSync, readFileSync, readdirSync, statSync, writeFileSync } from "node:fs";
import path from "node:path";
import { CASE_HTTP_TIMEOUT_MS, CheckFailure, HttpTransportError, callJson, check,
  errorMessage, inside, sha256 } from "../../../tools.ts";
import type { CheckRecord, HttpCall, RunRecord } from "../../../tools.ts";
import { BACKGROUNDS_PATH, uploadBackground } from "./prepare_data.ts";

/** Send one raw request and return its HTTP status and response bytes. */
async function httpBytes(base: string, method: string, route: string): Promise<[number, Buffer]> {
  try {
    const response = await fetch(base + route, { method, signal: AbortSignal.timeout(CASE_HTTP_TIMEOUT_MS) });
    return [response.status, Buffer.from(await response.arrayBuffer())];
  } catch (error) {
    throw new HttpTransportError(method + " " + route + " failed: " + errorMessage(error));
  }
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

export async function executeTest(base: string, root: string, record: RunRecord,
  responses: HttpCall[], checks: CheckRecord[],
  restart: () => Promise<void>, stopCurrent: () => Promise<void>) {
  const dataDir = record.dataDir;
  const backgroundsDir = path.join(dataDir, "product", "settings", "backgrounds");
  const developerBackgrounds = path.join(root, "data", "settings", "backgrounds");
  // The prepared upload must be the only file, under the isolated data root
  // and not in the developer's data directory.
  const filenames = diskFiles(backgroundsDir);
  check(checks, "exactly one prepared background", filenames.length, 1);
  const filename = filenames[0]!;
  const invalidName = "invalid-background.txt";
  const sourceBytes = readFileSync(path.join(dataDir, "upload-source.png"));
  const invalidBytes = readFileSync(path.join(dataDir, "invalid-background.txt"));
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

  // API and disk must agree before the restart.
  const namesBeforeRestart = await backgroundNames(base, responses);
  check(checks, "list contains the uploaded file", namesBeforeRestart, [filename]);
  const readBefore = await readBackground(base, filename, responses);
  writeFileSync(path.join(dataDir, "readback-before-restart.png"), readBefore);
  check(checks, "read before restart byte length", readBefore.length, sourceBytes.length);
  check(checks, "read before restart matches upload bytes", sha256(readBefore), sha256(sourceBytes));

  // A rejected upload must not change the listing or leave any file behind.
  const rejected = await uploadBackground(base, invalidName, invalidBytes, responses);
  check(checks, "invalid extension upload is rejected", rejected.code, 1);
  check(checks, "invalid extension response carries no data", rejected.data ?? null, null);
  const namesAfterInvalid = await backgroundNames(base, responses);
  check(checks, "invalid upload did not change the listing", namesAfterInvalid, namesBeforeRestart);
  const filesAfterInvalid = diskFiles(backgroundsDir);
  check(checks, "invalid upload produced no file", filesAfterInvalid, [filename]);
  check(checks, "invalid upload left no .txt file",
    filesAfterInvalid.filter(name => name.endsWith(".txt")), []);

  await restart();

  // The uploaded bytes must remain listed and readable after the restart.
  const namesAfterRestart = await backgroundNames(base, responses);
  check(checks, "uploaded file is still listed after restart", namesAfterRestart, [filename]);
  const readAfter = await readBackground(base, filename, responses);
  writeFileSync(path.join(dataDir, "readback-after-restart.png"), readAfter);
  check(checks, "read after restart byte length", readAfter.length, sourceBytes.length);
  check(checks, "read after restart matches upload bytes", sha256(readAfter), sha256(sourceBytes));
  const binaryEvidence = {
    filename,
    source: { size: sourceBytes.length, sha256: sha256(sourceBytes) },
    readback_before_restart: { size: readBefore.length, sha256: sha256(readBefore) },
    readback_after_restart: { size: readAfter.length, sha256: sha256(readAfter) },
  };
  record.writeDataJson("readback_evidence.json", binaryEvidence);

  // Deletion must remove both the listing entry and the stored file.
  const deleted = await callJson(base, "DELETE", BACKGROUNDS_PATH + "/" +
    encodeURIComponent(filename), null, responses);
  check(checks, "delete response", deleted.code, 0);
  const namesAfterDelete = await backgroundNames(base, responses);
  check(checks, "listing is empty after delete", namesAfterDelete, []);
  check(checks, "deleted file is gone from disk", existsSync(stored), false);
  check(checks, "directory holds no files after delete", diskFiles(backgroundsDir), []);
  const readAfterDelete = await readBackground(base, filename, responses, 404);
  check(checks, "read after delete byte length", readAfterDelete.length, 0);

  await stopCurrent();
  return binaryEvidence;
}
