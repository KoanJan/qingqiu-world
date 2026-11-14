/** Validate a committed scenario and its exact fixture before execution. */
import { fileURLToPath } from "node:url";
import path from "node:path";
import { loadCaseContract, Scenario } from "../../rag_contract.ts";
import { scenarioCli } from "../../semantic_contract.ts";
const caseDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const contract = await loadCaseContract(caseDir);
process.exitCode = scenarioCli(caseDir, contract?.Scenario ?? Scenario);
