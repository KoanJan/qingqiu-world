#!/usr/bin/env python3
"""Check user-profile persistence through real HTTP calls and a server restart."""

from pathlib import Path
import sqlite3
import sys
from datetime import datetime, timezone


ROOT = Path(__file__).resolve().parents[5]
CASE_DIR = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(CASE_DIR.parents[1]))
from tools import (CheckFailure, HttpShapeError, HttpStatusError,
                   HttpTransportError, RunRecord, SetupError, build_server,
                   call_json, check, free_port, git_state,
                   isolated_server_env, start_server, stop_server)

NAME = "profile-restart-user"
ORIGINAL_BIO = "Created before restart"
UPDATED_BIO = "Updated before restart"


def main() -> int:
    started = datetime.now(timezone.utc).isoformat()
    record = RunRecord(CASE_DIR)
    data_dir = record.data_dir
    command = f"python3 {Path(__file__).resolve().relative_to(ROOT)}"
    responses = []
    checks = []
    process = None
    revision = None
    status = "ERROR"
    detail = "Case did not start"
    print(f"Evidence directory: {record.root}", flush=True)
    try:
        revision = git_state(ROOT)
        binary = build_server(ROOT, data_dir)

        port = free_port()
        base = f"http://127.0.0.1:{port}"
        env = isolated_server_env(data_dir, port)
        process = start_server(binary, data_dir, env, base, log_name="server-1.log")

        initial = call_json(base, "GET", "/api/user-profile", responses=responses)
        check(checks, "initial profile response", initial.get("code"), 0)
        check(checks, "fresh data has no user", initial.get("data"), {})

        created = call_json(base, "PUT", "/api/user-profile",
                            {"name": NAME, "bio": ORIGINAL_BIO}, responses=responses)
        check(checks, "create response", created.get("code"), 0)
        person_id = created.get("data", {}).get("id")
        check(checks, "created profile name", created.get("data", {}).get("name"), NAME)
        check(checks, "created profile bio", created.get("data", {}).get("bio"), ORIGINAL_BIO)
        check(checks, "created profile type", created.get("data", {}).get("type"), 2)
        if not isinstance(person_id, int) or person_id <= 0:
            raise CheckFailure(f"created profile has invalid ID: {person_id!r}")

        updated = call_json(base, "PUT", "/api/user-profile",
                            {"name": "attempted-rename", "bio": UPDATED_BIO},
                            responses=responses)
        check(checks, "update response", updated.get("code"), 0)
        check(checks, "user ID is stable", updated.get("data", {}).get("id"), person_id)
        check(checks, "user name is immutable", updated.get("data", {}).get("name"), NAME)
        check(checks, "bio update took effect", updated.get("data", {}).get("bio"), UPDATED_BIO)

        first_exit = stop_server(process)
        process = None
        check(checks, "first process stopped cleanly", first_exit, 0)
        process = start_server(binary, data_dir, env, base, log_name="server-2.log")
        restored = call_json(base, "GET", "/api/user-profile", responses=responses)
        check(checks, "read after restart", restored.get("code"), 0)
        for field, expected in (("id", person_id), ("name", NAME),
                                ("bio", UPDATED_BIO), ("type", 2)):
            check(checks, f"persisted {field}", restored.get("data", {}).get(field), expected)

        second_exit = stop_server(process)
        process = None
        check(checks, "second process stopped cleanly", second_exit, 0)
        db_path = data_dir / "product" / "db" / "database.db"
        with sqlite3.connect(f"file:{db_path}?mode=ro", uri=True) as db:
            rows = [list(row) for row in db.execute(
                "SELECT id, name, bio, type FROM persons WHERE type = 2")]
        record.write_data_json("db_state.json", rows)
        check(checks, "exactly one persisted human profile", rows,
              [[person_id, NAME, UPDATED_BIO, 2]])
        status = "PASS"
        detail = "Profile update and immutable name survived a real server restart"
    except (CheckFailure, HttpStatusError, HttpShapeError) as exc:
        status, detail = "FAIL", str(exc)
    except (SetupError, HttpTransportError, OSError, sqlite3.Error) as exc:
        status, detail = "ERROR", str(exc)
    finally:
        if process is not None:
            try:
                stop_server(process)
            except SetupError as exc:
                status, detail = "ERROR", f"{detail}; cleanup: {exc}"
        if status == "PASS":
            (data_dir / "qingqiu-server").unlink(missing_ok=True)
        record.write_data_json("responses.json", responses)
        record.write_case(
            track="deterministic", status=status, detail=detail,
            execution_command=command, started_at_utc=started,
            tested_revision=revision, checks=checks,
            notes="The runner used a fresh data root and two owned server processes. "
                  "Inspect `result.json`, then `data/responses.json`, "
                  "`data/db_state.json`, `data/server-1.log`, and "
                  "`data/server-2.log` in this directory.")
    print(f"{record.case_name}: {status}: {detail}")
    return {"PASS": 0, "FAIL": 1, "ERROR": 2}[status]


if __name__ == "__main__":
    sys.exit(main())
