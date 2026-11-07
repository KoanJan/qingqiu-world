/** Run this case through the shared S-track semantic runner. */
import { fileURLToPath } from "node:url";
import path from "node:path";
import { run } from "../../semantic_runner.ts";
const caseDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
process.exitCode = await run(caseDir);
