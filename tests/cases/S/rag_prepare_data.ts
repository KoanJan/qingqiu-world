/** Shared product setup for the two RAG cases. */
import { randomUUID } from "node:crypto";
import { readFileSync } from "node:fs";
import path from "node:path";
import { sleep } from "../tools.ts";
import type { RunRecord, ServerStatus } from "../tools.ts";
import type { ScenarioData } from "./rag_contract.ts";
import { request, ScenarioError } from "./product_api.ts";
import type { DbRow } from "./product_api.ts";
import { DocumentStatus } from "../product_model.ts";
import { AgentRoute, KnowledgeBaseRoute } from "./product_routes.ts";

const INDEX_POLL_MS = 1_000;
/** A ready document exposes the first active revision. */
export const FIRST_REVISION_ID = 1;

/** Names of the product records this workflow creates; evidence checks reuse them. */
export const RAG_AGENT_NAME = "RAG source reader";
export const RAG_KB_NAME = "RAG source test KB";

/** Upload one source file as a hand-built multipart request. */
async function uploadDocument(base: string, kbId: number, fixture: string): Promise<DbRow> {
  // The shared HTTP helper sends JSON or raw bytes, so the multipart envelope
  // is assembled here to keep the upload identical to a real client request.
  const boundary = "qingqiu-" + randomUUID().replaceAll("-", "");
  const head = "--" + boundary +
    '\r\nContent-Disposition: form-data; name="file"; filename="' + path.basename(fixture) +
    '"\r\nContent-Type: text/markdown\r\n\r\n';
  const body = Buffer.concat([
    Buffer.from(head), readFileSync(fixture), Buffer.from("\r\n--" + boundary + "--\r\n"),
  ]);
  return request(base, "POST", KnowledgeBaseRoute.documents(kbId), body,
    "multipart/form-data; boundary=" + boundary);
}

async function waitReady(
  base: string, kbId: number, docId: number, child: ServerStatus, seconds: number,
): Promise<DbRow> {
  // Indexing is asynchronous; poll until a ready revision appears, and fail
  // fast when the server exits or processing reports failure.
  const deadline = Date.now() + seconds * 1000;
  while (Date.now() < deadline) {
    if (child.exitCode !== null || child.signalCode !== null)
      throw new ScenarioError("server exited during document indexing: " + (child.exitCode ?? child.signalCode));
    const doc = await request(base, "GET", KnowledgeBaseRoute.document(kbId, docId));
    if (doc.status === DocumentStatus.FAILED)
      throw new ScenarioError("document processing failed: " + (doc.error_message ?? ""));
    if (doc.status === DocumentStatus.READY && doc.active_revision_id >= FIRST_REVISION_ID) return doc;
    await sleep(INDEX_POLL_MS);
  }
  throw new ScenarioError("document did not reach an active ready revision within " + seconds + " seconds");
}

/** Create and index the source before the case sends its question. */
export async function prepareRagData(base: string, child: ServerStatus, scenario: ScenarioData,
  record: RunRecord, fixture: string, llmId: number): Promise<void> {
  // Create the Agent that will read the source, then the isolated KB.
  const agent = await request(base, "POST", AgentRoute.CREATE, {
    name: RAG_AGENT_NAME,
    description: "Answer from the supplied KB source",
    character_settings: "Use the KB source and explain uncertainty when it is incomplete.",
    llm_config_id: llmId,
  });
  const kb = await request(base, "POST", KnowledgeBaseRoute.CREATE, {
    name: RAG_KB_NAME, description: record.caseName,
  });
  // Upload and index before granting access, so the grant covers a ready source.
  const doc = await uploadDocument(base, kb.id, fixture);
  await waitReady(base, kb.id, doc.id, child, scenario.limits.index_timeout_seconds);
  await request(base, "POST", KnowledgeBaseRoute.access(kb.id), { person_id: agent.id });
  // Read the grant back: setup is complete only when the product exposes it.
  const access = await request<Record<string, any>[]>(base, "GET", KnowledgeBaseRoute.access(kb.id));
  if (!access.some((item) => item.id === agent.id))
    throw new ScenarioError("agent KB grant was not visible");
}
