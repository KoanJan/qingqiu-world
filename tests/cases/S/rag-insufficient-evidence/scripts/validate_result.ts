/** Validate a semantic run's scenario identity and artifact-backed result. */
import { fileURLToPath } from "node:url";
import path from "node:path";
import { loadCaseContract, ResultBase, Scenario } from "../../rag_contract.ts";
import { resultCli } from "../../semantic_contract.ts";
const caseDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const contract = await loadCaseContract(caseDir);
process.exitCode = resultCli(
  caseDir,
  contract?.Scenario ?? Scenario,
  contract?.Result ?? ResultBase,
);
