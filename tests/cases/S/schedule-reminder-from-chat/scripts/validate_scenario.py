"""Validate this case's versioned chat-to-reminder scenario definition."""

from pathlib import Path
import sys

HERE = Path(__file__).resolve().parent
CASE_DIR = HERE.parent
sys.path.insert(0, str(CASE_DIR.parent))
sys.path.insert(0, str(HERE))

from base_contracts import scenario_cli
from case_contract import Scenario


if __name__ == "__main__":
    raise SystemExit(scenario_cli(CASE_DIR, model=Scenario))
