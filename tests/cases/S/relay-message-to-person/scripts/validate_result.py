"""Validate a relay-message run's capture facts and final judgment."""

from pathlib import Path
import sys

HERE = Path(__file__).resolve().parent
CASE_DIR = HERE.parent
sys.path.insert(0, str(CASE_DIR.parent))
sys.path.insert(0, str(HERE))

from base_contracts import result_cli
from case_contract import Result, Scenario


if __name__ == "__main__":
    raise SystemExit(result_cli(CASE_DIR, scenario_model=Scenario, result_model=Result))
