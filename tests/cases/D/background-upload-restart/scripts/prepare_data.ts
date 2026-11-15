/** Validate committed fixtures and upload the background before the restart check. */
import { randomUUID } from "node:crypto";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { CASE_HTTP_TIMEOUT_MS, CheckFailure, SetupError, callJson, check, httpJson, sha256 } from "../../../tools.ts";
import type { CheckRecord, HttpCall, RunRecord } from "../../../tools.ts";

export const BACKGROUNDS_PATH = "/api/appearance/backgrounds";
const PNG_MAGIC = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);

function multipartBody(filename: string, content: Buffer): [Buffer, string] {
  const boundary = "----qingqiu-case-" + randomUUID().replaceAll("-", "");
  const head = `--${boundary}\r\nContent-Disposition: form-data; name="file"; filename="${filename}"\r\nContent-Type: application/octet-stream\r\n\r\n`;
  const tail = `\r\n--${boundary}--\r\n`;
  return [Buffer.concat([Buffer.from(head), content, Buffer.from(tail)]),
    "multipart/form-data; boundary=" + boundary];
}

export async function uploadBackground(base: string, filename: string, content: Buffer,
  responses: HttpCall[]) {
  const [body, contentType] = multipartBody(filename, content);
  const envelope = await httpJson(base, "POST", BACKGROUNDS_PATH, body,
    { contentType, timeout: CASE_HTTP_TIMEOUT_MS });
  responses.push({ method: "POST", path: BACKGROUNDS_PATH,
    request: { field: "file", filename, size: content.length, sha256: sha256(content) },
    response: envelope });
  return envelope;
}

export async function prepareData(base: string, record: RunRecord, responses: HttpCall[], checks: CheckRecord[]) {
  const caseDir = record.caseDir;
  const assetPng = path.join(caseDir, "assets", "sample-background.png");
  const assetInvalid = path.join(caseDir, "assets", "invalid-background.txt");
  // Setup fails fast when the committed assets are missing or mislabeled.
  if (!existsSync(assetPng) || !existsSync(assetInvalid))
    throw new SetupError("missing committed asset under " + path.join(caseDir, "assets"));
  const sourceBytes = readFileSync(assetPng);
  const invalidBytes = readFileSync(assetInvalid);
  if (!sourceBytes.subarray(0, PNG_MAGIC.length).equals(PNG_MAGIC))
    throw new SetupError("sample-background.png is not a PNG file");
  if (path.extname(assetInvalid) !== ".txt")
    throw new SetupError("invalid-background.txt must use the .txt suffix");
  // Copy the committed bytes into the run so the post-restart check compares
  // against exactly what was uploaded, not a file that may change later.
  record.writeDataJson("binary_evidence.json", {
    source: { asset: path.relative(caseDir, assetPng), size: sourceBytes.length, sha256: sha256(sourceBytes) },
    invalid: { asset: path.relative(caseDir, assetInvalid), size: invalidBytes.length, sha256: sha256(invalidBytes) },
  });
  writeFileSync(path.join(record.dataDir, "upload-source.png"), sourceBytes);
  writeFileSync(path.join(record.dataDir, "invalid-background.txt"), invalidBytes);
  // A fresh data root must list no backgrounds before the first upload.
  const empty = await callJson(base, "GET", BACKGROUNDS_PATH, null, responses);
  check(checks, "fresh data list response", empty.code, 0);
  check(checks, "fresh data has no backgrounds", empty.data, []);
  const uploaded = await uploadBackground(base, path.basename(assetPng), sourceBytes, responses);
  check(checks, "upload response", uploaded.code, 0);
  const filename = uploaded.data?.filename;
  if (typeof filename !== "string" || !filename)
    throw new CheckFailure("upload returned invalid filename: " + JSON.stringify(filename));
}
