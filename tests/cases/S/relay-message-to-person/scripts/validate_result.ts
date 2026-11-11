/** Validate this case's capture and final semantic result. */
import { fileURLToPath } from "node:url";
import path from "node:path";
import { chatResultCli } from "../../chat_contract.ts";
import { Result, Scenario } from "./case_contract.ts";

const caseDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
process.exitCode = chatResultCli(caseDir, Scenario, Result);
