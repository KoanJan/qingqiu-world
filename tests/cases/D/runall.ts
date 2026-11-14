/** Run every deterministic cross-component case and report each exit status. */
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { existsSync, readdirSync } from "node:fs";
import path from "node:path";

const trackDir = path.dirname(fileURLToPath(import.meta.url));
const testsDir = path.resolve(trackDir, "../..");
// Every directory under D/ is a case; a missing runner is reported, not skipped.
const cases = readdirSync(trackDir, { withFileTypes: true })
  .filter((item) => item.isDirectory() && item.name !== "__pycache__")
  .map((item) => item.name)
  .sort();
if (!cases.length) {
  console.error("D: no case directories found");
  process.exitCode = 1;
} else {
  const results = [];
  for (const name of cases) {
    const runner = path.join(trackDir, name, "scripts", "run.ts");
    if (!existsSync(runner)) {
      console.error(name + ": ERROR: missing scripts/run.ts");
      results.push([name, "missing runner"]);
      continue;
    }
    console.log("\n=== D/" + name + " ===");
    // Each case owns its process and exit code; this orchestrator only relays
    // output and never shares state between runs.
    const completed = spawnSync(process.execPath, ["--import", "tsx", runner], {
      cwd: testsDir, stdio: "inherit",
    });
    results.push([name, completed.error ? "could not start" : "exit " + completed.status]);
  }
  console.log("\nD case results:");
  for (const [name, result] of results) console.log("  " + name + ": " + result);
  process.exitCode = results.every(([, result]) => result === "exit 0") ? 0 : 1;
}
