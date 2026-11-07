/** Offline contract checks for the chat-to-reminder and relay semantic cases. */
import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { test } from "node:test";
import { validateChatResult } from "./chat_contract.ts";
import { Result as ScheduleResult } from "./schedule-reminder-from-chat/scripts/case_contract.ts";
import { Result as RelayResult } from "./relay-message-to-person/scripts/case_contract.ts";

const S_DIR = path.dirname(fileURLToPath(import.meta.url));

for (const name of ["schedule-reminder-from-chat", "relay-message-to-person"] as const) {
  test(`${name}: final PASS requires matching capture, report, and evidence`, () => {
    const caseDir = path.join(S_DIR, name);
    const scenario = JSON.parse(readFileSync(path.join(caseDir, "scenario-v1.json"), "utf8"));
    const runDir = mkdtempSync(path.join(os.tmpdir(), `qingqiu-${name}-`));
    const dataDir = path.join(runDir, "data");
    mkdirSync(dataDir);
    const artifacts = ["data/metadata.json", "data/capture.json", "data/session.json", "data/db_evidence.json", "report.md"];
    try {
      for (const artifact of artifacts) {
        const target = path.join(runDir, artifact);
        writeFileSync(target, artifact === "report.md" ? "# Result\n\nStatus: PASS\n" : "evidence");
      }
      const capture: { status: string; mechanical_failures: string[]; mechanical_checks_performed: boolean; observed_model_calls: null } = {
        status: "INCONCLUSIVE", mechanical_failures: [], mechanical_checks_performed: true,
        observed_model_calls: null,
      };
      const writeCapture = () => writeFileSync(path.join(dataDir, "capture.json"), JSON.stringify(capture));
      writeCapture();
      const result = {
        schema_version: 1, run_id: path.basename(runDir), case_name: name,
        scenario_version: scenario.version, scenario_sha256: "0".repeat(64),
        track: "semantic", status: "PASS", detail: "All criteria supported",
        execution_command: `npm --prefix tests run case -- cases/S/${name}/scripts/run.ts`,
        started_at_utc: "2026-11-05T00:00:00Z", ended_at_utc: "2026-11-05T00:01:00Z",
        tested_revision: { head: "tested-sha", status: " M tested.ts", tracked_diff_sha256: "test" },
        finished_revision: { head: "later-sha", status: " M tested.ts", tracked_diff_sha256: "test" },
        request: scenario.request, subject_entry: "chat", model: { llm: "subject-model" },
        environment: { os: "TestOS", arch: "arm64", node: "26", isolated_data_root: true },
        limits: scenario.limits, observed_model_calls: null, mechanical_failures: [],
        mechanical_checks_performed: true,
        judge: { kind: "same_session_agent", model: "judge-model", judgement: "supported",
          rationale: "The captured behavior matches." },
        criteria: [...scenario.required, ...scenario.forbidden].map((item: { id: string }) => ({
          id: item.id, status: "PASS", observed: "checked",
          evidence: [{ artifact: "data/db_evidence.json", location: "row[0]" }],
        })),
        artifacts, limitations: ["Judgment was formed in the capture session."],
        ...(name === "relay-message-to-person" ? { target_person_name: scenario.target_person_name } : {}),
      };
      const validate = (value: unknown) => name === "relay-message-to-person"
        ? validateChatResult(value, runDir, scenario, caseDir, RelayResult)
        : validateChatResult(value, runDir, scenario, caseDir, ScheduleResult);
      assert.doesNotThrow(() => validate(result));
      writeFileSync(path.join(runDir, "report.md"), "Status: INCONCLUSIVE\n");
      assert.throws(() => validate(result), /report.md status must match/);
      writeFileSync(path.join(runDir, "report.md"), "Status: PASS\n");
      assert.throws(() => validate({ ...result, acceptance_owner: "reviewer" }));
      capture.status = "FAIL";
      capture.mechanical_failures.push("missing evidence");
      writeCapture();
      assert.throws(() => validate(result), /judge changed captured field/);
      capture.status = "INCONCLUSIVE";
      capture.mechanical_failures = [];
      writeCapture();
      const calibration = ["supported", "contradicted", "insufficient_evidence"].map((label) => ({
        claim: label, expected: label, observed: label,
      }));
      calibration[0].observed = "contradicted";
      writeFileSync(path.join(dataDir, "calibration.json"), JSON.stringify(calibration));
      assert.throws(() => validate({ ...result, artifacts: [...artifacts, "data/calibration.json"] }),
        /correct reference judgments/);
    } finally { rmSync(runDir, { recursive: true, force: true }); }
  });
}
