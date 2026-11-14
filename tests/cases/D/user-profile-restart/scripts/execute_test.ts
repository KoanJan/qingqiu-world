/** Verify the prepared profile after the owned server restarts. */
import path from "node:path";
import { callJson, check, readSqlite } from "../../../tools.ts";
import type { CheckRecord, HttpCall, RunRecord } from "../../../tools.ts";
import { NAME, PERSON_TYPE_HUMAN, UPDATED_BIO, USER_PROFILE_PATH } from "./prepare_data.ts";

export async function executeTest(base: string, record: RunRecord, responses: HttpCall[],
  checks: CheckRecord[]) {
  // Read the persisted row first: the restart check compares the API response
  // against what SQLite actually kept.
  const [profiles] = await readSqlite(path.join(record.dataDir, "product", "db", "database.db"), [
    { sql: "SELECT id, name, bio, type FROM persons WHERE type = ?", params: [PERSON_TYPE_HUMAN] },
  ]);
  const rows = profiles!.map((row) => [row.id, row.name, row.bio, row.type]);
  record.writeDataJson("db_state.json", rows);
  check(checks, "exactly one persisted human profile", rows.length, 1);
  const personId = rows[0]![0];
  // The API must expose the same identity and updated bio after the restart.
  const restored = await callJson(base, "GET", USER_PROFILE_PATH, null, responses);
  check(checks, "read after restart", restored.code, 0);
  for (const [field, expected] of [
    ["id", personId], ["name", NAME], ["bio", UPDATED_BIO], ["type", PERSON_TYPE_HUMAN],
  ]) check(checks, "persisted " + field, restored.data?.[field], expected);
  check(checks, "persisted database profile", rows, [[personId, NAME, UPDATED_BIO, PERSON_TYPE_HUMAN]]);
}
