"""Shared mechanics for cross-component test runners.

Case runners keep their product workflow, assertions, and verdict rules. This
module owns report layout, local process lifecycle and prerequisites, and HTTP
transport.
"""

import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import socket
import subprocess
import tempfile
import time
from datetime import datetime, timezone
from typing import Callable
import urllib.error
import urllib.parse
import urllib.request


class HttpRequestError(Exception):
    """A request did not return a usable JSON object."""


class HttpStatusError(HttpRequestError):
    """The server returned an unexpected HTTP status."""


class HttpTransportError(HttpRequestError):
    """The response could not be fetched or decoded as a JSON object."""


class HttpShapeError(HttpRequestError):
    """The server returned valid JSON with the wrong envelope shape."""


class SetupError(Exception):
    """The product run could not reach the behavior under test."""


class CheckFailure(Exception):
    """An observed product response contradicted the case contract."""


def free_port() -> int:
    """Choose an available loopback port for one owned server process."""
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def http_json(base: str, method: str, path: str, payload=None, *,
              content_type: str | None = None, timeout: float = 30,
              expected_status: int | None = None) -> dict:
    """Send JSON or raw bytes and return the full JSON response envelope.

    Business response codes remain the caller's responsibility. HTTP status
    and transport failures have separate types so cases can classify them.
    """
    data = None
    headers = {}
    if payload is not None:
        if content_type is None:
            data = json.dumps(payload, ensure_ascii=False).encode()
            headers["Content-Type"] = "application/json"
        else:
            if not isinstance(payload, bytes):
                raise TypeError("raw HTTP payload must be bytes")
            data = payload
            headers["Content-Type"] = content_type
    request = urllib.request.Request(base + path, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            if expected_status is not None and response.status != expected_status:
                raise HttpStatusError(
                    f"{method} {path} returned HTTP {response.status}; expected {expected_status}")
            envelope = json.load(response)
    except urllib.error.HTTPError as exc:
        raise HttpStatusError(f"{method} {path} returned HTTP {exc.code}") from exc
    except (urllib.error.URLError, TimeoutError, ValueError) as exc:
        raise HttpTransportError(f"{method} {path} could not return JSON: {exc}") from exc
    if not isinstance(envelope, dict):
        raise HttpShapeError(f"{method} {path} returned a non-object response")
    return envelope


def check(checks: list, label: str, observed, expected) -> None:
    """Record one exact comparison as evidence and stop on a mismatch."""
    passed = observed == expected
    checks.append({"name": label, "passed": passed, "observed": observed, "expected": expected})
    if not passed:
        raise CheckFailure(f"{label}: expected {expected!r}, observed {observed!r}")


def call_json(base: str, method: str, path: str, payload=None, *, responses: list,
              expected_status: int = 200, timeout: float = 10) -> dict:
    """Send one product request and preserve its request and response as evidence."""
    envelope = http_json(base, method, path, payload, timeout=timeout,
                         expected_status=expected_status)
    responses.append({"method": method, "path": path, "request": payload,
                      "response": envelope})
    return envelope


def provider_endpoint(url: str) -> dict:
    """Record a provider host and URL fingerprint without storing credentials."""
    parsed = urllib.parse.urlsplit(url)
    return {"host": parsed.hostname or "", "sha256": hashlib.sha256(url.encode()).hexdigest()}


def git_state(root: Path, *, include_diff: bool = False) -> dict:
    """Record the tested revision and worktree state, optionally hashing the diff."""
    state = {
        "head": subprocess.check_output(
            ["git", "rev-parse", "HEAD"], cwd=root, text=True).strip(),
        "status": subprocess.check_output(
            ["git", "status", "--short"], cwd=root, text=True).strip(),
    }
    if include_diff:
        diff = subprocess.check_output(["git", "diff", "--binary"], cwd=root)
        state["tracked_diff_sha256"] = hashlib.sha256(diff).hexdigest()
    return state


def validate_report_status(report_path: Path, status: str) -> None:
    """Require one report status declaration matching the machine result."""
    declarations = [line.removeprefix("Status:").strip()
                    for line in report_path.read_text(encoding="utf-8").splitlines()
                    if line.startswith("Status:")]
    if len(declarations) != 1 or declarations[0] != status:
        raise ValueError("report.md status must match result.json status")


def isolated_build_env(data_dir: Path) -> dict:
    """Keep Go build and temporary files inside this run's data directory."""
    tmp_dir = data_dir / "tmp"
    tmp_dir.mkdir(exist_ok=True)
    return {**os.environ, "GOCACHE": str(data_dir / "go-cache"),
            "GOTMPDIR": str(tmp_dir), "TMPDIR": str(tmp_dir),
            "TMP": str(tmp_dir), "TEMP": str(tmp_dir)}


def isolated_server_env(data_dir: Path, port: int, *, extra: dict | None = None) -> dict:
    """Give an owned server its own product state, logs, temp files, and port."""
    product_dir = data_dir / "product"
    logs_dir = data_dir / "logs"
    tmp_dir = data_dir / "tmp"
    for directory in (product_dir, logs_dir, tmp_dir):
        directory.mkdir(exist_ok=True)
    env = {**os.environ, "DATA_ROOT": str(product_dir), "LOG_DIR": str(logs_dir),
           "AOS_ROOT": str(product_dir / "aos"),
           "AOS_META_ROOT": str(product_dir / "aosmeta"),
           "JINSHU_ROOT": str(product_dir / "jinshu"), "PORT": str(port),
           "TMPDIR": str(tmp_dir), "TMP": str(tmp_dir), "TEMP": str(tmp_dir)}
    if extra:
        overlap = set(extra) & {
            "DATA_ROOT", "LOG_DIR", "AOS_ROOT", "AOS_META_ROOT", "JINSHU_ROOT",
            "PORT", "TMPDIR", "TMP", "TEMP"}
        if overlap:
            raise ValueError(f"extra server environment overrides isolation: {sorted(overlap)}")
        env.update(extra)
    return env


def build_server(root: Path, data_dir: Path, *, server_dir: str = "server",
                 package: str = "./cmd", binary_name: str = "qingqiu-server",
                 log_name: str = "build.log", timeout: float = 300) -> Path:
    """Build the owned server binary and keep Go build state under data_dir."""
    binary = data_dir / "bin" / binary_name
    binary.parent.mkdir(exist_ok=True)
    build_env = isolated_build_env(data_dir)
    try:
        with (data_dir / log_name).open("w") as build_log:
            try:
                completed = subprocess.run(
                    ["go", "build", "-o", str(binary), package],
                    cwd=root / server_dir, env=build_env, stdout=build_log,
                    stderr=subprocess.STDOUT, timeout=timeout)
            except subprocess.TimeoutExpired as exc:
                raise SetupError(
                    f"server build exceeded {timeout:g} seconds; inspect {log_name}") from exc
    finally:
        shutil.rmtree(data_dir / "go-cache", ignore_errors=True)
    if completed.returncode:
        raise SetupError(f"server build failed; inspect {log_name}")
    return binary


def start_server(binary: Path, data_dir: Path, env: dict, base: str, *,
                 log_name: str = "server.log", ready_path: str = "/api/version",
                 ready_code: int = 0, timeout: float = 90) -> subprocess.Popen:
    """Start one owned server, appending output, and wait until it answers."""
    with (data_dir / log_name).open("a") as log:
        process = subprocess.Popen([str(binary)], cwd=data_dir, env=env,
                                   stdout=log, stderr=subprocess.STDOUT)
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise SetupError(f"server exited during startup with {process.returncode}")
        try:
            response = http_json(base, "GET", ready_path, timeout=10,
                                 expected_status=200)
            if response.get("code") == ready_code:
                return process
        except HttpRequestError:
            pass
        time.sleep(0.25)
    stop_server(process)
    raise SetupError(f"server did not become ready within {timeout:g} seconds")


def stop_server(process: subprocess.Popen, *, timeout: float = 20) -> int:
    """Stop one owned server gracefully and return its exit code."""
    if process.poll() is not None:
        return process.returncode
    process.terminate()
    try:
        process.wait(timeout=timeout)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=5)
        raise SetupError("owned server did not stop gracefully")
    return process.returncode


class RunRecord:
    """Create and finalize one isolated, Git-ignored case report directory."""

    REQUIRED = (
        "track", "status", "detail", "execution_command", "started_at_utc",
        "ended_at_utc", "tested_revision", "limitations",
    )
    STATUSES = frozenset({"PASS", "FAIL", "INCONCLUSIVE", "ERROR"})

    def __init__(self, case_dir: Path):
        case_dir = Path(case_dir).resolve()
        reports_dir = case_dir / "reports"
        reports_dir.mkdir(exist_ok=True)
        self.root = Path(tempfile.mkdtemp(prefix="run-", dir=reports_dir))
        self.case_dir = case_dir
        self.data_dir = self.root / "data"
        self.data_dir.mkdir()
        self.case_name = case_dir.name

    def write_data_json(self, filename: str, value: object) -> None:
        """Write a JSON evidence file directly inside this run's data directory."""
        if not filename.endswith(".json") or Path(filename).name != filename:
            raise ValueError("data JSON filename must be a direct .json file")
        self._write_json(self.data_dir / filename, value)

    def write_report(self, content: str) -> None:
        """Write the human-readable conclusion before finalizing the result."""
        (self.root / "report.md").write_text(content, encoding="utf-8")

    def write_case(self, *, track: str, status: str, detail: str,
                   execution_command: str, started_at_utc: str,
                   tested_revision: dict | None, checks: list | None = None,
                   limitations: list | None = None, notes: str = "",
                   extra: dict | None = None,
                   validator: Callable[[dict], None] | None = None) -> None:
        """Write the standard report and result assembled from common fields."""
        check_lines = "".join(
            f"- {'PASS' if item['passed'] else 'FAIL'}: {item['name']}\n"
            for item in checks or []) or "- No assertions completed.\n"
        report = (f"# {self.case_name} run\n\nStatus: {status}\n\n{detail}\n\n"
                  f"Command: `{execution_command}`\n\n"
                  f"Tested revision: "
                  f"`{(tested_revision or {}).get('head', 'unavailable')}`\n\n")
        if checks is not None:
            report += f"## Checks\n\n{check_lines}\n"
        if notes:
            report += notes.rstrip() + "\n"
        self.write_report(report)

        result = {
            "track": track, "status": status, "detail": detail,
            "execution_command": execution_command,
            "started_at_utc": started_at_utc,
            "ended_at_utc": datetime.now(timezone.utc).isoformat(),
            "tested_revision": tested_revision,
            "environment": {"os": platform.system(), "arch": platform.machine(),
                            "python": platform.python_version()},
            "limitations": limitations or [],
        }
        if checks is not None:
            result["checks"] = checks
        if extra:
            reserved = set(result) | {"case_name", "run_id", "artifacts"}
            if overlap := set(extra) & reserved:
                raise ValueError(f"extra result fields replace managed fields: {sorted(overlap)}")
            result.update(extra)
        self.write_result(result, validator)

    def write_result(self, fields: dict, validator: Callable[[dict], None] | None = None) -> None:
        """Add common identity/evidence fields and validate the report layout."""
        result = dict(fields)
        for key, value in (("case_name", self.case_name), ("run_id", self.root.name)):
            if key in result and result[key] != value:
                raise ValueError(f"{key} must match the case/run directory")
            result[key] = value
        if "artifacts" in result:
            raise ValueError("artifacts are generated by RunRecord")
        result["artifacts"] = self.artifact_paths()
        missing = [key for key in self.REQUIRED if key not in result]
        if missing:
            raise ValueError(f"result missing common fields: {', '.join(missing)}")
        if result["status"] not in self.STATUSES:
            raise ValueError(f"invalid result status: {result['status']!r}")
        if not isinstance(result["limitations"], list):
            raise ValueError("limitations must be a list")
        report_path = self.root / "report.md"
        result_path = self.root / "result.json"
        if not report_path.is_file():
            raise ValueError("report.md must exist before result.json")
        if report_path.is_symlink() or result_path.is_symlink() or self.data_dir.is_symlink():
            raise ValueError("run-root entries must belong to this run")
        validate_report_status(report_path, result["status"])
        unexpected = {path.name for path in self.root.iterdir()} - {
            "data", "report.md", "result.json"}
        if unexpected:
            raise ValueError(f"unexpected run-root entries: {sorted(unexpected)}")
        if validator is not None:
            validator(result)
        self._write_json(result_path, result)

    def artifact_paths(self) -> list[str]:
        """Index top-level evidence files; product state stays under data/ unlisted."""
        files = [path for path in self.data_dir.iterdir() if path.is_file()]
        if any(path.is_symlink() or not path.resolve().is_relative_to(self.data_dir)
               for path in files):
            raise ValueError("evidence files must belong to this run's data directory")
        return ["report.md"] + sorted(
            path.relative_to(self.root).as_posix()
            for path in files
        )

    @staticmethod
    def _write_json(path: Path, value: object) -> None:
        path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n",
                        encoding="utf-8")
