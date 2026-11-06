"""Offline result-contract checks for the chat-to-reminder semantic case."""

import copy
import json
from pathlib import Path
import sys
import tempfile
import unittest

from base_contracts import validate_result


CASE_DIR = Path(__file__).resolve().parent / "schedule-reminder-from-chat"
sys.path.insert(0, str(CASE_DIR / "scripts"))
from case_contract import Result  # noqa: E402


class ScheduleResultContractTests(unittest.TestCase):
    def setUp(self):
        self.case = json.loads((CASE_DIR / "scenario-v1.json").read_text())
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.run_dir = Path(temporary.name)
        artifacts = ["data/metadata.json", "data/capture.json", "data/session.json",
                     "data/db_evidence.json", "report.md"]
        for name in artifacts:
            path = self.run_dir / name
            path.parent.mkdir(exist_ok=True)
            path.write_text("evidence")
        (self.run_dir / "report.md").write_text("# Result\n\nStatus: PASS\n")
        self.write_capture()
        self.result = {
            "schema_version": 1, "run_id": self.run_dir.name,
            "case_name": CASE_DIR.name, "scenario_version": self.case["version"],
            "scenario_sha256": "0" * 64, "track": "semantic", "status": "PASS",
            "detail": "All criteria supported", "execution_command": "python3 scripts/run.py",
            "started_at_utc": "2026-11-05T00:00:00Z",
            "ended_at_utc": "2026-11-05T00:01:00Z",
            "tested_revision": {"head": "tested-sha", "status": " M tested.py",
                                "tracked_diff_sha256": "test"},
            "finished_revision": {"head": "later-sha", "status": " M tested.py",
                                  "tracked_diff_sha256": "test"},
            "request": self.case["request"], "subject_entry": "chat",
            "model": {"llm": "subject-model"},
            "environment": {"os": "TestOS", "arch": "arm64", "python": "3.14",
                            "isolated_data_root": True},
            "limits": self.case["limits"], "observed_model_calls": None,
            "mechanical_failures": [], "mechanical_checks_performed": True,
            "judge": {"kind": "same_session_agent", "model": "judge-model",
                      "judgement": "supported", "rationale": "The captured reminder matches."},
            "criteria": [{"id": item["id"], "status": "PASS", "observed": "checked",
                          "evidence": [{"artifact": "data/db_evidence.json",
                                        "location": "scheduled_events[0]"}]}
                         for item in self.case["required"] + self.case["forbidden"]],
            "artifacts": artifacts,
            "limitations": ["Judgment was formed in the capture session."],
        }

    def write_capture(self, **overrides):
        capture = {"status": "INCONCLUSIVE", "mechanical_failures": [],
                   "mechanical_checks_performed": True, "observed_model_calls": None}
        capture.update(overrides)
        (self.run_dir / "data/capture.json").write_text(json.dumps(capture))

    def validate(self, result):
        return validate_result(result, self.run_dir, self.case, CASE_DIR, model=Result)

    def test_evidence_based_pass_needs_no_delivery_fields_or_clean_worktree(self):
        self.validate(self.result)

    def test_stale_report_status_cannot_validate(self):
        (self.run_dir / "report.md").write_text("Status: INCONCLUSIVE\n")
        with self.assertRaisesRegex(ValueError, "report.md status must match"):
            self.validate(self.result)

    def test_delivery_owner_is_not_a_result_field(self):
        result = copy.deepcopy(self.result)
        result["acceptance_owner"] = "reviewer"
        with self.assertRaisesRegex(ValueError, "Extra inputs are not permitted"):
            self.validate(result)

    def test_mechanical_failure_cannot_become_pass(self):
        self.write_capture(status="FAIL", mechanical_failures=["missing reminder"])
        with self.assertRaisesRegex(ValueError, "judge changed captured field"):
            self.validate(self.result)

    def test_unreliable_recorded_calibration_cannot_support_pass(self):
        result = copy.deepcopy(self.result)
        calibration = [{"claim": label, "expected": label, "observed": label}
                       for label in ("supported", "contradicted", "insufficient_evidence")]
        calibration[0]["observed"] = "contradicted"
        (self.run_dir / "data/calibration.json").write_text(json.dumps(calibration))
        result["artifacts"].append("data/calibration.json")
        with self.assertRaisesRegex(ValueError, "correct reference judgments"):
            self.validate(result)


if __name__ == "__main__":
    unittest.main()
