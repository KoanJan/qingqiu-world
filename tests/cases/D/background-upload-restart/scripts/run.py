#!/usr/bin/env python3
"""Verify background upload, restart persistence, readback, and deletion."""

from pathlib import Path
import hashlib
import json
import sys
import urllib.error
import urllib.parse
import urllib.request
import uuid
from datetime import datetime, timezone


ROOT = Path(__file__).resolve().parents[5]
CASE_DIR = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(CASE_DIR.parents[1]))
from tools import (CheckFailure, HttpStatusError, HttpShapeError,
                   HttpTransportError, RunRecord, SetupError, build_server,
                   call_json, check, free_port, git_state, isolated_server_env,
                   start_server, stop_server)

ASSET_PNG = CASE_DIR / "assets" / "sample-background.png"
ASSET_INVALID = CASE_DIR / "assets" / "invalid-background.txt"
PNG_MAGIC = b"\x89PNG\r\n\x1a\n"


def sha256(data: bytes) -> str:
    """Return the lowercase hex SHA-256 of a byte sequence."""
    return hashlib.sha256(data).hexdigest()


def multipart_body(filename: str, content: bytes) -> tuple[bytes, str]:
    """Build a one-field multipart/form-data body for the `file` form field."""
    boundary = f"----qingqiu-case-{uuid.uuid4().hex}"
    head = (f"--{boundary}\r\n"
            f'Content-Disposition: form-data; name="file"; filename="{filename}"\r\n'
            f"Content-Type: application/octet-stream\r\n\r\n").encode()
    tail = f"\r\n--{boundary}--\r\n".encode()
    return head + content + tail, f"multipart/form-data; boundary={boundary}"


def http_bytes(base: str, method: str, path: str, *, timeout: float = 10) -> tuple[int, bytes]:
    """Send one raw request and return its HTTP status and response bytes."""
    request = urllib.request.Request(base + path, method=method)
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            return response.status, response.read()
    except urllib.error.HTTPError as exc:
        return exc.code, exc.read()
    except (urllib.error.URLError, TimeoutError) as exc:
        raise HttpTransportError(f"{method} {path} failed: {exc}") from exc


def upload_background(base: str, filename: str, content: bytes,
                      responses: list) -> dict:
    """Upload one file through the real multipart endpoint and record evidence."""
    body, content_type = multipart_body(filename, content)
    request = urllib.request.Request(
        base + "/api/appearance/backgrounds", data=body,
        headers={"Content-Type": content_type}, method="POST")
    try:
        with urllib.request.urlopen(request, timeout=10) as response:
            envelope = json.load(response)
    except urllib.error.HTTPError as exc:
        raise HttpStatusError(
            f"POST /api/appearance/backgrounds returned HTTP {exc.code}") from exc
    except (urllib.error.URLError, TimeoutError, ValueError) as exc:
        raise HttpTransportError(
            f"POST /api/appearance/backgrounds could not return JSON: {exc}") from exc
    if not isinstance(envelope, dict):
        raise HttpShapeError(
            "POST /api/appearance/backgrounds returned a non-object response")
    responses.append({"method": "POST", "path": "/api/appearance/backgrounds",
                      "request": {"field": "file", "filename": filename,
                                  "size": len(content),
                                  "sha256": sha256(content)},
                      "response": envelope})
    return envelope


def background_names(base: str, responses: list) -> list[str]:
    """List uploaded backgrounds through the product API and return filenames."""
    envelope = call_json(base, "GET", "/api/appearance/backgrounds",
                         responses=responses)
    if envelope.get("code") != 0:
        raise CheckFailure(
            f"list backgrounds returned business code {envelope.get('code')!r}")
    data = envelope.get("data")
    if data is None:
        # The handler emits a nil slice as JSON null once the directory exists
        # but holds no uploads; treat it as the same empty listing.
        return []
    if not isinstance(data, list):
        raise CheckFailure(f"list backgrounds returned non-list data: {data!r}")
    return [item.get("filename") for item in data if isinstance(item, dict)]


def read_background(base: str, filename: str, responses: list,
                    *, expected_status: int = 200) -> bytes:
    """Read one uploaded background as raw bytes and record transport evidence."""
    path = "/api/appearance/backgrounds/" + urllib.parse.quote(filename)
    status, body = http_bytes(base, "GET", path)
    responses.append({"method": "GET", "path": path, "request": None,
                      "response": {"http_status": status, "size": len(body),
                                   "sha256": sha256(body)}})
    if status != expected_status:
        raise CheckFailure(
            f"GET {path} returned HTTP {status}; expected {expected_status}")
    return body


def disk_files(directory: Path) -> list[str]:
    """Return regular file names in a directory, or an empty list if absent."""
    if not directory.is_dir():
        return []
    return sorted(path.name for path in directory.iterdir() if path.is_file())


def main() -> int:
    started = datetime.now(timezone.utc).isoformat()
    record = RunRecord(CASE_DIR)
    data_dir = record.data_dir
    command = f"python3 {Path(__file__).resolve().relative_to(ROOT)}"
    backgrounds_dir = data_dir / "product" / "settings" / "backgrounds"
    developer_backgrounds = ROOT / "data" / "settings" / "backgrounds"
    responses = []
    checks = []
    process = None
    revision = None
    status = "ERROR"
    detail = "Case did not start"
    binary_evidence = {}
    print(f"Evidence directory: {record.root}", flush=True)
    try:
        revision = git_state(ROOT)
        if not ASSET_PNG.is_file() or not ASSET_INVALID.is_file():
            raise SetupError(f"missing committed asset under {CASE_DIR / 'assets'}")
        source_bytes = ASSET_PNG.read_bytes()
        invalid_bytes = ASSET_INVALID.read_bytes()
        if not source_bytes.startswith(PNG_MAGIC):
            raise SetupError("sample-background.png is not a PNG file")
        if ASSET_INVALID.suffix != ".txt":
            raise SetupError("invalid-background.txt must use the .txt suffix")
        record.write_data_json("binary_evidence.json", {
            "source": {"asset": ASSET_PNG.relative_to(CASE_DIR).as_posix(),
                       "size": len(source_bytes), "sha256": sha256(source_bytes)},
            "invalid": {"asset": ASSET_INVALID.relative_to(CASE_DIR).as_posix(),
                        "size": len(invalid_bytes), "sha256": sha256(invalid_bytes)},
        })
        (data_dir / "upload-source.png").write_bytes(source_bytes)

        binary = build_server(ROOT, data_dir)
        port = free_port()
        base = f"http://127.0.0.1:{port}"
        env = isolated_server_env(data_dir, port)
        process = start_server(binary, data_dir, env, base, log_name="server-1.log")

        empty = call_json(base, "GET", "/api/appearance/backgrounds",
                          responses=responses)
        check(checks, "fresh data list response", empty.get("code"), 0)
        check(checks, "fresh data has no backgrounds", empty.get("data"), [])

        uploaded = upload_background(base, ASSET_PNG.name, source_bytes, responses)
        check(checks, "upload response", uploaded.get("code"), 0)
        filename = uploaded.get("data", {}).get("filename")
        if not isinstance(filename, str) or not filename:
            raise CheckFailure(f"upload returned invalid filename: {filename!r}")
        check(checks, "uploaded filename keeps the source extension",
              filename.endswith(".png"), True)
        check(checks, "uploaded filename is a bare basename",
              "/" not in filename and "\\" not in filename and ".." not in filename, True)
        check(checks, "backgrounds directory lives under isolated DATA_ROOT",
              backgrounds_dir.resolve().is_relative_to((data_dir / "product").resolve()), True)
        stored = backgrounds_dir / filename
        check(checks, "uploaded file exists in the isolated backgrounds directory",
              stored.is_file(), True)
        check(checks, "uploaded file absent from developer data directory",
              (developer_backgrounds / filename).exists(), False)
        stored_bytes = stored.read_bytes()
        check(checks, "stored file size matches the asset", len(stored_bytes), len(source_bytes))
        check(checks, "stored file sha256 matches the asset", sha256(stored_bytes), sha256(source_bytes))

        names_before_restart = background_names(base, responses)
        check(checks, "list contains the uploaded file", names_before_restart, [filename])

        read_before = read_background(base, filename, responses)
        (data_dir / "readback-before-restart.png").write_bytes(read_before)
        check(checks, "read before restart byte length", len(read_before), len(source_bytes))
        check(checks, "read before restart matches upload bytes", sha256(read_before), sha256(source_bytes))

        rejected = upload_background(base, ASSET_INVALID.name, invalid_bytes, responses)
        check(checks, "invalid extension upload is rejected", rejected.get("code"), 1)
        check(checks, "invalid extension response carries no data", rejected.get("data"), None)
        names_after_invalid = background_names(base, responses)
        check(checks, "invalid upload did not change the listing",
              names_after_invalid, names_before_restart)
        files_after_invalid = disk_files(backgrounds_dir)
        check(checks, "invalid upload produced no file", files_after_invalid, [filename])
        check(checks, "invalid upload left no .txt file",
              [name for name in files_after_invalid if name.endswith(".txt")], [])

        first_exit = stop_server(process)
        process = None
        check(checks, "first process stopped cleanly", first_exit, 0)
        process = start_server(binary, data_dir, env, base, log_name="server-2.log")

        names_after_restart = background_names(base, responses)
        check(checks, "uploaded file is still listed after restart",
              names_after_restart, [filename])
        read_after = read_background(base, filename, responses)
        (data_dir / "readback-after-restart.png").write_bytes(read_after)
        check(checks, "read after restart byte length", len(read_after), len(source_bytes))
        check(checks, "read after restart matches upload bytes", sha256(read_after), sha256(source_bytes))
        binary_evidence = {
            "filename": filename,
            "source": {"size": len(source_bytes), "sha256": sha256(source_bytes)},
            "readback_before_restart": {"size": len(read_before), "sha256": sha256(read_before)},
            "readback_after_restart": {"size": len(read_after), "sha256": sha256(read_after)},
        }
        record.write_data_json("readback_evidence.json", binary_evidence)

        deleted = call_json(base, "DELETE",
                            "/api/appearance/backgrounds/" + urllib.parse.quote(filename),
                            responses=responses)
        check(checks, "delete response", deleted.get("code"), 0)
        names_after_delete = background_names(base, responses)
        check(checks, "listing is empty after delete", names_after_delete, [])
        check(checks, "deleted file is gone from disk", stored.exists(), False)
        check(checks, "directory holds no files after delete",
              disk_files(backgrounds_dir), [])
        read_after_delete = read_background(base, filename, responses,
                                            expected_status=404)
        check(checks, "read after delete byte length", len(read_after_delete), 0)

        second_exit = stop_server(process)
        process = None
        check(checks, "second process stopped cleanly", second_exit, 0)
        status = "PASS"
        detail = "Uploaded bytes survived a restart and deletion removed the file"
    except (CheckFailure, HttpStatusError, HttpShapeError) as exc:
        status, detail = "FAIL", str(exc)
    except (SetupError, HttpTransportError, OSError) as exc:
        status, detail = "ERROR", str(exc)
    finally:
        if process is not None:
            try:
                stop_server(process)
            except SetupError as exc:
                status, detail = "ERROR", f"{detail}; cleanup: {exc}"
        record.write_data_json("responses.json", responses)
        record.write_case(
            track="deterministic", status=status, detail=detail,
            execution_command=command, started_at_utc=started,
            tested_revision=revision, checks=checks,
            extra={"binary_evidence": binary_evidence},
            limitations=[
                "Covers the server process and HTTP API only; it does not inspect "
                "browser rendering or Electron packaging.",
                "Uses committed assets rather than real user photos.",
            ],
            notes="The runner used a fresh data root and two owned server processes. "
                  "Inspect `result.json`, then `data/responses.json`, "
                  "`data/readback_evidence.json`, `data/upload-source.png`, "
                  "`data/readback-before-restart.png`, "
                  "`data/readback-after-restart.png`, `data/server-1.log`, and "
                  "`data/server-2.log` in this directory.")
    print(f"{record.case_name}: {status}: {detail}")
    return {"PASS": 0, "FAIL": 1, "ERROR": 2}[status]


if __name__ == "__main__":
    sys.exit(main())
