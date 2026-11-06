"""Regression checks for scripted RAG report and artifact boundaries.

Add a case using the shared RAG runner to RAG_CASE_NAMES; other S workflows
own contract tests for their own definition and evidence format.
"""

import copy
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from base_contracts import (ResultBase, load_case_contract, load_scenario,
                            validate_result, validate_scenario)
import rag_contract


CASES_ROOT = Path(__file__).resolve().parent
RAG_CASE_NAMES = ("rag-insufficient-evidence", "rag-source-order")
CASE_DIRS = [CASES_ROOT / name for name in RAG_CASE_NAMES]
sys.path.insert(0, str(CASES_ROOT))


def contract_test_case(case_dir: Path):
    """Build the contract test class for one semantic case directory."""
    case_dir = case_dir.resolve()

    class ContractTests(unittest.TestCase):
        def setUp(self):
            self.contract = load_case_contract(case_dir)
            self.scenario_model = getattr(self.contract, "Scenario", rag_contract.Scenario)
            self.result_model = getattr(self.contract, "Result", ResultBase)
            self.case, _ = load_scenario(case_dir, model=self.scenario_model)
            self.temp = tempfile.TemporaryDirectory()
            self.addCleanup(self.temp.cleanup)
            self.run_dir = Path(self.temp.name)
            self.result_validator = lambda value: validate_result(
                value, self.run_dir, self.case, case_dir, model=self.result_model)
            self.scenario_validator = lambda value, path: validate_scenario(
                value, path, case_dir, model=self.scenario_model)
            artifacts = ["data/metadata.json", "data/capture.json", "data/calibration.json",
                         "report.md",
                         "data/session.json", "data/db_evidence.json",
                         f"data/{Path(self.case['fixture']).name}"]
            for name in artifacts:
                path = self.run_dir / name
                path.parent.mkdir(exist_ok=True)
                path.write_text("evidence")
            fixture_bytes = (case_dir / self.case["fixture"]).read_bytes()
            (self.run_dir / "data" / Path(self.case["fixture"]).name).write_bytes(fixture_bytes)
            self.write_calibration()
            self.write_capture()
            self.write_report("PASS")
            self.result = {
                "schema_version": 1, "run_id": self.run_dir.name, "case_name": case_dir.name,
                "scenario_version": self.case["version"],
                "scenario_sha256": hashlib.sha256(
                    (case_dir / "scenario-v1.json").read_bytes()).hexdigest(),
                "track": "semantic", "status": "PASS", "detail": "All criteria supported",
                "execution_command": "python3 scripts/run.py",
                "started_at_utc": "2026-11-05T00:00:00Z", "ended_at_utc": "2026-11-05T00:01:00Z",
                "question": self.case["question"],
                "subject_entry": rag_contract.SUBJECT_ENTRY,
                "model": {"llm": "subject-model", "embedding": "embedding-model"},
                "environment": {"os": "TestOS", "arch": "arm64", "python": "3.14",
                                "isolated_data_root": True},
                "limits": self.case["limits"],
                "fixture_sha256": hashlib.sha256(fixture_bytes).hexdigest(),
                "tested_revision": {"head": "test-sha", "status": "",
                                    "tracked_diff_sha256": "test"},
                "finished_revision": {"head": "test-sha", "status": "",
                                     "tracked_diff_sha256": "test"},
                "mechanical_failures": [], "observed_model_calls": 2,
                "mechanical_checks_performed": True,
                "judge": {"kind": "independent_agent", "model": "judge-model",
                          "judgement": "supported",
                          "rationale": "Original admission order supports the claim."},
                "criteria": [{"id": item["id"], "status": "PASS", "observed": "checked",
                              "evidence": [{"artifact": "data/db_evidence.json",
                                            "location": "active_chunks[0]"}]}
                             for item in self.case["required"] + self.case["forbidden"]],
                "artifacts": artifacts, "limitations": [],
            }

        def write_capture(self, **overrides):
            """Write the runner capture record that judgment may not rewrite."""
            capture = {"status": "INCONCLUSIVE", "mechanical_failures": [],
                       "mechanical_checks_performed": True, "observed_model_calls": 2}
            capture.update(overrides)
            (self.run_dir / "data" / "capture.json").write_text(json.dumps(capture))

        def write_calibration(self):
            calibration = [{"claim": f"reference claim {label}", "expected": label,
                            "observed": label}
                           for label in ("supported", "contradicted", "insufficient_evidence")]
            (self.run_dir / "data/calibration.json").write_text(json.dumps(calibration))

        def write_report(self, status):
            (self.run_dir / "report.md").write_text(f"# Result\n\nStatus: {status}\n")

        def test_complete_result_passes(self):
            self.result_validator(self.result)

        def test_final_cli_rejects_stale_report_status(self):
            self.write_report("INCONCLUSIVE")
            (self.run_dir / "result.json").write_text(json.dumps(self.result))
            command = [sys.executable, str(case_dir / "scripts/validate_result.py"),
                       str(self.run_dir)]
            completed = subprocess.run(command, capture_output=True, text=True)
            self.assertEqual(completed.returncode, 2, completed.stdout)
            self.assertIn("report.md status must match result.json status", completed.stderr)

        def test_delivery_policy_fields_are_not_result_fields(self):
            result = copy.deepcopy(self.result)
            result["acceptance_owner"] = "someone"
            with self.assertRaisesRegex(ValueError, "Extra inputs are not permitted"):
                self.result_validator(result)

        def test_pass_does_not_require_clean_or_matching_worktrees(self):
            result = copy.deepcopy(self.result)
            result["tested_revision"]["status"] = " M tested.py"
            result["finished_revision"]["head"] = "later-sha"
            self.result_validator(result)
            (self.run_dir / "result.json").write_text(json.dumps(result))
            command = [sys.executable, str(case_dir / "scripts/validate_result.py"),
                       str(self.run_dir)]
            completed = subprocess.run(command, capture_output=True, text=True)
            self.assertEqual(completed.returncode, 0, completed.stderr)
            self.assertIn("FINAL VALID", completed.stdout)

        def test_same_session_judgment_can_pass(self):
            result = copy.deepcopy(self.result)
            result["judge"]["kind"] = "same_session_agent"
            result["limitations"].append("Judgment was formed in the capture session.")
            self.result_validator(result)

        def test_capture_scaffold_is_distinct_from_final_result(self):
            result = copy.deepcopy(self.result)
            result["status"] = "INCONCLUSIVE"
            result["judge"] = None
            self.write_report("INCONCLUSIVE")
            for item in result["criteria"]:
                item.update(status="INCONCLUSIVE", observed="", evidence=[])
            validate_result(result, self.run_dir, self.case, case_dir,
                            model=self.result_model, final=False)
            with self.assertRaisesRegex(ValueError, "completed semantic judgment"):
                self.result_validator(result)

        def test_mechanical_fail_scaffold_needs_judgment_to_be_final(self):
            result = copy.deepcopy(self.result)
            result["status"] = "FAIL"
            result["mechanical_failures"] = ["record: no KB retrieval trace"]
            result["judge"] = None
            self.write_report("FAIL")
            self.write_capture(status="FAIL", mechanical_failures=result["mechanical_failures"])
            validate_result(result, self.run_dir, self.case, case_dir,
                            model=self.result_model, final=False)
            with self.assertRaisesRegex(ValueError, "completed semantic judgment"):
                self.result_validator(result)

        def test_result_cli_distinguishes_capture_from_final(self):
            result = copy.deepcopy(self.result)
            result["status"] = "FAIL"
            result["mechanical_failures"] = ["record: no KB retrieval trace"]
            result["judge"] = None
            self.write_report("FAIL")
            for item in result["criteria"]:
                item.update(status="INCONCLUSIVE", observed="", evidence=[])
            self.write_capture(status="FAIL", mechanical_failures=result["mechanical_failures"])
            (self.run_dir / "result.json").write_text(json.dumps(result))
            command = [sys.executable, str(case_dir / "scripts/validate_result.py")]
            capture = subprocess.run(command + ["--capture", str(self.run_dir)],
                                     capture_output=True, text=True)
            self.assertEqual(capture.returncode, 1, capture.stderr)
            self.assertIn("CAPTURE VALID", capture.stdout)
            final = subprocess.run(command + [str(self.run_dir)],
                                   capture_output=True, text=True)
            self.assertEqual(final.returncode, 2, final.stdout)
            self.assertIn("final result requires a completed semantic judgment", final.stderr)
            result["judge"] = {"kind": "independent_agent", "model": "judge-model",
                               "judgement": "contradicted",
                               "rationale": "The recorded retrieval trace is absent."}
            for item in result["criteria"]:
                item.update(status="PASS", observed="Checked the captured session and database evidence.",
                            evidence=[{"artifact": "data/db_evidence.json",
                                       "location": "kb_usage_traces"}])
            result["criteria"][0]["status"] = "FAIL"
            (self.run_dir / "result.json").write_text(json.dumps(result))
            final = subprocess.run(command + [str(self.run_dir)],
                                   capture_output=True, text=True)
            self.assertEqual(final.returncode, 1, final.stderr)
            self.assertIn("FINAL VALID", final.stdout)

        def test_mechanical_failure_cannot_be_softened(self):
            result = copy.deepcopy(self.result)
            result["mechanical_failures"] = ["record: no KB retrieval trace"]
            result["status"] = "INCONCLUSIVE"
            with self.assertRaisesRegex(ValueError, "mechanical failures require FAIL status"):
                self.result_validator(result)
            result["status"] = "FAIL"
            self.write_capture(status="FAIL", mechanical_failures=result["mechanical_failures"])
            self.write_report("FAIL")
            self.result_validator(result)

        def test_capture_record_cannot_be_rewritten(self):
            self.write_capture(status="FAIL", mechanical_failures=["record: no KB retrieval trace"])
            with self.assertRaisesRegex(ValueError, "judge changed captured field"):
                self.result_validator(self.result)

        def test_missing_criterion_evidence_cannot_pass(self):
            result = copy.deepcopy(self.result)
            result["criteria"][0]["evidence"] = []
            with self.assertRaisesRegex(ValueError, "evidence for every criterion"):
                self.result_validator(result)

        def test_inconclusive_judgement_cannot_pass(self):
            result = copy.deepcopy(self.result)
            result["judge"]["judgement"] = "insufficient_evidence"
            with self.assertRaisesRegex(ValueError, "explained, supported semantic judgment"):
                self.result_validator(result)

        def test_artifact_escape_cannot_pass(self):
            result = copy.deepcopy(self.result)
            result["artifacts"].append("../outside.txt")
            with self.assertRaisesRegex(ValueError, "outside this run"):
                self.result_validator(result)

        def test_run_data_cannot_be_scattered_at_top_level(self):
            (self.run_dir / "unexpected.log").write_text("stray")
            with self.assertRaisesRegex(ValueError, "run root must contain only"):
                self.result_validator(self.result)

        def test_artifact_cannot_escape_data_directory(self):
            result = copy.deepcopy(self.result)
            result["artifacts"].append("data/../report.md")
            with self.assertRaisesRegex(ValueError, "artifact outside data directory"):
                self.result_validator(result)

        def test_calibration_is_optional_for_pass(self):
            result = copy.deepcopy(self.result)
            result["artifacts"].remove("data/calibration.json")
            (self.run_dir / "data/calibration.json").unlink()
            self.result_validator(result)

        def test_recorded_bad_calibration_cannot_support_pass(self):
            result = copy.deepcopy(self.result)
            self.write_calibration()
            calibration_path = self.run_dir / "data/calibration.json"
            calibration = json.loads(calibration_path.read_text())
            calibration[0]["observed"] = "contradicted"
            calibration_path.write_text(json.dumps(calibration))
            with self.assertRaisesRegex(ValueError, "correct reference judgments"):
                self.result_validator(result)

        def test_work_interaction_count_is_not_compared_to_phase_limit(self):
            result = copy.deepcopy(self.result)
            result["observed_model_calls"] = self.case["limits"]["max_model_calls"] + 1
            self.write_capture(observed_model_calls=result["observed_model_calls"])
            self.result_validator(result)

        def test_pass_requires_observed_work_interactions(self):
            result = copy.deepcopy(self.result)
            result["observed_model_calls"] = None
            self.write_capture(observed_model_calls=None)
            with self.assertRaisesRegex(ValueError, "observed work-model interaction count"):
                self.result_validator(result)

        def test_captured_fixture_must_match_result_checksum(self):
            (self.run_dir / "data" / Path(self.case["fixture"]).name).write_text("tampered")
            with self.assertRaisesRegex(ValueError, "captured fixture hash mismatch"):
                self.result_validator(self.result)

        def test_fixture_must_belong_to_its_case(self):
            case = copy.deepcopy(self.case)
            case["fixture"] = "../../README.md"
            with self.assertRaisesRegex(ValueError, "outside its case directory"):
                self.scenario_validator(case, case_dir / "scenario-v1.json")

        def test_scenario_rejects_unused_fields(self):
            case = copy.deepcopy(self.case)
            case["source_facts"] = "duplicated fixture summary"
            with self.assertRaisesRegex(ValueError, "scenario fields must be exactly"):
                self.scenario_validator(case, case_dir / "scenario-v1.json")

        def test_proposed_case_file_can_be_validated_before_activation(self):
            case = copy.deepcopy(self.case)
            case["lifecycle"] = "proposed"
            self.scenario_validator(case, case_dir / "scenario-v1.json")
            command = [sys.executable, str(case_dir / "scripts/validate_scenario.py"),
                       "--file", str(case_dir / "scenario-v1.json")]
            completed = subprocess.run(command, capture_output=True, text=True)
            self.assertEqual(completed.returncode, 0, completed.stderr)
            self.assertIn(f"{case_dir.name} v1 active", completed.stdout)

    return ContractTests


for case_dir in CASE_DIRS:
    globals()[f"ContractTests_{case_dir.name.replace('-', '_')}"] = contract_test_case(case_dir)


if __name__ == "__main__":
    unittest.main()
