"""Contracts for the current scripted RAG semantic cases.

Case runners supply their case directory and, optionally, a case-specific
Pydantic model. The models here describe the scripted RAG result format;
other S workflows can define contracts suited to their own evidence.
"""

import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import re
import sys
from typing import Annotated, Any, Literal, get_args

from pydantic import BaseModel, ConfigDict, Field, field_validator, model_validator

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
from tools import validate_report_status


Status = Literal["PASS", "FAIL", "INCONCLUSIVE", "ERROR"]
STATUSES = frozenset(get_args(Status))
Lifecycle = Literal["proposed", "reviewed", "active", "deprecated"]
LimitValue = Annotated[int, Field(gt=0)]


def _require(condition, message):
    if not condition:
        raise ValueError(message)


def _inside(path: Path, parent: Path):
    return path == parent or parent in path.parents


def load_case_contract(case_dir: Path):
    """Load optional case-specific models; return None when none are declared."""
    path = Path(case_dir) / "scripts" / "case_contract.py"
    if not path.is_file():
        return None
    name = f"case_contract_{Path(case_dir).name.replace('-', '_')}"
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class Criterion(BaseModel):
    """One required or forbidden outcome declared by a scenario."""

    model_config = ConfigDict(strict=True, extra="forbid")

    id: str = Field(min_length=1)
    condition: str = Field(min_length=1)

    @field_validator("id", "condition")
    @classmethod
    def nonblank(cls, value: str) -> str:
        _require(value.strip(), "criterion fields must be nonempty")
        return value


class ScenarioBase(BaseModel):
    """Fields in the current scripted RAG scenario format."""

    model_config = ConfigDict(strict=True, extra="forbid")

    version: int = Field(ge=1)
    lifecycle: Lifecycle
    fixture: str = Field(min_length=1)
    question: str = Field(min_length=1)
    required: list[Criterion] = Field(min_length=1)
    forbidden: list[Criterion] = Field(min_length=1)
    limits: dict[str, LimitValue] = Field(min_length=1)

    @field_validator("fixture", "question")
    @classmethod
    def nonblank(cls, value: str) -> str:
        _require(value.strip(), "scenario text fields must be nonempty")
        return value

    @model_validator(mode="after")
    def unique_criterion_ids(self):
        ids = [item.id for item in self.required + self.forbidden]
        _require(len(ids) == len(set(ids)), "duplicate criterion ID")
        return self

    def validate_context(self, path: Path, case_dir: Path) -> Path:
        """Check directory, filename, and fixture rules for one scenario."""
        path = Path(path).resolve()
        case_dir = Path(case_dir).resolve()
        _require(path.parent == case_dir and case_dir.parent.name == "S" and
                 re.fullmatch(r"[a-z][a-z0-9]*(?:-[a-z0-9]+)*", case_dir.name) is not None,
                 f"{path}: invalid semantic case directory")
        _require(path.name == f"scenario-v{self.version}.json",
                 f"{path}: scenario filename must match its version")
        fixture = (path.parent / self.fixture).resolve()
        _require(_inside(fixture, path.parent.resolve()) and fixture.is_file(),
                 f"{path}: fixture missing or outside its case directory")
        return fixture


class EvidenceReference(BaseModel):
    """One artifact location supporting a criterion verdict."""

    model_config = ConfigDict(strict=True, extra="forbid")

    artifact: str = Field(min_length=1)
    location: str = Field(min_length=1)

    @field_validator("artifact", "location")
    @classmethod
    def nonblank(cls, value: str) -> str:
        _require(value.strip(), "evidence reference fields must be nonempty")
        return value


class CriterionResult(BaseModel):
    """The recorded verdict and evidence for one scenario criterion.

    Status is per criterion, not a second copy of the run status. A forbidden
    criterion that did not occur is `PASS` with `observed` stating that the
    prohibited behavior did not happen; the run can still be `FAIL` elsewhere.
    """

    model_config = ConfigDict(strict=True, extra="forbid")

    id: str = Field(min_length=1)
    status: Status
    observed: str
    evidence: list[EvidenceReference]


class Revision(BaseModel):
    """Git identity captured before and after a run."""

    model_config = ConfigDict(strict=True, extra="forbid")

    head: str = Field(min_length=1)
    status: str
    tracked_diff_sha256: str


class Environment(BaseModel):
    """Platform identity of the process that executed the case."""

    model_config = ConfigDict(strict=True, extra="forbid")

    os: str = Field(min_length=1)
    arch: str = Field(min_length=1)
    python: str = Field(min_length=1)
    isolated_data_root: bool


class ResultBase(BaseModel):
    """Fields in the current scripted RAG result format."""

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
    fixture_sha256: str = Field(pattern=r"[0-9a-f]{64}")
    question: str = Field(min_length=1)
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
                 self.question == case["question"] and self.limits == case["limits"],
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
        fixture_artifact = f"data/{Path(case['fixture']).name}"
        if fixture_artifact in self.artifacts:
            _require(hashlib.sha256((run_dir / fixture_artifact).read_bytes()).hexdigest() ==
                     self.fixture_sha256, "captured fixture hash mismatch")
        expected = {item["id"] for item in case["required"] + case["forbidden"]}
        _require({item.id for item in self.criteria} == expected,
                 "result criteria do not match scenario")
        for item in self.criteria:
            for reference in item.evidence:
                _require(reference.artifact in self.artifacts,
                         f"invalid evidence reference in {item.id}")
        _require(not self.mechanical_failures or self.status == "FAIL",
                 "recorded mechanical failures require FAIL status")
        # The runner writes capture facts before judgment; a judge may append but
        # must not rewrite them, so the result is cross-checked against that record.
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
            _require({"data/session.json", "data/db_evidence.json", fixture_artifact} <=
                     set(self.artifacts),
                     "PASS requires session, database evidence, and exact source")
            _require(isinstance(self.observed_model_calls, int) and
                     self.observed_model_calls > 0,
                     "PASS requires an observed work-model interaction count")
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


def validate_scenario(case: dict, path: Path, case_dir: Path, *,
                      model: type[ScenarioBase] = ScenarioBase) -> ScenarioBase:
    """Validate one scenario with its base and optional case-specific model."""
    path = Path(path).resolve()
    case_dir = Path(case_dir).resolve()
    fields = set(model.model_fields)
    _require(isinstance(case, dict) and set(case) == fields,
             f"{path}: scenario fields must be exactly {sorted(fields)}")
    instance = model.model_validate(case)
    instance.validate_context(path, case_dir)
    return instance


def load_scenario(case_dir: Path, *, model: type[ScenarioBase] = ScenarioBase):
    """Return the single active scenario dict and its path for one case."""
    case_dir = Path(case_dir).resolve()
    matches = []
    for path in case_dir.glob("scenario-v*.json"):
        case = json.loads(path.read_text())
        instance = validate_scenario(case, path, case_dir, model=model)
        if instance.lifecycle == "active":
            matches.append((case, path))
    _require(len(matches) == 1,
             f"expected one active scenario in {case_dir.name}, found {len(matches)}")
    return matches[0]


def validate_result(result: dict, run_dir: Path, case: dict, case_dir: Path, *,
                    model: type[ResultBase] = ResultBase,
                    final: bool = True) -> ResultBase:
    """Validate capture facts, and require judgment unless checking a capture."""
    instance = model.model_validate(result)
    instance.validate_context(Path(run_dir), case, Path(case_dir))
    validate_report_status(Path(run_dir) / "report.md", instance.status)
    if final:
        instance.validate_final()
    return instance


def scenario_cli(case_dir: Path, *, model: type[ScenarioBase] = ScenarioBase) -> int:
    """Run the scenario validator CLI for one case directory."""
    parser = argparse.ArgumentParser(
        description="Validate a committed scenario and its exact fixture before execution.")
    parser.add_argument("--file", type=Path,
                        help="validate a case file, including proposed or reviewed cases")
    args = parser.parse_args()
    case_dir = Path(case_dir).resolve()
    try:
        if args.file is not None:
            path = args.file.resolve()
            if path.parent != case_dir:
                raise ValueError("scenario file must be inside this case directory")
            case = json.loads(path.read_text())
            validate_scenario(case, path, case_dir, model=model)
        else:
            case, path = load_scenario(case_dir, model=model)
    except (OSError, ValueError, KeyError, TypeError) as exc:
        print(f"INVALID: {exc}", file=sys.stderr)
        return 2
    print(f"VALID: {path.parent.name} v{case['version']} {case['lifecycle']} ({path})")
    return 0


def result_cli(case_dir: Path, *, scenario_model: type[ScenarioBase] = ScenarioBase,
               result_model: type[ResultBase] = ResultBase) -> int:
    """Run the result validator CLI for one case directory."""
    parser = argparse.ArgumentParser(
        description="Validate a semantic run's scenario identity and artifact-backed result.")
    parser.add_argument("--capture", action="store_true",
                        help="validate the runner's capture scaffold before judgment")
    parser.add_argument("run_dir", type=Path)
    args = parser.parse_args()
    case_dir = Path(case_dir).resolve()
    try:
        run_dir = args.run_dir.resolve()
        result = json.loads((run_dir / "result.json").read_text())
        case, scenario_path = load_scenario(case_dir, model=scenario_model)
        if hashlib.sha256(scenario_path.read_bytes()).hexdigest() != result["scenario_sha256"]:
            raise ValueError("scenario file changed since the run")
        if args.capture and result["status"] == "PASS":
            raise ValueError("a capture result cannot be PASS")
        validate_result(result, run_dir, case, case_dir, model=result_model,
                        final=not args.capture)
    except (OSError, ValueError, KeyError, TypeError) as exc:
        print(f"INVALID: {exc}", file=sys.stderr)
        return 2
    label = ("CAPTURE VALID" if args.capture else
             "DIAGNOSTIC VALID" if result["status"] == "ERROR" else "FINAL VALID")
    print(f"{label}: {result['case_name']} {result['status']} ({run_dir})")
    return 0 if not args.capture and result["status"] == "PASS" else 1
