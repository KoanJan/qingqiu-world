/**
 * Shared mechanics for cross-component test runners.
 *
 * Case runners keep their product workflow, assertions, and verdict rules. This
 * module owns report layout, local process lifecycle and prerequisites, and HTTP
 * transport.
 */
import { createHash } from "node:crypto";
import { spawn } from "node:child_process";
import type { ChildProcess, SpawnOptions } from "node:child_process";
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  openSync,
  closeSync,
  readFileSync,
  readdirSync,
  realpathSync,
  rmSync,
  lstatSync,
  writeFileSync,
} from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { once } from "node:events";
import sqlite3 from "sqlite3";

/** A request did not return a usable JSON object. */
export class HttpRequestError extends Error {}

/** The server returned an unexpected HTTP status. */
export class HttpStatusError extends HttpRequestError {}

/** The response could not be fetched or decoded as a JSON object. */
export class HttpTransportError extends HttpRequestError {}

/** The server returned valid JSON with the wrong envelope shape. */
export class HttpShapeError extends HttpRequestError {}

/** The product run could not reach the behavior under test. */
export class SetupError extends Error {}

/** An observed product response contradicted the case contract. */
export class CheckFailure extends Error {}

export const STATUSES = ["PASS", "FAIL", "INCONCLUSIVE", "ERROR"] as const;
export type Status = (typeof STATUSES)[number];
/** Process exit code for each final case status. */
export const EXIT_CODE: Record<Status, number> = { PASS: 0, FAIL: 1, ERROR: 2, INCONCLUSIVE: 3 };
/** The process facts that scenario monitors may observe without owning the process. */
export type ServerStatus = Readonly<Pick<ChildProcess, "exitCode" | "signalCode">>;
export type CheckRecord = { name: string; passed: boolean; observed: unknown; expected: unknown };
export type HttpCall = {
  method: string;
  path: string;
  request: unknown;
  response: Record<string, any>;
};
const DEFAULT_HTTP_TIMEOUT_MS = 30_000;
export const CASE_HTTP_TIMEOUT_MS = 10_000;
const BUILD_TIMEOUT_MS = 300_000;
const SERVER_START_TIMEOUT_MS = 90_000;
const SERVER_READY_POLL_MS = 250;
const SERVER_STOP_TIMEOUT_MS = 20_000;
const HTTP_OK = 200;
export const errorMessage = (error: unknown) =>
  error instanceof Error ? error.message : String(error);

export const utcNow = () => new Date().toISOString();
export const sleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));
export const sha256 = (value: string | Buffer) => createHash("sha256").update(value).digest("hex");
export const writeJson = (file: string, value: unknown) =>
  writeFileSync(file, JSON.stringify(value, null, 2) + "\n");
export const inside = (target: string, parent: string) =>
  target === parent || target.startsWith(parent + path.sep);

/** Return the repository-root command recorded with a case result. */
export function caseCommand(root: string, script: string) {
  return "npm --prefix tests run case -- " + path.relative(path.join(root, "tests"), path.resolve(script));
}

export type SqliteQuery = { sql: string; params?: Array<string | number> };
export type SqliteRow = Record<string, any>;

/** Read committed evidence through one read-only SQLite connection. */
export async function readSqlite(dbPath: string, queries: SqliteQuery[]): Promise<SqliteRow[][]> {
  const db = await new Promise<sqlite3.Database>((resolve, reject) => {
    const connection = new sqlite3.Database(dbPath, sqlite3.OPEN_READONLY, (error) => {
      if (error) reject(error);
      else resolve(connection);
    });
  });
  try {
    const results: SqliteRow[][] = [];
    for (const { sql, params = [] } of queries) {
      results.push(await new Promise<SqliteRow[]>((resolve, reject) => {
        db.all(sql, params, (error, rows) => {
          if (error) reject(error);
          else resolve(rows as SqliteRow[]);
        });
      }));
    }
    return results;
  } finally {
    await new Promise<void>((resolve, reject) => {
      db.close((error) => error ? reject(error) : resolve());
    });
  }
}

/** Choose an available loopback port for one owned server process. */
export async function freePort() {
  const server = net.createServer();
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const address = server.address();
  if (!address || typeof address === "string") throw new SetupError("loopback port unavailable");
  const port = address.port;
  server.close();
  await once(server, "close");
  return port;
}

/**
 * Send JSON or raw bytes and return the full JSON response envelope.
 *
 * Business response codes remain the caller's responsibility. HTTP status
 * and transport failures have separate types so cases can classify them.
 */
export async function httpJson(
  base: string,
  method: string,
  route: string,
  payload: unknown = null,
  options: { contentType?: string | null; timeout?: number; expectedStatus?: number | null } = {},
): Promise<Record<string, any>> {
  const { contentType = null, timeout = DEFAULT_HTTP_TIMEOUT_MS, expectedStatus = null } = options;
  const headers: Record<string, string> = {};
  let body: string | Buffer | undefined;
  if (payload !== null) {
    if (contentType === null) {
      body = JSON.stringify(payload);
      headers["Content-Type"] = "application/json";
    } else {
      if (!Buffer.isBuffer(payload)) throw new TypeError("raw HTTP payload must be bytes");
      body = payload;
      headers["Content-Type"] = contentType;
    }
  }
  let response;
  try {
    response = await fetch(base + route, {
      method,
      headers,
      body: body as BodyInit | undefined,
      signal: AbortSignal.timeout(timeout),
    });
  } catch (error) {
    throw new HttpTransportError(
      method + " " + route + " could not return JSON: " + errorMessage(error),
    );
  }
  if (!response.ok || (expectedStatus !== null && response.status !== expectedStatus)) {
    throw new HttpStatusError(
      method +
        " " +
        route +
        " returned HTTP " +
        response.status +
        (expectedStatus === null ? "" : "; expected " + expectedStatus),
    );
  }
  let envelope;
  try {
    envelope = await response.json();
  } catch (error) {
    throw new HttpTransportError(
      method + " " + route + " could not return JSON: " + errorMessage(error),
    );
  }
  if (envelope === null || Array.isArray(envelope) || typeof envelope !== "object") {
    throw new HttpShapeError(method + " " + route + " returned a non-object response");
  }
  return envelope as Record<string, any>;
}

/** Record one exact comparison as evidence and stop on a mismatch. */
export function check(checks: CheckRecord[], label: string, observed: unknown, expected: unknown) {
  const passed = JSON.stringify(observed) === JSON.stringify(expected);
  checks.push({ name: label, passed, observed, expected });
  if (!passed)
    throw new CheckFailure(
      label + ": expected " + JSON.stringify(expected) + ", observed " + JSON.stringify(observed),
    );
}

/** Send one product request and preserve its request and response as evidence. */
export async function callJson(
  base: string,
  method: string,
  route: string,
  payload: unknown,
  responses: HttpCall[],
  expectedStatus = HTTP_OK,
) {
  const envelope = await httpJson(base, method, route, payload, { timeout: CASE_HTTP_TIMEOUT_MS, expectedStatus });
  responses.push({ method, path: route, request: payload ?? null, response: envelope });
  return envelope;
}

/** Record a provider host and URL fingerprint without storing credentials. */
export function providerEndpoint(url: string) {
  return { host: new URL(url).hostname, sha256: sha256(url) };
}

/** Require one report status declaration matching the machine result. */
export function validateReportStatus(reportPath: string, status: Status) {
  const declarations = readFileSync(reportPath, "utf8")
    .split(/\r?\n/)
    .filter((line) => line.startsWith("Status:"))
    .map((line) => line.slice("Status:".length).trim());
  if (declarations.length !== 1 || declarations[0] !== status) {
    throw new Error("report.md status must match result.json status");
  }
}

/** Keep Go build and temporary files inside this run's data directory. */
export function isolatedBuildEnv(dataDir: string) {
  const tmp = path.join(dataDir, "tmp");
  mkdirSync(tmp, { recursive: true });
  return {
    ...process.env,
    GOCACHE: path.join(dataDir, "go-cache"),
    GOTMPDIR: tmp,
    TMPDIR: tmp,
    TMP: tmp,
    TEMP: tmp,
  };
}

/** Give every owned test server isolated state and DEBUG application logs. */
export function isolatedServerEnv(
  dataDir: string,
  port: number,
  extra: Record<string, string> = {},
): NodeJS.ProcessEnv {
  const product = path.join(dataDir, "product");
  const logs = path.join(dataDir, "logs");
  const tmp = path.join(dataDir, "tmp");
  for (const directory of [product, logs, tmp]) mkdirSync(directory, { recursive: true });
  const protectedKeys = [
    "DATA_ROOT",
    "LOG_DIR",
    "AOS_ROOT",
    "AOS_META_ROOT",
    "JINSHU_ROOT",
    "PORT",
    "TMPDIR",
    "TMP",
    "TEMP",
  ];
  const overlap = Object.keys(extra).filter((key) => protectedKeys.includes(key));
  if (overlap.length)
    throw new Error("extra server environment overrides isolation: " + overlap.join(", "));
  return {
    ...process.env,
    DATA_ROOT: product,
    LOG_DIR: logs,
    AOS_ROOT: path.join(product, "aos"),
    AOS_META_ROOT: path.join(product, "aosmeta"),
    JINSHU_ROOT: path.join(product, "jinshu"),
    PORT: String(port),
    TMPDIR: tmp,
    TMP: tmp,
    TEMP: tmp,
    ...extra,
    LOG_LEVEL: "DEBUG",
  };
}

function runChild(
  command: string,
  args: string[],
  options: SpawnOptions,
  timeout: number,
  logPath: string,
): Promise<number | null> {
  return new Promise((resolve, reject) => {
    const fd = openSync(logPath, "w");
    const child = spawn(command, args, { ...options, stdio: ["ignore", fd, fd] });
    closeSync(fd);
    let timedOut = false;
    const timer = setTimeout(() => {
      timedOut = true;
      child.kill("SIGKILL");
    }, timeout);
    child.once("error", (error) => {
      clearTimeout(timer);
      reject(new SetupError(error.message));
    });
    child.once("exit", (code) => {
      clearTimeout(timer);
      if (timedOut) reject(new SetupError(command + " exceeded " + timeout / 1000 + " seconds"));
      else resolve(code);
    });
  });
}

/** Build the owned server binary and keep Go build state under dataDir. */
export async function buildServer(root: string, dataDir: string) {
  const binary = path.join(dataDir, "bin", "qingqiu-server");
  mkdirSync(path.dirname(binary), { recursive: true });
  let code;
  try {
    code = await runChild(
      "go",
      ["build", "-o", binary, "./cmd"],
      { cwd: path.join(root, "server"), env: isolatedBuildEnv(dataDir) },
      BUILD_TIMEOUT_MS,
      path.join(dataDir, "build.log"),
    );
  } finally {
    rmSync(path.join(dataDir, "go-cache"), { recursive: true, force: true });
  }
  if (code !== 0) throw new SetupError("server build failed; inspect build.log");
  return binary;
}

/** Own one isolated test server's configuration and process lifecycle. */
export class IsolatedServer implements ServerStatus {
  private child: ChildProcess | null = null;
  private readonly binary: string;
  private readonly dataDir: string;
  private readonly env: NodeJS.ProcessEnv;
  readonly base: string;
  private readonly startTimeout: number;

  constructor(
    binary: string, dataDir: string, env: NodeJS.ProcessEnv,
    base: string, startTimeout = SERVER_START_TIMEOUT_MS,
  ) {
    this.binary = binary;
    this.dataDir = dataDir;
    this.env = env;
    this.base = base;
    this.startTimeout = startTimeout;
  }

  /** Prepare an isolated binary, data root, and port without starting the server. */
  static async prepare(root: string, dataDir: string,
    extraEnv: Record<string, string> = {}, startTimeout = SERVER_START_TIMEOUT_MS): Promise<IsolatedServer> {
    const binary = await buildServer(root, dataDir);
    const port = await freePort();
    const env = isolatedServerEnv(dataDir, port, extraEnv);
    return new IsolatedServer(binary, dataDir, env, `http://127.0.0.1:${port}`, startTimeout);
  }

  private get process(): ChildProcess {
    if (this.child === null) throw new SetupError("isolated server has not started");
    return this.child;
  }

  get exitCode(): number | null { return this.process.exitCode; }
  get signalCode(): NodeJS.Signals | null { return this.process.signalCode; }
  get hasProcess(): boolean { return this.child !== null; }

  /** Start the prepared server and wait until it answers the readiness request. */
  async start(logName = "server.log"): Promise<void> {
    if (this.child !== null) throw new SetupError("isolated server already has a process");
    this.child = await startServer(this.binary, this.dataDir, this.env, this.base,
      logName, this.startTimeout);
  }

  /** Stop the owned process and release its handle, including an already exited process. */
  async stop(timeout = SERVER_STOP_TIMEOUT_MS): Promise<number> {
    const child = this.child;
    if (child === null) throw new SetupError("isolated server has no process to stop");
    try {
      return await stopServer(child, timeout);
    } finally {
      if (child.exitCode !== null || child.signalCode !== null) this.child = null;
    }
  }
}

/** Start one owned server, appending output, and wait until it answers. */
async function startServer(
  binary: string,
  dataDir: string,
  env: NodeJS.ProcessEnv,
  base: string,
  logName = "server.log",
  timeout = SERVER_START_TIMEOUT_MS,
): Promise<ChildProcess> {
  const fd = openSync(path.join(dataDir, logName), "a");
  let child: ChildProcess;
  try {
    child = spawn(binary, [], { cwd: dataDir, env, stdio: ["ignore", fd, fd] });
  } finally {
    closeSync(fd);
  }
  let startError: Error | null = null;
  child.once("error", (error) => {
    startError = error;
  });
  const deadline = Date.now() + timeout;
  try {
    while (Date.now() < deadline) {
      if (startError) throw new SetupError("server could not start: " + errorMessage(startError));
      if (child.exitCode !== null || child.signalCode !== null) {
        throw new SetupError(
          "server exited during startup with " + (child.exitCode ?? child.signalCode),
        );
      }
      try {
        const response = await httpJson(base, "GET", "/api/version", null, {
          timeout: CASE_HTTP_TIMEOUT_MS,
          expectedStatus: HTTP_OK,
        });
        if (response.code === 0) return child;
      } catch (error) {
        if (!(error instanceof HttpRequestError)) throw error;
      }
      await sleep(SERVER_READY_POLL_MS);
    }
    throw new SetupError("server did not become ready within " + timeout / 1000 + " seconds");
  } catch (error) {
    // A process that started but never became ready still belongs to this run.
    if (child.pid !== undefined && child.exitCode === null && child.signalCode === null) {
      try { await stopServer(child); }
      catch (cleanupError) {
        throw new SetupError(errorMessage(error) + "; cleanup: " + errorMessage(cleanupError));
      }
    }
    throw error;
  }
}

/** Stop one owned server gracefully and return its exit code. */
async function stopServer(child: ChildProcess, timeout = SERVER_STOP_TIMEOUT_MS): Promise<number> {
  if (child.exitCode !== null || child.signalCode !== null) return child.exitCode ?? -1;
  const exited = once(child, "exit");
  child.kill("SIGTERM");
  let timer: ReturnType<typeof setTimeout> | undefined;
  const timedOut = new Promise<false>((resolve) => {
    timer = setTimeout(() => resolve(false), timeout);
  });
  const finished = await Promise.race([exited.then(() => true), timedOut]);
  clearTimeout(timer);
  if (!finished) {
    child.kill("SIGKILL");
    await exited.catch(() => {});
    throw new SetupError("owned server did not stop gracefully");
  }
  return child.exitCode ?? -1;
}

/** Create and finalize one isolated, Git-ignored case report directory. */
export class RunRecord {
  readonly caseDir: string;
  readonly caseName: string;
  readonly root: string;
  readonly dataDir: string;

  constructor(caseDir: string) {
    this.caseDir = realpathSync(caseDir);
    this.caseName = path.basename(this.caseDir);
    const reports = path.join(this.caseDir, "reports");
    mkdirSync(reports, { recursive: true });
    const runPrefix = utcNow().slice(0, 19).replace(/:/g, "-") + "Z-";
    this.root = mkdtempSync(path.join(reports, runPrefix));
    this.dataDir = path.join(this.root, "data");
    mkdirSync(this.dataDir);
  }

  /** Write a JSON evidence file directly inside this run's data directory. */
  writeDataJson(filename: string, value: unknown) {
    if (!filename.endsWith(".json") || path.basename(filename) !== filename) {
      throw new Error("data JSON filename must be a direct .json file");
    }
    writeJson(path.join(this.dataDir, filename), value);
  }

  /** Write the human-readable conclusion before finalizing the result. */
  writeReport(content: string) {
    writeFileSync(path.join(this.root, "report.md"), content);
  }

  /** Index top-level evidence files; product state stays under data/ unlisted. */
  artifactPaths() {
    const files = readdirSync(this.dataDir).filter((name) => {
      const file = path.join(this.dataDir, name);
      if (lstatSync(file).isSymbolicLink()) {
        throw new Error("evidence files must belong to this run's data directory");
      }
      if (!lstatSync(file).isFile()) return false;
      if (!inside(realpathSync(file), this.dataDir)) {
        throw new Error("evidence files must belong to this run's data directory");
      }
      return true;
    });
    return ["report.md", ...files.sort().map((name) => "data/" + name)];
  }

  /** Add common identity/evidence fields and validate the report layout. */
  writeResult(
    fields: Record<string, any>,
    validator: ((result: Record<string, any>) => void) | null = null,
  ) {
    const result: Record<string, any> = { ...fields };
    for (const [key, value] of [
      ["case_name", this.caseName],
      ["run_id", path.basename(this.root)],
    ]) {
      if (key in result && result[key] !== value)
        throw new Error(key + " must match the case/run directory");
      result[key] = value;
    }
    if ("artifacts" in result) throw new Error("artifacts are generated by RunRecord");
    result.artifacts = this.artifactPaths();
    for (const key of [
      "track",
      "status",
      "detail",
      "execution_command",
      "started_at_utc",
      "ended_at_utc",
      "limitations",
    ]) {
      if (!(key in result)) throw new Error("result missing common field: " + key);
    }
    if (!["PASS", "FAIL", "INCONCLUSIVE", "ERROR"].includes(result.status))
      throw new Error("invalid result status");
    if (!Array.isArray(result.limitations)) throw new Error("limitations must be a list");
    const report = path.join(this.root, "report.md");
    const resultPath = path.join(this.root, "result.json");
    if (!existsSync(report)) throw new Error("report.md must exist before result.json");
    for (const item of [report, resultPath, this.dataDir]) {
      if (existsSync(item) && lstatSync(item).isSymbolicLink())
        throw new Error("run-root entries must belong to this run");
    }
    validateReportStatus(report, result.status);
    const unexpected = readdirSync(this.root).filter(
      (name) => !["data", "report.md", "result.json"].includes(name),
    );
    if (unexpected.length) throw new Error("unexpected run-root entries: " + unexpected.join(", "));
    if (validator) validator(result);
    writeJson(resultPath, result);
  }

  /** Write the standard report and result assembled from common fields. */
  writeCase({
    track,
    status,
    detail,
    executionCommand,
    startedAtUtc,
    checks = null,
    limitations = [],
    notes = "",
    extra = {},
    validator = null,
  }: {
    track: string;
    status: Status;
    detail: string;
    executionCommand: string;
    startedAtUtc: string;
    checks?: CheckRecord[] | null;
    limitations?: string[];
    notes?: string;
    extra?: Record<string, unknown>;
    validator?: ((result: Record<string, any>) => void) | null;
  }) {
    let report =
      "# " +
      this.caseName +
      " run\n\nStatus: " +
      status +
      "\n\n" +
      detail +
      "\n\nCommand: " +
      executionCommand +
      "\n\n";
    if (checks !== null)
      report +=
        "## Checks\n\n" +
        (checks.length
          ? checks
              .map((item) => "- " + (item.passed ? "PASS" : "FAIL") + ": " + item.name + "\n")
              .join("")
          : "- No assertions completed.\n") +
        "\n";
    if (notes) report += notes.trimEnd() + "\n";
    this.writeReport(report);
    const result: Record<string, any> = {
      track,
      status,
      detail,
      execution_command: executionCommand,
      started_at_utc: startedAtUtc,
      ended_at_utc: utcNow(),
      environment: { os: os.type(), arch: os.arch(), node: process.version },
      limitations,
      ...(checks === null ? {} : { checks }),
    };
    const reserved = new Set([...Object.keys(result), "case_name", "run_id", "artifacts"]);
    const overlap = Object.keys(extra).filter((key) => reserved.has(key));
    if (overlap.length) throw new Error("extra result fields replace managed fields: " + overlap.join(", "));
    this.writeResult({ ...result, ...extra }, validator);
  }
}
