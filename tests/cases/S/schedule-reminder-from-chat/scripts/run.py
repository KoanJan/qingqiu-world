"""Run the chat-to-reminder case against a real, isolated product server.

The case sends the scenario's fixed chat request, waits for either an alarm
record or an Agent reply, captures the session and read-only database rows,
checks the decision-to-effect chain and the requested time, and leaves reminder
meaning to an independent semantic judge.
"""

import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import platform
import sqlite3
import sys
import time
import urllib.parse

HERE = Path(__file__).resolve().parent
CASE_DIR = HERE.parent
ROOT = CASE_DIR.parents[3]
sys.path.insert(0, str(ROOT / "tests/cases"))
sys.path.insert(0, str(CASE_DIR.parent))
sys.path.insert(0, str(HERE))

from base_contracts import load_scenario, validate_result
from case_contract import Result, Scenario
from provider_env import missing_provider_env
from tools import (HttpRequestError, RunRecord, SetupError, build_server,
                   free_port, git_state, http_json, isolated_server_env,
                   provider_endpoint, start_server, stop_server)


# Product constants mirrored from server/internal/model and runtime action code.
EVENT_TYPE_MESSAGE = 1
ACTION_TYPE_CREATE_ALARM = 4
EFFECT_TYPE_SCHEDULED_EVENT = 3
SCHEDULED_STATUS_PENDING = 0

CHAT_PATH = "/api/chat/new"
MESSAGES_PATH = "/api/messages"
ACTIVITIES_PATH = "/api/sessions"
SUBJECT_ENTRY = (
    f"POST {CHAT_PATH}; GET {MESSAGES_PATH}/:id; "
    f"GET {ACTIVITIES_PATH}/:id/activities; read-only SELECT on messages, "
    "events, decisions, actions, action_effects, scheduled_events"
)


class ScenarioError(Exception):
    """A prerequisite or real product operation failed."""


def request(base: str, method: str, path: str, payload=None, content_type=None):
    """Return product data only when the API business code succeeds."""
    try:
        result = http_json(base, method, path, payload, content_type=content_type)
    except HttpRequestError as exc:
        raise ScenarioError(str(exc)) from exc
    if result.get("code") != 0:
        raise ScenarioError(
            f"{method} {path}: business code {result.get('code')}: {result.get('message')}")
    return result.get("data")


def read_rows(db_path: Path, sql: str, params=()):
    """Read committed rows without blocking the owned server's writer."""
    conn = sqlite3.connect(f"file:{db_path}?mode=ro", uri=True, timeout=10)
    conn.row_factory = sqlite3.Row
    try:
        return [dict(row) for row in conn.execute(sql, params)]
    finally:
        conn.close()


def wait_for_scheduled_event(base: str, db_path: Path, session_id: int, agent_id: int,
                             process, limit: float):
    """Wait for a persisted alarm or an Agent reply for this chat session."""
    end = time.monotonic() + limit
    saw_reply = False
    while time.monotonic() < end:
        if process.poll() is not None:
            raise ScenarioError(f"server exited while waiting for a reminder: {process.returncode}")
        rows = read_rows(db_path,
                         "SELECT * FROM scheduled_events WHERE session_id = ? ORDER BY id",
                         (session_id,))
        if rows:
            return rows, saw_reply
        messages = request(base, "GET", f"{MESSAGES_PATH}/{session_id}")
        saw_reply = saw_reply or any(
            item.get("person_id") == agent_id and str(item.get("content", "")).strip()
            for item in messages)
        time.sleep(1)
    return [], saw_reply


def read_db_evidence(db_path: Path):
    """Capture every persistence table needed to inspect the reminder chain."""
    conn = sqlite3.connect(f"file:{db_path}?mode=ro", uri=True, timeout=10)
    conn.row_factory = sqlite3.Row
    try:
        def rows(table: str):
            return [dict(row) for row in conn.execute(f"SELECT * FROM {table} ORDER BY id")]

        return {
            "messages": rows("messages"),
            "events": rows("events"),
            "decisions": rows("decisions"),
            "actions": rows("actions"),
            "action_effects": rows("action_effects"),
            "scheduled_events": rows("scheduled_events"),
        }
    finally:
        conn.close()


def parse_time(value):
    """Parse SQLite/GORM timestamps that separate date and time with a space."""
    return datetime.fromisoformat(str(value).replace(" ", "T", 1))


def check_db_evidence(evidence, session, case: dict):
    """Check stable record, state, and time facts; leave reminder meaning to the judge.

    Failure prefixes separate causes: `record` is an absent required persistence
    record, `state` is product state contradicting the intended agent/session,
    and `time` is a trigger that does not match the declared future time.
    """
    failures = []
    session_id = session["chat"]["session_id"]
    agent_id = session["agent_id"]
    user_id = session["user_id"]
    request_text = case["request"]
    expected_delay = case["expected_delay_seconds"]
    tolerance = case["delay_tolerance_seconds"]

    senders = [item for item in evidence["messages"]
               if item["session_id"] == session_id and item["person_id"] == user_id and
               item["content"] == request_text]
    if len(senders) != 1:
        failures.append("record: chat session did not preserve the exact requested message")
        return failures
    sender = senders[0]
    if session["chat"].get("message_id") != sender["id"]:
        failures.append("record: chat response message ID does not match the persisted request")
        return failures

    origin_events = [item for item in evidence["events"]
                     if item["event_type"] == EVENT_TYPE_MESSAGE and item["ref_id"] == sender["id"]]
    if not origin_events:
        failures.append("record: no event recorded for the original request message")
        return failures

    pending = [item for item in evidence["scheduled_events"]
               if item["person_id"] == agent_id and item["session_id"] == session_id and
               item["status"] == SCHEDULED_STATUS_PENDING]
    if not pending:
        failures.append("state: no pending scheduled event belongs to the intended Agent and session")
        return failures

    pending_ids = {item["id"] for item in pending}
    effects = [item for item in evidence["action_effects"]
               if item["effect_type"] == EFFECT_TYPE_SCHEDULED_EVENT and
               item["effect_id"] in pending_ids]
    if not effects:
        failures.append("record: no action_effect links the pending event to an action")
        return failures

    action_ids = {item["action_id"] for item in effects}
    alarms = [item for item in evidence["actions"]
              if item["id"] in action_ids and item["type"] == ACTION_TYPE_CREATE_ALARM]
    if not alarms:
        failures.append("record: no create_alarm action is linked to the pending event")
        return failures

    decision_ids = {item["decision_id"] for item in alarms}
    decisions = [item for item in evidence["decisions"]
                 if item["id"] in decision_ids and item["person_id"] == agent_id]
    if not decisions:
        failures.append("record: no Agent decision backs the create_alarm action")
        return failures

    plan_ok = False
    for action in alarms:
        try:
            plan = json.loads(action["plan_json"] or "{}")
        except (TypeError, ValueError):
            continue
        if isinstance(plan, dict) and plan.get("trigger_at") and plan.get("message"):
            plan_ok = True
    if not plan_ok:
        failures.append("record: create_alarm plan lacks trigger_at or message")

    for item in pending:
        if not str(item.get("message", "")).strip():
            failures.append("state: scheduled event has no reminder message note")

    deltas = []
    for item in pending:
        try:
            created_at = parse_time(sender["created_at"])
            trigger_at = parse_time(item["trigger_at"])
        except (TypeError, ValueError):
            failures.append("record: scheduled event has an invalid or missing trigger time")
            continue
        deltas.append((item["id"], (trigger_at - created_at).total_seconds()))
    if deltas and not any(abs(delta - expected_delay) <= tolerance for _, delta in deltas):
        detail = ", ".join(f"event {event_id}: {delta:.0f}s" for event_id, delta in deltas)
        failures.append(
            f"time: trigger_at is outside the declared tolerance of the requested future time "
            f"(expected {expected_delay}s +/- {tolerance}s; observed {detail})")
    return failures


def finalize(record: RunRecord, case: dict, metadata: dict, status: str, detail: str, *,
             failures: list | None = None, checks_performed: bool = False):
    """Write capture, report, and a validator-checked result scaffold."""
    failures = failures or []
    limitations = []
    if status != "ERROR":
        limitations.append(
            "observed_model_calls is null: the chat Decide/Comprehension calls are not "
            "persisted as work interactions, so this case does not derive a model-call count; "
            "max_model_calls configures per-process cognitive requests, not a run-wide cap")
    if status == "INCONCLUSIVE":
        limitations.append("Independent semantic judgment has not run")

    record.write_data_json("capture.json", {
        "status": status, "mechanical_failures": failures,
        "mechanical_checks_performed": checks_performed,
        "observed_model_calls": None,
    })
    criteria = [{"id": item["id"], "status": "INCONCLUSIVE", "observed": "",
                 "evidence": []} for item in case["required"] + case["forbidden"]]
    result = {
        "schema_version": 1, "scenario_version": case["version"],
        "scenario_sha256": metadata["scenario_sha256"],
        "track": "semantic", "status": status, "detail": detail,
        "execution_command": metadata["execution_command"],
        "started_at_utc": metadata["started_at_utc"],
        "ended_at_utc": datetime.now(timezone.utc).isoformat(),
        "tested_revision": metadata["git"],
        "finished_revision": git_state(ROOT, include_diff=True),
        "request": case["request"], "subject_entry": SUBJECT_ENTRY,
        "model": {"llm": metadata.get("llm_model_id"),
                  "embedding": metadata.get("embedding_model_id")},
        "environment": metadata["environment"],
        "limits": case["limits"], "observed_model_calls": None,
        "mechanical_failures": failures,
        "mechanical_checks_performed": checks_performed,
        "judge": None, "criteria": criteria,
        "limitations": limitations, "deviations": [],
    }
    record.write_report(
        f"# {record.case_name} v{case['version']} run\n\nStatus: {status}\n\n{detail}\n\n"
        f"Request: `{case['request']}`\n\n"
        f"Command: `{metadata['execution_command']}`\n\n"
        f"Tested revision: `{metadata['git']['head']}`\n\n"
        f"Mechanical checks: {'completed' if checks_performed else 'not completed'}.\n\n"
        f"Mechanical failures: {'; '.join(failures) if failures else 'none recorded'}.\n\n"
        "Failure prefixes: `record` (required persistence absent), `state` (product state "
        "contradicts the intended Agent or session), `time` (trigger outside the declared "
        "future-time tolerance).\n\n"
        "Semantic criteria: awaiting independent judgment.\n\n"
        "## Evidence\n\n"
        "- `data/metadata.json`: scenario checksum, tested revision, provider models, limits.\n"
        "- `data/capture.json`: runner-captured mechanical facts, written before judgment.\n"
        "- `data/session.json`: chat/session responses and visible messages and activities.\n"
        "- `data/db_evidence.json`: read-only messages, events, decisions, actions, "
        "action_effects, and scheduled_events rows.\n"
        "- `data/build.log` and `data/server.log`: build and owned-process output.\n\n"
        "Inspect `data/db_evidence.json` for the original message and its event, then for "
        "the pending scheduled event linked through action_effects to a create_alarm "
        "action and its Agent decision. Record the independent semantic verdict and "
        "evidence locations here.\n")
    record.write_result(result, lambda value: validate_result(
        value, record.root, case, record.case_dir, model=Result, final=False))


def run() -> int:
    """Execute one isolated chat-to-reminder run."""
    record = RunRecord(CASE_DIR)
    data_dir = record.data_dir
    started = datetime.now(timezone.utc).isoformat()
    command = f"{sys.executable} {Path(__file__).resolve().relative_to(ROOT)}"
    try:
        case, scenario_path = load_scenario(CASE_DIR, model=Scenario)
    except (OSError, ValueError, KeyError, TypeError) as exc:
        detail = f"Scenario setup failed for {CASE_DIR.name}: {exc}"
        record.write_report(
            f"# {CASE_DIR.name} setup\n\nStatus: ERROR\n\n{detail}\n\n"
            f"Command: `{command}`\n\n"
            "No product action ran; the scenario definition could not be validated.\n")
        record.write_result({
            "track": "semantic", "status": "ERROR", "detail": detail,
            "execution_command": command, "started_at_utc": started,
            "ended_at_utc": datetime.now(timezone.utc).isoformat(),
            "tested_revision": git_state(ROOT, include_diff=True),
            "limitations": ["Scenario definition could not be validated"],
        })
        print(f"{detail}\nEvidence directory: {record.root}", file=sys.stderr)
        return 2

    metadata = {
        "case_name": record.case_name, "scenario_version": case["version"],
        "scenario_sha256": hashlib.sha256(scenario_path.read_bytes()).hexdigest(),
        "started_at_utc": started,
        "environment": {"os": platform.system(), "arch": platform.machine(),
                        "python": platform.python_version(), "isolated_data_root": True},
        "subject_entry": SUBJECT_ENTRY, "execution_command": command,
        "git": git_state(ROOT, include_diff=True), "run_dir": str(record.root),
    }
    missing = missing_provider_env()
    if missing:
        detail = "Missing required environment variables: " + ", ".join(missing)
        record.write_data_json("metadata.json", metadata)
        finalize(record, case, metadata, "ERROR", detail)
        print(f"{detail}\nEvidence directory: {record.root}", file=sys.stderr)
        return 2

    metadata.update({
        "llm_endpoint": provider_endpoint(os.environ["QINGQIU_TEST_LLM_BASE_URL"]),
        "llm_model_id": os.environ["QINGQIU_TEST_LLM_MODEL_ID"],
        "embedding_endpoint": provider_endpoint(os.environ["QINGQIU_TEST_EMBEDDING_BASE_URL"]),
        "embedding_model_id": os.environ["QINGQIU_TEST_EMBEDDING_MODEL_ID"],
    })
    record.write_data_json("metadata.json", metadata)
    print(f"Evidence directory: {record.root}")

    process = None
    status = "ERROR"
    detail = "Scenario did not complete setup"
    failures = []
    checks_performed = False
    session = None
    try:
        binary = build_server(ROOT, data_dir)

        port = free_port()
        base = f"http://127.0.0.1:{port}"
        server_env = isolated_server_env(data_dir, port, extra={
            "COGNITIVE_MAX_REQUESTS": str(case["limits"]["max_model_calls"]),
            "FOCUSED_WORK_MAX_ITERATIONS": str(case["limits"]["max_model_calls"]),
        })
        process = start_server(binary, data_dir, server_env, base, timeout=60)

        user = request(base, "PUT", "/api/user-profile", {"name": "Reminder request user"})
        llm = request(base, "POST", "/api/llm-configs", {
            "name": "Reminder test LLM",
            "model_id": os.environ["QINGQIU_TEST_LLM_MODEL_ID"],
            "base_url": os.environ["QINGQIU_TEST_LLM_BASE_URL"],
            "api_key": os.environ["QINGQIU_TEST_LLM_API_KEY"],
        })
        request(base, "PUT", "/api/embedding-config", {
            "name": "Reminder test embedding",
            "model_id": os.environ["QINGQIU_TEST_EMBEDDING_MODEL_ID"],
            "base_url": os.environ["QINGQIU_TEST_EMBEDDING_BASE_URL"],
            "api_key": os.environ["QINGQIU_TEST_EMBEDDING_API_KEY"],
        })
        # Embedding services initialize at startup; restart this owned process
        # after configuration before creating the Agent.
        stop_server(process, timeout=10)
        process = start_server(binary, data_dir, server_env, base, timeout=60)

        agent = request(base, "POST", "/api/agents", {
            "name": "Reminder keeper",
            "description": "Keeps the user's reminders at the requested time and content",
            "character_settings": "Reliable and literal about requested reminder times and content.",
            "llm_config_id": llm["id"],
        })
        path = CHAT_PATH + "?" + urllib.parse.urlencode({
            "agent_id": agent["id"], "message": case["request"]})
        chat = request(base, "POST", path)
        session = {"user_id": user["id"], "agent_id": agent["id"], "chat": chat,
                   "messages": [], "activities": [], "completed": False}
        record.write_data_json("session.json", session)

        db_path = data_dir / "product/db/database.db"
        alarms, saw_reply = wait_for_scheduled_event(
            base, db_path, chat["session_id"], agent["id"], process,
            case["limits"]["reminder_timeout_seconds"])
        if not alarms and not saw_reply:
            raise ScenarioError(
                "agent produced neither a reply nor a scheduled event within "
                f"{case['limits']['reminder_timeout_seconds']} seconds")

        messages = request(base, "GET", f"{MESSAGES_PATH}/{chat['session_id']}")
        activities = request(
            base, "GET",
            f"{ACTIVITIES_PATH}/{chat['session_id']}/activities?limit=200")
        session.update({"messages": messages, "activities": activities, "completed": True})
        record.write_data_json("session.json", session)
        status = "INCONCLUSIVE"
        detail = "Real product run completed; independent semantic judgment is required"
    except (ScenarioError, SetupError, KeyError, OSError, sqlite3.Error) as exc:
        detail = str(exc)
    finally:
        if process is not None:
            try:
                stop_server(process, timeout=10)
            except SetupError as exc:
                detail += f"; cleanup: {exc}"
        session_path = data_dir / "session.json"
        if session_path.exists():
            session = json.loads(session_path.read_text())
            db_path = data_dir / "product/db/database.db"
            try:
                evidence = read_db_evidence(db_path)
                record.write_data_json("db_evidence.json", evidence)
                if session.get("completed"):
                    failures = check_db_evidence(evidence, session, case)
                    checks_performed = True
                    if failures:
                        status = "FAIL"
                        detail = "Mechanical provenance checks failed: " + "; ".join(failures)
            except (OSError, sqlite3.Error, KeyError, TypeError, ValueError) as exc:
                if status != "ERROR":
                    status = "INCONCLUSIVE"
                detail += f"; read-only DB evidence unavailable or malformed: {exc}"
        finalize(record, case, metadata, status, detail, failures=failures,
                 checks_performed=checks_performed)
    print(f"{CASE_DIR.name}: {status}: {detail}")
    return {"PASS": 0, "FAIL": 1, "ERROR": 2, "INCONCLUSIVE": 3}[status]


def cli() -> int:
    """Run this case's owned executor."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.parse_args()
    return run()


if __name__ == "__main__":
    raise SystemExit(cli())
