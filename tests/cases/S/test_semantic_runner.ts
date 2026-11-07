/** Check that RAG capture distinguishes missing traces from empty retrievals. */
import assert from "node:assert/strict";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";
import { checkDbEvidence } from "./semantic_runner.ts";

test("missing retrieval trace differs from a completed lookup that returned no evidence", (t) => {
  const temp = mkdtempSync(path.join(os.tmpdir(), "qingqiu-semantic-"));
  t.after(() => rmSync(temp, { recursive: true, force: true }));
  const fixture = path.join(temp, "source.md");
  writeFileSync(fixture, "source passage");
  const session = {
    document: { id: 7, active_revision_id: 3 },
    kb_id: 1,
    agent_id: 2,
  };
  const evidence: Parameters<typeof checkDbEvidence>[0] = {
    document: { knowledge_base_id: 1, status: 2, active_revision_id: 3, file_path: fixture },
    kb_access: [{ person_id: 2, kb_id: 1 }],
    active_chunks: [{ id: 9 }],
    works: [],
    interactions: [],
    kb_usage_traces: [],
  };
  assert.deepEqual(checkDbEvidence(evidence, session, fixture), [
    "record: no persisted work records for the chat session",
    "record: no KB retrieval trace was recorded for the chat session",
  ]);

  evidence.works = [{ id: 4 }];
  evidence.kb_usage_traces = [
    {
      id: 5,
      authorized_kb_ids_json: JSON.stringify([1]),
      evidence_handles_json: "[]",
      returned_evidence_count: 0,
    },
  ];
  assert.deepEqual(checkDbEvidence(evidence, session, fixture), [
    "retrieval: KB retrieval returned no source evidence",
  ]);
  evidence.kb_usage_traces[0]!.returned_evidence_count = 1;
  assert.deepEqual(checkDbEvidence(evidence, session, fixture), []);
});
