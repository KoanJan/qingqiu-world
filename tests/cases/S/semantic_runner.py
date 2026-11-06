#!/usr/bin/env python3
"""Shared runner for scripted RAG semantic cases.

The case supplies its directory, scenario, and fixture. Optional case models
add rules to the shared RAG contract without copying the product workflow.
"""

import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import platform
import sqlite3
import subprocess
import sys
import time
import urllib.parse
import uuid

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[2]
sys.path.insert(0, str(HERE))
sys.path.insert(0, str(HERE.parent))
from base_contracts import ResultBase, load_case_contract, load_scenario, validate_result
from provider_env import missing_provider_env
import rag_contract
from tools import (HttpRequestError, RunRecord, SetupError, build_server,
                   free_port, git_state, http_json, isolated_server_env,
                   provider_endpoint, start_server, stop_server)


class ScenarioError(Exception):
    """A prerequisite or real product operation failed."""


def request(base: str, method: str, path: str, payload=None, content_type=None):
    """Return product data only when the API business code succeeds."""
    try:
        result = http_json(base, method, path, payload, content_type=content_type)
    except HttpRequestError as exc:
        raise ScenarioError(str(exc)) from exc
    if result.get("code") != 0:
        raise ScenarioError(f"{method} {path}: business code {result.get('code')}: {result.get('message')}")
    return result.get("data")


def upload_document(base: str, kb_id: int, fixture: Path):
    boundary = "qingqiu-" + uuid.uuid4().hex
    body = (
        f"--{boundary}\r\n"
        f'Content-Disposition: form-data; name="file"; filename="{fixture.name}"\r\n'
        "Content-Type: text/markdown\r\n\r\n"
    ).encode() + fixture.read_bytes() + f"\r\n--{boundary}--\r\n".encode()
    return request(base, "POST", f"/api/kb/{kb_id}/documents", body,
                   f"multipart/form-data; boundary={boundary}")


def wait_ready(base: str, kb_id: int, doc_id: int, process: subprocess.Popen, limit: float):
    end = time.monotonic() + limit
    while time.monotonic() < end:
        if process.poll() is not None:
            raise ScenarioError(f"server exited during document indexing: {process.returncode}")
        doc = request(base, "GET", f"/api/kb/{kb_id}/documents/{doc_id}")
        if doc["status"] == 3:
            raise ScenarioError(f"document processing failed: {doc.get('error_message', '')}")
        if doc["status"] == 2 and doc["active_revision_id"] > 0:
            return doc
        time.sleep(1)
    raise ScenarioError(f"document did not reach an active ready revision within {limit:g} seconds")


def wait_answer(base: str, messages_path: str, session_id: int, agent_id: int,
                process: subprocess.Popen, limit: float):
    end = time.monotonic() + limit
    while time.monotonic() < end:
        if process.poll() is not None:
            raise ScenarioError(f"server exited before answer: {process.returncode}")
        messages = request(base, "GET", f"{messages_path}/{session_id}")
        answers = [item for item in messages
                   if item.get("person_id") == agent_id and item.get("content", "").strip()]
        if answers:
            return messages, answers[-1]
        time.sleep(2)
    raise ScenarioError(f"agent produced no message within {limit:g} seconds")


def read_db_evidence(db_path: Path, session_id: int, doc_id: int, revision_id: int):
    conn = sqlite3.connect(f"file:{db_path}?mode=ro", uri=True)
    conn.row_factory = sqlite3.Row
    try:
        traces = [dict(row) for row in conn.execute(
            "SELECT * FROM kb_usage_traces WHERE session_id = ? ORDER BY id", (session_id,))]
        chunks = [dict(row) for row in conn.execute(
            "SELECT id, knowledge_base_id, document_id, revision_id, display_text, content "
            "FROM document_chunks WHERE document_id = ? AND revision_id = ? AND deleted = 0 ORDER BY id",
            (doc_id, revision_id))]
        document = conn.execute("SELECT * FROM documents WHERE id = ?", (doc_id,)).fetchone()
        grants = [dict(row) for row in conn.execute(
            "SELECT person_id, kb_id FROM kb_access ORDER BY id")]
        works = [dict(row) for row in conn.execute(
            "SELECT id, person_id, session_id, status FROM works WHERE session_id = ? ORDER BY id",
            (session_id,))]
        interactions = [dict(row) for row in conn.execute(
            "SELECT i.id, i.work_id, i.iteration, i.type, i.data FROM interactions i "
            "JOIN works w ON w.id = i.work_id WHERE w.session_id = ? ORDER BY i.id",
            (session_id,))]
        return {"document": dict(document) if document else None, "kb_access": grants,
                "works": works, "interactions": interactions,
                "kb_usage_traces": traces, "active_chunks": chunks}
    finally:
        conn.close()


def check_db_evidence(evidence, session, fixture: Path):
    """Check stable provenance facts; leave answer meaning to the independent judge.

    Failure prefixes separate causes: `record` is an absent required persistence
    record, `index` is a missing index product, `retrieval` is a completed lookup
    that returned nothing, and `state` is product state contradicting the setup.
    """
    failures = []
    document = evidence["document"]
    uploaded = session["document"]
    kb_id = session["kb_id"]
    agent_id = session["agent_id"]
    if not document or document["knowledge_base_id"] != kb_id or \
            document["status"] != 2 or document["active_revision_id"] != uploaded["active_revision_id"]:
        failures.append("state: document is not the active, ready document in the created KB")
    elif not Path(document["file_path"]).is_file() or \
            hashlib.sha256(Path(document["file_path"]).read_bytes()).digest() != \
            hashlib.sha256(fixture.read_bytes()).digest():
        failures.append("state: uploaded source bytes differ from the versioned fixture")
    if evidence["kb_access"] != [{"person_id": agent_id, "kb_id": kb_id}]:
        failures.append("state: agent does not have exactly the intended KB access grant")
    chunks = {row["id"]: row for row in evidence["active_chunks"]}
    if not chunks:
        failures.append("index: the active document has no indexed chunks")
    if not evidence["works"]:
        failures.append("record: no persisted work records for the chat session")
    traces = evidence["kb_usage_traces"]
    if not traces:
        failures.append("record: no KB retrieval trace was recorded for the chat session")
    for trace in traces:
        try:
            authorized = json.loads(trace["authorized_kb_ids_json"])
            handles = json.loads(trace["evidence_handles_json"])
        except (ValueError, TypeError) as exc:
            failures.append(f"record: trace {trace['id']} has invalid JSON evidence metadata: {exc}")
            continue
        if authorized != [kb_id]:
            failures.append(f"state: trace {trace['id']} has unexpected authorized KBs: {authorized}")
        for handle in handles:
            chunk = chunks.get(handle.get("chunk_id"))
            if not chunk or handle.get("kb_id") != kb_id or \
                    handle.get("document_id") != uploaded["id"] or \
                    handle.get("revision_id") != uploaded["active_revision_id"]:
                failures.append(f"state: trace {trace['id']} references an inaccessible or stale chunk")
    if traces and not any(trace["returned_evidence_count"] > 0 for trace in traces):
        failures.append("retrieval: KB retrieval returned no source evidence")
    return failures


def write_result(record: RunRecord, result_model, case: dict, metadata: dict, status: str,
                 detail: str, model_calls: int | None = None,
                 mechanical_failures: list | None = None,
                 mechanical_checks_performed: bool = False):
    criteria = [{"id": item["id"], "status": "INCONCLUSIVE", "observed": "",
                 "evidence": []} for item in case["required"] + case["forbidden"]]
    limitations = (["Independent semantic judgment has not run"]
                   if status == "INCONCLUSIVE" else [])
    if status != "ERROR":
        limitations.append(
            "observed_model_calls counts only persisted type=2 interactions in this "
            "session's works; it is not a total provider-call count. max_model_calls "
            "configures per-process cognitive requests and focused-work iterations, "
            "not a run-wide call cap")
    if model_calls is None and status != "ERROR":
        limitations.append(
            "Work-model interaction count could not be derived from persisted works and interactions")
    result = {
        "schema_version": 1, "scenario_version": case["version"],
        "scenario_sha256": metadata["scenario_sha256"],
        "track": "semantic", "status": status, "detail": detail,
        "execution_command": metadata["execution_command"],
        "started_at_utc": metadata["started_at_utc"],
        "ended_at_utc": datetime.now(timezone.utc).isoformat(),
        "tested_revision": metadata["git"],
        "finished_revision": git_state(ROOT, include_diff=True),
        "fixture_sha256": metadata["fixture_sha256"],
        "question": case["question"], "subject_entry": rag_contract.SUBJECT_ENTRY,
        "model": {"llm": metadata.get("llm_model_id"),
                  "embedding": metadata.get("embedding_model_id")},
        "environment": metadata["environment"],
        "limits": case["limits"], "observed_model_calls": model_calls,
        "mechanical_failures": mechanical_failures or [],
        "mechanical_checks_performed": mechanical_checks_performed,
        "judge": None, "criteria": criteria,
        "limitations": limitations, "deviations": [],
    }
    record.write_result(result, lambda value: validate_result(
        value, record.root, case, record.case_dir, model=result_model, final=False))


def run(case_dir: Path) -> int:
    CASE_DIR = Path(case_dir).resolve()
    case_contract = load_case_contract(CASE_DIR)
    scenario_model = getattr(case_contract, "Scenario", rag_contract.Scenario)
    result_model = getattr(case_contract, "Result", ResultBase)
    record = RunRecord(CASE_DIR)
    data_dir = record.data_dir
    started = datetime.now(timezone.utc).isoformat()
    command = f"{sys.executable} {Path(sys.argv[0]).resolve().relative_to(ROOT)}"
    try:
        case, scenario_path = load_scenario(CASE_DIR, model=scenario_model)
    except (OSError, ValueError, KeyError, TypeError) as exc:
        detail = f"Scenario setup failed for {CASE_DIR.name}: {exc}"
        record.write_report(
            f"# {CASE_DIR.name} setup\n\nStatus: ERROR\n\n{detail}\n\n"
            f"Command: `{command}`\n\n"
            "No product action ran; the scenario definition could not be validated.\n")
        record.write_result({
            "track": "semantic", "status": "ERROR", "detail": detail,
            "execution_command": command,
            "started_at_utc": started,
            "ended_at_utc": datetime.now(timezone.utc).isoformat(),
            "tested_revision": git_state(ROOT, include_diff=True),
            "limitations": ["Scenario definition could not be validated"],
        })
        print(f"{detail}\nEvidence directory: {record.root}", file=sys.stderr)
        return 2
    source_fixture = scenario_path.parent / case["fixture"]
    fixture_bytes = source_fixture.read_bytes()
    metadata = {
        "case_name": record.case_name, "scenario_version": case["version"],
        "scenario_sha256": hashlib.sha256(scenario_path.read_bytes()).hexdigest(),
        "fixture_sha256": hashlib.sha256(fixture_bytes).hexdigest(),
        "started_at_utc": started,
        "environment": {"os": platform.system(), "arch": platform.machine(),
                        "python": platform.python_version(), "isolated_data_root": True},
        "subject_entry_point": rag_contract.SUBJECT_ENTRY,
        "execution_command": command,
        "git": git_state(ROOT, include_diff=True), "run_dir": str(record.root),
    }
    missing = missing_provider_env()
    if missing:
        detail = "Missing required environment variables: " + ", ".join(missing)
        record.write_report(
            f"# {CASE_DIR.name} run\n\nStatus: ERROR\n\n{detail}\n\n"
            f"Command: `{metadata['execution_command']}`\n\n"
            f"Tested revision: `{metadata['git']['head']}`\n\n"
            "No product action ran. Inspect `data/metadata.json` for setup details.\n")
        record.write_data_json("metadata.json", metadata)
        record.write_data_json("capture.json", {
            "status": "ERROR", "mechanical_failures": [],
            "mechanical_checks_performed": False, "observed_model_calls": None,
        })
        write_result(record, result_model, case, metadata, "ERROR", detail)
        print(f"{detail}\nEvidence directory: {record.root}", file=sys.stderr)
        return 2

    fixture = data_dir / source_fixture.name
    fixture.write_bytes(fixture_bytes)
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
    mechanical_failures = []
    mechanical_checks_performed = False
    try:
        binary = build_server(ROOT, data_dir)

        port = free_port()
        base = f"http://127.0.0.1:{port}"
        server_env = isolated_server_env(data_dir, port, extra={
            "COGNITIVE_MAX_REQUESTS": str(case["limits"]["max_model_calls"]),
            "FOCUSED_WORK_MAX_ITERATIONS": str(case["limits"]["max_model_calls"]),
        })
        process = start_server(binary, data_dir, server_env, base, timeout=60)

        user = request(base, "PUT", "/api/user-profile", {"name": "RAG source test user"})
        llm = request(base, "POST", "/api/llm-configs", {
            "name": "RAG source test LLM",
            "model_id": os.environ["QINGQIU_TEST_LLM_MODEL_ID"],
            "base_url": os.environ["QINGQIU_TEST_LLM_BASE_URL"],
            "api_key": os.environ["QINGQIU_TEST_LLM_API_KEY"],
        })
        request(base, "PUT", "/api/embedding-config", {
            "name": "RAG source test embedding",
            "model_id": os.environ["QINGQIU_TEST_EMBEDDING_MODEL_ID"],
            "base_url": os.environ["QINGQIU_TEST_EMBEDDING_BASE_URL"],
            "api_key": os.environ["QINGQIU_TEST_EMBEDDING_API_KEY"],
        })
        # Background embedding services are initialized at startup, so restart
        # this owned process after initial configuration before Agent work.
        stop_server(process, timeout=10)
        process = start_server(binary, data_dir, server_env, base, timeout=60)
        agent = request(base, "POST", "/api/agents", {
            "name": "RAG source reader", "description": "Answer from the supplied KB source",
            "character_settings": "Use the KB source and explain uncertainty when it is incomplete.",
            "llm_config_id": llm["id"],
        })
        kb = request(base, "POST", "/api/kb", {
            "name": "RAG source test KB", "description": CASE_DIR.name,
        })
        doc = upload_document(base, kb["id"], fixture)
        doc = wait_ready(base, kb["id"], doc["id"], process,
                         case["limits"]["index_timeout_seconds"])
        request(base, "POST", f"/api/kb/{kb['id']}/access", {"person_id": agent["id"]})
        access = request(base, "GET", f"/api/kb/{kb['id']}/access")
        if not any(item.get("id") == agent["id"] for item in access):
            raise ScenarioError("agent KB grant was not visible")
        path = rag_contract.CHAT_PATH + "?" + urllib.parse.urlencode({
            "agent_id": agent["id"], "message": case["question"]})
        chat = request(base, "POST", path)
        messages, answer = wait_answer(base, rag_contract.MESSAGES_PATH, chat["session_id"], agent["id"],
                                       process, case["limits"]["answer_timeout_seconds"])
        if not any(item.get("id") == chat["message_id"] and
                   item.get("content") == case["question"] for item in messages):
            raise ScenarioError("session did not preserve the exact question")
        activities = request(base, "GET", f"{rag_contract.ACTIVITIES_PATH}/{chat['session_id']}/activities?limit=200")
        record.write_data_json("session.json", {
            "user_id": user["id"], "agent_id": agent["id"], "kb_id": kb["id"],
            "document": doc, "chat": chat, "messages": messages, "answer": answer,
            "activities": activities,
        })
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
        model_calls = None
        if session_path.exists():
            session = json.loads(session_path.read_text())
            db_path = data_dir / "product/db/database.db"
            try:
                evidence = read_db_evidence(
                    db_path, session["chat"]["session_id"], session["document"]["id"],
                    session["document"]["active_revision_id"])
                record.write_data_json("db_evidence.json", evidence)
                model_calls = (sum(item["type"] == 2 for item in evidence["interactions"])
                               if evidence["works"] else None)
                failures = check_db_evidence(evidence, session, fixture)
                mechanical_checks_performed = True
                if failures:
                    mechanical_failures = failures
                    status = "FAIL"
                    detail = "Mechanical provenance checks failed: " + "; ".join(failures)
            except (OSError, sqlite3.Error) as exc:
                status = "INCONCLUSIVE"
                detail += f"; read-only DB evidence unavailable: {exc}"
        record.write_data_json("capture.json", {
            "status": status, "mechanical_failures": mechanical_failures,
            "mechanical_checks_performed": mechanical_checks_performed,
            "observed_model_calls": model_calls,
        })
        record.write_report(
            f"# {CASE_DIR.name} v{case['version']} run\n\nStatus: {status}\n\n{detail}\n\n"
            f"Command: `{metadata['execution_command']}`\n\n"
            f"Tested revision: `{metadata['git']['head']}`\n\n"
            f"Mechanical checks: {'completed' if mechanical_checks_performed else 'not completed'}.\n\n"
            f"Mechanical failures: {'; '.join(mechanical_failures) if mechanical_failures else 'none recorded'}.\n\n"
            f"Observed work-model interactions: {model_calls if model_calls is not None else 'unknown'}; "
            "this excludes other model calls. `max_model_calls` configures per-process "
            "cognitive requests and focused-work iterations, not a run-wide call cap.\n\n"
            "Failure prefixes: `record` (required persistence absent), `index` (index product missing), "
            "`retrieval` (lookup returned no source evidence), `state` (product state contradicts setup).\n\n"
            "Semantic criteria: awaiting independent judgment.\n\n"
            "## Artifacts and checks\n\n"
            "- `data/metadata.json`: tested Git revision, run time, fixture checksum, and provider models.\n"
            "- `data/capture.json`: runner-captured mechanical facts, written before judgment.\n"
            f"- `data/{fixture.name}`: exact source uploaded through the API.\n"
            "- `data/session.json`: product responses, exact answer, and visible activities, if chat completed.\n"
            "- `data/db_evidence.json`: read-only document, access, chunk, trace, and raw interaction records, if available.\n"
            "- `data/build.log` and `data/server.log`: build and owned-process output.\n\n"
            "The runner checks active document state, uploaded source bytes, the sole KB grant, "
            "indexed chunks, retrieval scope, and evidence handle provenance. "
            "Review the exact answer against the versioned scenario criteria and original source. "
            "Record the independent semantic verdict, criterion reasoning, and evidence locations here.\n"
        )
        write_result(record, result_model, case, metadata, status, detail, model_calls,
                     mechanical_failures, mechanical_checks_performed)
    print(f"{CASE_DIR.name}: {status}: {detail}")
    return {"PASS": 0, "FAIL": 1, "ERROR": 2, "INCONCLUSIVE": 3}[status]


def cli(case_dir: Path) -> int:
    """Run the shared runner CLI for one case directory."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.parse_args()
    return run(case_dir)


if __name__ == "__main__":
    raise SystemExit("run this through a case directory's scripts/run.py")
