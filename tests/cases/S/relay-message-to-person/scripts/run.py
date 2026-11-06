"""Run the relay-message-to-person case against a real, isolated product server.

Setup creates the requesting user, one subject Agent, and a second AI Person as
the scenario's declared target. The runner sends the fixed chat request, waits
for a message from the subject Agent in a session shared with the target, then
captures the session and read-only database rows that link the original
request, the delivery decision, the chat action, and the committed message.
Message meaning is left to an independent semantic judge.
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
ACTION_TYPE_CHAT = 0
EFFECT_TYPE_MESSAGE = 2

CHAT_PATH = "/api/chat/new"
MESSAGES_PATH = "/api/messages"
SUBJECT_ENTRY = (
    f"POST {CHAT_PATH}; POST /api/agents; GET {MESSAGES_PATH}/:id; read-only SELECT on "
    "messages, events, decisions, actions, action_effects, sessions, "
    "participant_sessions, persons"
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


def find_target_session_ids(db_path: Path, agent_id: int, target_id: int):
    """Return sessions whose participants include both the Agent and target."""
    rows = read_rows(db_path, """
        SELECT DISTINCT p1.session_id AS session_id
        FROM participant_sessions p1
        JOIN participant_sessions p2 ON p2.session_id = p1.session_id
        WHERE p1.participant_id = ? AND p2.participant_id = ?
        ORDER BY p1.session_id
        """, (agent_id, target_id))
    return [row["session_id"] for row in rows]


def find_relay_messages(db_path: Path, agent_id: int, target_id: int,
                        source_session_id: int):
    """Return nonblank messages the Agent sent in a session shared with the target."""
    return read_rows(db_path, """
        SELECT DISTINCT m.*
        FROM messages m
        JOIN participant_sessions p1 ON p1.session_id = m.session_id AND p1.participant_id = ?
        JOIN participant_sessions p2 ON p2.session_id = m.session_id AND p2.participant_id = ?
        WHERE m.person_id = ? AND m.session_id != ? AND length(trim(m.content)) > 0
        ORDER BY m.id
        """, (agent_id, target_id, agent_id, source_session_id))


def wait_for_relay(base: str, db_path: Path, session_id: int, agent_id: int,
                   target_id: int, process, limit: float):
    """Wait for a relay message or an Agent reply in the requesting session."""
    end = time.monotonic() + limit
    saw_reply = False
    while time.monotonic() < end:
        if process.poll() is not None:
            raise ScenarioError(f"server exited while waiting for a relay: {process.returncode}")
        relay = find_relay_messages(db_path, agent_id, target_id, session_id)
        if relay:
            return relay, saw_reply
        messages = request(base, "GET", f"{MESSAGES_PATH}/{session_id}")
        saw_reply = saw_reply or any(
            item.get("person_id") == agent_id and str(item.get("content", "")).strip()
            for item in messages)
        time.sleep(1)
    return [], saw_reply


def read_db_evidence(db_path: Path):
    """Capture every persistence table needed to inspect the relay chain."""
    conn = sqlite3.connect(f"file:{db_path}?mode=ro", uri=True, timeout=10)
    conn.row_factory = sqlite3.Row
    try:
        def rows(table: str):
            return [dict(row) for row in conn.execute(f"SELECT * FROM {table} ORDER BY id")]

        return {
            "persons": rows("persons"),
            "sessions": rows("sessions"),
            "participant_sessions": rows("participant_sessions"),
            "messages": rows("messages"),
            "events": rows("events"),
            "decisions": rows("decisions"),
            "actions": rows("actions"),
            "action_effects": rows("action_effects"),
        }
    finally:
        conn.close()


def check_db_evidence(evidence, session, case: dict):
    """Check record, state, and target facts; leave message meaning to the judge.

    Failure prefixes separate causes: `record` is an absent required persistence
    record or link, and `state` is product state contradicting the intended
    recipient, session, or delivery target.
    """
    failures = []
    session_id = session["chat"]["session_id"]
    agent_id = session["agent_id"]
    target_id = session["target_person_id"]
    user_id = session["user_id"]
    request_text = case["request"]

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

    memberships = {}
    for link in evidence["participant_sessions"]:
        memberships.setdefault(link["session_id"], set()).add(link["participant_id"])
    target_sessions = {sid for sid, people in memberships.items()
                       if people == {agent_id, target_id}}
    if not target_sessions:
        failures.append("state: no 1v1 session contains the Agent and the declared target person")
        return failures

    relay = [item for item in evidence["messages"]
             if item["session_id"] in target_sessions and item["person_id"] == agent_id and
             str(item.get("content", "")).strip()]
    if not relay:
        failures.append("record: no message from the Agent in a session shared with the target person")
        return failures

    relay_ids = {item["id"] for item in relay}
    effects = [item for item in evidence["action_effects"]
               if item["effect_type"] == EFFECT_TYPE_MESSAGE and item["effect_id"] in relay_ids]
    if not effects:
        failures.append("record: no action_effect links the delivered message to an action")
        return failures

    chats = [item for item in evidence["actions"]
             if item["id"] in {link["action_id"] for link in effects} and
             item["type"] == ACTION_TYPE_CHAT]
    if not chats:
        failures.append("record: no chat action is linked to the delivered message")
        return failures

    decisions = [item for item in evidence["decisions"]
                 if item["id"] in {action["decision_id"] for action in chats} and
                 item["person_id"] == agent_id]
    if not decisions:
        failures.append("record: no Agent decision backs the chat action")
        return failures

    # The plan must aim at this delivery: either the session that holds the
    # message, or the declared recipient when the plan creates a new 1v1 session.
    plan_ok = False
    for effect in effects:
        action = next((item for item in chats if item["id"] == effect["action_id"]), None)
        message = next((item for item in relay if item["id"] == effect["effect_id"]), None)
        if action is None or message is None:
            continue
        try:
            plan = json.loads(action["plan_json"] or "{}")
        except (TypeError, ValueError):
            continue
        if not isinstance(plan, dict):
            continue
        plan_session = plan.get("session_id")
        plan_recipient = plan.get("recipient_person_id")
        if isinstance(plan_session, int) and plan_session > 0:
            plan_ok = plan_ok or message["session_id"] == plan_session
        elif isinstance(plan_session, int) and plan_session < 0:
            plan_ok = plan_ok or plan_recipient == target_id
    if not plan_ok:
        failures.append("state: chat action plan does not target the target session or target person")
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
        "request": case["request"], "target_person_name": case["target_person_name"],
        "subject_entry": SUBJECT_ENTRY,
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
        f"Target person: `{case['target_person_name']}`\n\n"
        f"Command: `{metadata['execution_command']}`\n\n"
        f"Tested revision: `{metadata['git']['head']}`\n\n"
        f"Mechanical checks: {'completed' if checks_performed else 'not completed'}.\n\n"
        f"Mechanical failures: {'; '.join(failures) if failures else 'none recorded'}.\n\n"
        "Failure prefixes: `record` (required persistence or link absent), `state` (product "
        "state contradicts the intended recipient, session, or delivery target).\n\n"
        "Semantic criteria: awaiting independent judgment.\n\n"
        "## Evidence\n\n"
        "- `data/metadata.json`: scenario checksum, tested revision, provider models, limits.\n"
        "- `data/capture.json`: runner-captured mechanical facts, written before judgment.\n"
        "- `data/session.json`: requesting-session messages and target-session messages.\n"
        "- `data/db_evidence.json`: read-only persons, sessions, participant_sessions, "
        "messages, events, decisions, actions, and action_effects rows.\n"
        "- `data/build.log` and `data/server.log`: build and owned-process output.\n\n"
        "Inspect `data/db_evidence.json` for the original request and its event, the target "
        "session's Agent message, then its action_effects link to a chat action and its Agent "
        "decision. Record the independent semantic verdict and evidence locations here.\n")
    record.write_result(result, lambda value: validate_result(
        value, record.root, case, record.case_dir, model=Result, final=False))


def run() -> int:
    """Execute one isolated relay-message run."""
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

        user = request(base, "PUT", "/api/user-profile", {"name": "Relay request user"})
        llm = request(base, "POST", "/api/llm-configs", {
            "name": "Relay test LLM",
            "model_id": os.environ["QINGQIU_TEST_LLM_MODEL_ID"],
            "base_url": os.environ["QINGQIU_TEST_LLM_BASE_URL"],
            "api_key": os.environ["QINGQIU_TEST_LLM_API_KEY"],
        })
        request(base, "PUT", "/api/embedding-config", {
            "name": "Relay test embedding",
            "model_id": os.environ["QINGQIU_TEST_EMBEDDING_MODEL_ID"],
            "base_url": os.environ["QINGQIU_TEST_EMBEDDING_BASE_URL"],
            "api_key": os.environ["QINGQIU_TEST_EMBEDDING_API_KEY"],
        })
        # Embedding services initialize at startup; restart this owned process
        # after configuration before creating the Agents.
        stop_server(process, timeout=10)
        process = start_server(binary, data_dir, server_env, base, timeout=60)

        # The target is a second Person in this isolated world; the subject
        # Agent can only reach it by resolving the name in its contactable list.
        target = request(base, "POST", "/api/agents", {
            "name": case["target_person_name"],
            "description": "The person who should receive the relayed message.",
            "character_settings": "Calm and literal; answer only when someone addresses you.",
            "llm_config_id": llm["id"],
        })
        agent = request(base, "POST", "/api/agents", {
            "name": "Relay assistant",
            "description": "Relays the user's message to the named person without changing its key details.",
            "character_settings": "Careful and reliable about names, times, places, and details.",
            "llm_config_id": llm["id"],
        })
        path = CHAT_PATH + "?" + urllib.parse.urlencode({
            "agent_id": agent["id"], "message": case["request"]})
        chat = request(base, "POST", path)
        session = {"user_id": user["id"], "agent_id": agent["id"],
                   "target_person_id": target["id"], "chat": chat,
                   "source_messages": [], "target_sessions": [], "completed": False}
        record.write_data_json("session.json", session)

        db_path = data_dir / "product/db/database.db"
        relay, saw_reply = wait_for_relay(
            base, db_path, chat["session_id"], agent["id"], target["id"], process,
            case["limits"]["relay_timeout_seconds"])
        if not relay and not saw_reply:
            raise ScenarioError(
                "agent produced neither a reply nor a relay message within "
                f"{case['limits']['relay_timeout_seconds']} seconds")

        source_messages = request(base, "GET", f"{MESSAGES_PATH}/{chat['session_id']}")
        target_views = []
        for session_id in find_target_session_ids(db_path, agent["id"], target["id"]):
            target_views.append({"session_id": session_id,
                                 "messages": request(base, "GET", f"{MESSAGES_PATH}/{session_id}")})
        session.update({"source_messages": source_messages,
                        "target_sessions": target_views, "completed": True})
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
