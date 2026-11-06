"""Case-specific contract for relay-message-to-person.

The case exercises a chat-to-chat relay workflow, not RAG. Its scenario declares
a fixed chat request and the declared target person, and its result keeps the
shared semantic discipline (capture before judgment, explicit evidence, verdict
checks) without the RAG fixture, question, or work-interaction count.
"""

import json
from pathlib import Path
import re
from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field, field_validator, model_validator

from base_contracts import (
    Criterion,
    CriterionResult,
    Environment,
    Lifecycle,
    LimitValue,
    Revision,
    Status,
)


def _require(condition, message):
    """Raise when a contract rule does not hold."""
    if not condition:
        raise ValueError(message)


def _inside(path: Path, parent: Path):
    """Return whether path is parent or one of its descendants."""
    return path == parent or parent in path.parents


LIMIT_KEYS = frozenset({"relay_timeout_seconds", "max_model_calls"})


class Scenario(BaseModel):
    """One versioned relay-message scenario definition."""

    model_config = ConfigDict(strict=True, extra="forbid")

    version: int = Field(ge=1)
    lifecycle: Lifecycle
    request: str = Field(min_length=1)
    target_person_name: str = Field(min_length=1)
    required: list[Criterion] = Field(min_length=1)
    forbidden: list[Criterion] = Field(min_length=1)
    limits: dict[str, LimitValue] = Field(min_length=1)

    @field_validator("request", "target_person_name")
    @classmethod
    def nonblank(cls, value: str) -> str:
        """Reject blank scenario text."""
        _require(value.strip(), "scenario text fields must be nonempty")
        return value

    @model_validator(mode="after")
    def consistent_definition(self):
        """Reject duplicate criteria, unknown limits, and a name outside the request."""
        ids = [item.id for item in self.required + self.forbidden]
        _require(len(ids) == len(set(ids)), "duplicate criterion ID")
        _require(set(self.limits) == LIMIT_KEYS,
                 f"scenario limits must be exactly {sorted(LIMIT_KEYS)}")
        _require(self.target_person_name in self.request,
                 "scenario target person must appear in the request")
        return self

    def validate_context(self, path: Path, case_dir: Path) -> None:
        """Check the case directory and versioned scenario filename."""
        path = Path(path).resolve()
        case_dir = Path(case_dir).resolve()
        _require(path.parent == case_dir and case_dir.parent.name == "S" and
                 re.fullmatch(r"[a-z][a-z0-9]*(?:-[a-z0-9]+)*", case_dir.name) is not None,
                 f"{path}: invalid semantic case directory")
        _require(path.name == f"scenario-v{self.version}.json",
                 f"{path}: scenario filename must match its version")


class Result(BaseModel):
    """Capture and judgment record for one relay-message run."""

    model_config = ConfigDict(strict=True, extra="forbid")

    schema_version: Literal[1]
    run_id: str = Field(min_length=1)
    case_name: str = Field(min_length=1)
    scenario_version: int = Field(ge=1)
    scenario_sha256: str = Field(pattern=r"[0-9a-f]{64}")
    track: Literal["semantic"]
    status: Status
    detail: str
    execution_command: str = Field(min_length=1)
    started_at_utc: str = Field(min_length=1)
    ended_at_utc: str = Field(min_length=1)
    tested_revision: Revision
    finished_revision: Revision
    request: str = Field(min_length=1)
    target_person_name: str = Field(min_length=1)
    subject_entry: str = Field(min_length=1)
    model: dict[str, Any]
    environment: Environment
    limits: dict[str, LimitValue] = Field(min_length=1)
    observed_model_calls: int | None
    mechanical_failures: list[str]
    mechanical_checks_performed: bool
    judge: dict[str, Any] | None
    criteria: list[CriterionResult]
    artifacts: list[str]
    limitations: list[str]
    deviations: list[str] = Field(default_factory=list)

    def validate_context(self, run_dir: Path, case: dict, case_dir: Path) -> None:
        """Check scenario identity, evidence boundaries, and PASS conditions."""
        run_dir = Path(run_dir).resolve()
        case_dir = Path(case_dir).resolve()
        _require(self.case_name == case_dir.name and
                 self.scenario_version == case["version"] and
                 self.request == case["request"] and
                 self.target_person_name == case["target_person_name"] and
                 self.limits == case["limits"],
                 "result changed the scenario input or limits")
        _require(self.run_id == run_dir.name, "result run ID mismatch")
        _require("data/metadata.json" in self.artifacts and "report.md" in self.artifacts,
                 "metadata and report must be listed as artifacts")
        data_root = run_dir / "data"
        _require(data_root.is_dir() and
                 {path.name for path in run_dir.iterdir()} <= {"result.json", "report.md", "data"},
                 "run root must contain only result.json, report.md, and data/")
        for artifact in self.artifacts:
            _require(artifact and not Path(artifact).is_absolute(),
                     "artifact path must be relative")
            target = (run_dir / artifact).resolve()
            _require(_inside(target, run_dir) and target.is_file(),
                     f"artifact missing or outside this run: {artifact}")
            _require(artifact == "report.md" or _inside(target, data_root),
                     f"artifact outside data directory: {artifact}")
        expected = {item["id"] for item in case["required"] + case["forbidden"]}
        _require({item.id for item in self.criteria} == expected,
                 "result criteria do not match scenario")
        for item in self.criteria:
            for reference in item.evidence:
                _require(reference.artifact in self.artifacts,
                         f"invalid evidence reference in {item.id}")
        _require(not self.mechanical_failures or self.status == "FAIL",
                 "recorded mechanical failures require FAIL status")
        # The runner writes capture facts before judgment; the judge may append
        # reasoning but must not rewrite the captured mechanical facts.
        _require("data/capture.json" in self.artifacts,
                 "result must list the runner capture record")
        capture_path = data_root / "capture.json"
        _require(capture_path.is_file(), "result requires data/capture.json")
        capture = json.loads(capture_path.read_text())
        _require({"status", "mechanical_failures", "mechanical_checks_performed",
                  "observed_model_calls"} <= set(capture),
                 "capture record is incomplete")
        for field in ("mechanical_failures", "mechanical_checks_performed", "observed_model_calls"):
            _require(getattr(self, field) == capture[field],
                     f"judge changed captured field: {field}")
        _require(capture["status"] not in {"FAIL", "ERROR"} or self.status == capture["status"],
                 "judge cannot change the capture status")
        if self.status == "PASS":
            judge = self.judge
            _require(isinstance(judge, dict) and
                     judge.get("kind") in {"independent_agent", "same_session_agent"} and
                     isinstance(judge.get("model"), str) and judge["model"] and
                     judge.get("judgement") == "supported" and
                     isinstance(judge.get("rationale"), str) and judge["rationale"].strip(),
                     "PASS requires an explained, supported semantic judgment")
            _require(all(item.status == "PASS" and item.evidence for item in self.criteria),
                     "PASS requires evidence for every criterion")
            _require(not self.mechanical_failures, "PASS cannot have mechanical failures")
            _require(self.mechanical_checks_performed, "PASS requires mechanical checks")
            _require({"data/session.json", "data/db_evidence.json"} <= set(self.artifacts),
                     "PASS requires session and database evidence")
            if "data/calibration.json" in self.artifacts:
                calibration = json.loads((data_root / "calibration.json").read_text())
                _require(isinstance(calibration, list) and calibration,
                         "calibration must be a nonempty list")
                _require(all(isinstance(item, dict) and isinstance(item.get("claim"), str) and
                             item["claim"].strip() for item in calibration),
                         "each calibration entry needs the reference claim text")
                labels = {item.get("expected") for item in calibration if
                          item.get("expected") == item.get("observed")}
                _require({"supported", "contradicted", "insufficient_evidence"} <= labels,
                         "PASS with calibration requires correct reference judgments in all three categories")

    def validate_final(self) -> None:
        """Require a completed judgment for every evaluated, non-ERROR run."""
        if self.status == "ERROR":
            return
        judge = self.judge
        _require(isinstance(judge, dict) and
                 judge.get("kind") in {"independent_agent", "same_session_agent"} and
                 isinstance(judge.get("model"), str) and judge["model"].strip() and
                 judge.get("judgement") in {
                     "supported", "contradicted", "insufficient_evidence"} and
                 isinstance(judge.get("rationale"), str) and judge["rationale"].strip(),
                 "final result requires a completed semantic judgment")
        _require(all(item.observed.strip() for item in self.criteria),
                 "final result requires an observation for every criterion")
