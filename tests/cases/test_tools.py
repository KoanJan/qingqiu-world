"""Verify shared report, HTTP, and isolation mechanics used by case runners."""

import io
import json
from pathlib import Path
import platform
import tempfile
import unittest
from unittest.mock import patch

from tools import (CheckFailure, HttpShapeError, HttpStatusError, HttpTransportError,
                   RunRecord, SetupError, build_server, call_json, check,
                   http_json, isolated_build_env, isolated_server_env,
                   provider_endpoint, start_server, stop_server)


class RunRecordTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.case_dir = Path(self.temp.name) / "D" / "example-case"
        self.case_dir.mkdir(parents=True)

    @staticmethod
    def result():
        return {
            "track": "deterministic", "status": "PASS", "detail": "Checks passed",
            "execution_command": "python3 scripts/run.py",
            "started_at_utc": "2026-01-01T00:00:00+00:00",
            "ended_at_utc": "2026-01-01T00:00:01+00:00",
            "tested_revision": {"head": "test"}, "limitations": [],
        }

    def test_isolated_layout_and_generated_evidence_index(self):
        first = RunRecord(self.case_dir)
        second = RunRecord(self.case_dir)
        self.assertNotEqual(first.root, second.root)
        first.write_data_json("observed.json", {"value": 1})
        first.write_report("# Result\n\nStatus: PASS\n")
        first.write_result(self.result())
        result = json.loads((first.root / "result.json").read_text())
        self.assertEqual(result["case_name"], "example-case")
        self.assertEqual(result["run_id"], first.root.name)
        self.assertEqual(result["artifacts"], ["report.md", "data/observed.json"])
        self.assertEqual({path.name for path in first.root.iterdir()},
                         {"data", "report.md", "result.json"})
        self.assertEqual(list(second.data_dir.iterdir()), [])

    def test_rejects_output_outside_contract(self):
        record = RunRecord(self.case_dir)
        with self.assertRaises(ValueError):
            record.write_data_json("../escape.json", {})
        record.write_report("# Result\n\nStatus: PASS\n")
        (record.root / "stray.log").write_text("stray")
        with self.assertRaisesRegex(ValueError, "unexpected run-root"):
            record.write_result(self.result())
        self.assertFalse((record.root / "result.json").exists())

    def test_case_validator_runs_before_result_is_written(self):
        record = RunRecord(self.case_dir)
        record.write_report("# Result\n\nStatus: PASS\n")

        def reject(_result):
            raise ValueError("case criterion failed")

        with self.assertRaisesRegex(ValueError, "case criterion"):
            record.write_result(self.result(), reject)
        self.assertFalse((record.root / "result.json").exists())

    def test_write_case_assembles_standard_report_and_result(self):
        record = RunRecord(self.case_dir)
        checks = [{"name": "sample", "passed": True, "observed": 1, "expected": 1}]
        record.write_data_json("observed.json", {"value": 1})
        record.write_case(
            track="deterministic", status="PASS", detail="Checks passed",
            execution_command="python3 scripts/run.py",
            started_at_utc="2026-01-01T00:00:00+00:00",
            tested_revision={"head": "abc"}, checks=checks,
            notes="Inspect `data/observed.json`.")
        result = json.loads((record.root / "result.json").read_text())
        self.assertEqual(result["track"], "deterministic")
        self.assertEqual(result["checks"], checks)
        self.assertEqual(result["environment"]["python"], platform.python_version())
        report = (record.root / "report.md").read_text()
        self.assertIn("## Checks", report)
        self.assertIn("- PASS: sample", report)
        self.assertIn("Inspect `data/observed.json`.", report)

    def test_rejects_mismatched_identity_and_external_evidence(self):
        record = RunRecord(self.case_dir)
        record.write_report("# Result\n\nStatus: PASS\n")
        with self.assertRaisesRegex(ValueError, "case_name"):
            record.write_result({**self.result(), "case_name": "another-case"})
        outside = Path(self.temp.name) / "outside.json"
        outside.write_text("{}")
        (record.data_dir / "outside.json").symlink_to(outside)
        with self.assertRaisesRegex(ValueError, "evidence files must belong"):
            record.write_result(self.result())
        self.assertFalse((record.root / "result.json").exists())

    def test_result_write_rejects_missing_duplicate_or_stale_report_status(self):
        record = RunRecord(self.case_dir)
        for content in ("# Result\n", "Status: PASS\nStatus: PASS\n",
                        "Status: INCONCLUSIVE\n"):
            with self.subTest(content=content):
                record.write_report(content)
                with self.assertRaisesRegex(ValueError, "report.md status must match"):
                    record.write_result(self.result())
                self.assertFalse((record.root / "result.json").exists())


class HttpAndIsolationTests(unittest.TestCase):
    def test_json_and_raw_payloads_return_full_envelope(self):
        class Response(io.BytesIO):
            status = 200

        with patch("tools.urllib.request.urlopen", return_value=Response(b'{"code":0,"data":{}}')) as send:
            self.assertEqual(http_json("http://local", "PUT", "/profile", {"name": "青丘"},
                                       expected_status=200), {"code": 0, "data": {}})
            request = send.call_args.args[0]
            self.assertEqual(json.loads(request.data), {"name": "青丘"})
            self.assertEqual(request.get_header("Content-type"), "application/json")
        with patch("tools.urllib.request.urlopen", return_value=Response(b'{"code":0}')) as send:
            http_json("http://local", "POST", "/upload", b"raw", content_type="text/plain")
            request = send.call_args.args[0]
            self.assertEqual(request.data, b"raw")
            self.assertEqual(request.get_header("Content-type"), "text/plain")

    def test_http_failures_have_distinct_types(self):
        class Response(io.BytesIO):
            status = 201

        with patch("tools.urllib.request.urlopen", return_value=Response(b"{}")):
            with self.assertRaises(HttpStatusError):
                http_json("http://local", "GET", "/item", expected_status=200)
        with patch("tools.urllib.request.urlopen", return_value=Response(b"not JSON")):
            with self.assertRaises(HttpTransportError):
                http_json("http://local", "GET", "/item")
        with patch("tools.urllib.request.urlopen", return_value=Response(b"[]")):
            with self.assertRaises(HttpShapeError):
                http_json("http://local", "GET", "/item")

    def test_isolated_environments_do_not_override_owned_paths(self):
        with tempfile.TemporaryDirectory() as temp:
            data_dir = Path(temp) / "data"
            data_dir.mkdir()
            build = isolated_build_env(data_dir)
            server = isolated_server_env(data_dir, 12345, extra={"CASE_LIMIT": "4"})
            self.assertEqual(build["GOCACHE"], str(data_dir / "go-cache"))
            self.assertEqual(server["DATA_ROOT"], str(data_dir / "product"))
            self.assertEqual(server["PORT"], "12345")
            self.assertEqual(server["CASE_LIMIT"], "4")
            self.assertTrue((data_dir / "tmp").is_dir())
            self.assertTrue((data_dir / "logs").is_dir())
            with self.assertRaisesRegex(ValueError, "overrides isolation"):
                isolated_server_env(data_dir, 12345, extra={"DATA_ROOT": "/outside"})

    def test_provider_url_is_fingerprinted_without_storing_credentials(self):
        endpoint = provider_endpoint("https://secret@example.test/v1")
        self.assertEqual(endpoint["host"], "example.test")
        self.assertNotIn("secret", str(endpoint))


class ExecutionHelperTests(unittest.TestCase):
    def test_check_records_comparisons_and_stops_on_mismatch(self):
        checks = []
        check(checks, "same", 1, 1)
        self.assertEqual(checks, [{"name": "same", "passed": True, "observed": 1,
                                   "expected": 1}])
        with self.assertRaisesRegex(CheckFailure, "different"):
            check(checks, "different", 2, 1)
        self.assertFalse(checks[-1]["passed"])

    def test_call_json_keeps_request_and_response_evidence(self):
        class Response(io.BytesIO):
            status = 200

        responses = []
        with patch("tools.urllib.request.urlopen", return_value=Response(b'{"code":0}')):
            envelope = call_json("http://local", "PUT", "/profile", {"name": "青丘"},
                                 responses=responses)
        self.assertEqual(envelope, {"code": 0})
        self.assertEqual(responses, [{"method": "PUT", "path": "/profile",
                                      "request": {"name": "青丘"},
                                      "response": {"code": 0}}])

    def test_build_server_uses_isolated_go_state_and_reports_failure(self):
        with tempfile.TemporaryDirectory() as temp:
            data_dir = Path(temp) / "data"
            data_dir.mkdir()
            completed = type("Completed", (), {"returncode": 1})()
            with patch("tools.subprocess.run", return_value=completed) as run:
                with self.assertRaisesRegex(SetupError, "build failed"):
                    build_server(Path(temp), data_dir)
            command = run.call_args.args[0]
            self.assertEqual(command[:3], ["go", "build", "-o"])
            self.assertEqual(run.call_args.kwargs["cwd"], Path(temp) / "server")
            self.assertEqual(run.call_args.kwargs["env"]["GOCACHE"],
                             str(data_dir / "go-cache"))
            self.assertTrue((data_dir / "build.log").is_file())

    def test_start_server_waits_for_readiness_and_stop_returns_exit_code(self):
        class Process:
            returncode = 0

            def poll(self):
                return None

            def terminate(self):
                pass

            def wait(self, timeout=None):
                return 0

        with tempfile.TemporaryDirectory() as temp:
            data_dir = Path(temp)
            process = Process()
            with patch("tools.subprocess.Popen", return_value=process), \
                    patch("tools.http_json",
                          side_effect=[HttpTransportError("not ready"), {"code": 0}]):
                started = start_server(data_dir / "server", data_dir, {}, "http://local",
                                       timeout=1)
            self.assertIs(started, process)
            self.assertTrue((data_dir / "server.log").is_file())
            self.assertEqual(stop_server(process), 0)


if __name__ == "__main__":
    unittest.main()
