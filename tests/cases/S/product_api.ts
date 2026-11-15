/**
 * Read/write access to an isolated product instance for semantic cases.
 *
 * This module owns the product API transport and read-only evidence queries.
 * The semantic runner lifecycle does not depend on it, and both RAG and chat
 * workflows use the same request and database helpers instead of copies.
 */
import { existsSync, readFileSync } from "node:fs";
import path from "node:path";
import { HttpRequestError, httpJson, readSqlite } from "../tools.ts";
import type { RunRecord } from "../tools.ts";

export type DbRow = Record<string, any>;
export type DbEvidence = Record<string, DbRow[]>;

/** A prerequisite or real product operation failed. */
export class ScenarioError extends Error {}

/** Return product data only when the API business code succeeds. */
export async function request<T = DbRow>(
  base: string,
  method: string,
  route: string,
  payload: unknown = null,
  contentType: string | null = null,
): Promise<T> {
  let response;
  try {
    response = await httpJson(base, method, route, payload, { contentType });
  } catch (error) {
    if (error instanceof HttpRequestError) throw new ScenarioError(error.message);
    throw error;
  }
  if (response.code !== 0)
    throw new ScenarioError(`${method} ${route}: business code ${response.code}: ${response.message}`);
  return response.data as T;
}

/** Read committed SQLite rows through a read-only connection. */
export async function readRows(
  dbPath: string,
  sql: string,
  params: Array<string | number> = [],
): Promise<DbRow[]> {
  const [rows] = await readSqlite(dbPath, [{ sql, params }]);
  return rows!;
}

/** Read the session artifact a case workflow wrote into its own run directory. */
export function readSessionArtifact(record: RunRecord): DbRow | null {
  const file = path.join(record.dataDir, "session.json");
  if (!existsSync(file)) return null;
  return JSON.parse(readFileSync(file, "utf8")) as DbRow;
}

/** Resolve one prepared Person from the case's isolated product database. */
export async function personIdByName(dbPath: string, name: string, type: number): Promise<number> {
  // Ambiguous setup is a scenario error, never a guessed ID.
  const rows = await readRows(dbPath, "SELECT id FROM persons WHERE name = ? AND type = ?", [name, type]);
  if (rows.length !== 1)
    throw new ScenarioError(`expected one prepared Person named ${name}; found ${rows.length}`);
  return rows[0]!.id;
}

/** Capture full rows of the declared evidence tables. */
export async function readTables(dbPath: string, tables: string[]): Promise<DbEvidence> {
  // Table names are identifiers, not values, so they cannot be parameterized;
  // restrict them to the lowercase identifier shape declared by case contracts.
  for (const table of tables) {
    if (!/^[a-z_]+$/.test(table)) throw new Error("invalid evidence table name");
  }
  const rows = await readSqlite(dbPath, tables.map((table) => ({ sql: `SELECT * FROM ${table} ORDER BY id` })));
  return Object.fromEntries(tables.map((table, index) => [table, rows[index]!]));
}
