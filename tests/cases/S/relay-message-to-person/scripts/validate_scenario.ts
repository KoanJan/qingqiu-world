/** Validate this case's versioned chat scenario. */
import { fileURLToPath } from "node:url";
import path from "node:path";
import { chatScenarioCli } from "../../chat_contract.ts";
import { Scenario } from "./case_contract.ts";

const caseDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
process.exitCode = chatScenarioCli(caseDir, Scenario);
