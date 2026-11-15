/** Run this case through the shared semantic runner with the RAG workflow. */
import { fileURLToPath } from "node:url";
import path from "node:path";
import { ragWorkflow } from "../../rag_workflow.ts";
import { runSemanticCase } from "../../semantic_runner.ts";
// The case directory is the parent of scripts/; the shared runner reads
// scenario.json from there and owns the run directory and result.
const caseDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
process.exitCode = await runSemanticCase(ragWorkflow(caseDir));
