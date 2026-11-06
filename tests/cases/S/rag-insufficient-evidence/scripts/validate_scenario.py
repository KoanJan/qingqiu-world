#!/usr/bin/env python3
"""Validate a committed scenario and its exact fixture before execution."""

from pathlib import Path
import sys

CASE_DIR = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(CASE_DIR.parent))
from base_contracts import load_case_contract, scenario_cli
import rag_contract

if __name__ == "__main__":
    contract = load_case_contract(CASE_DIR)
    raise SystemExit(scenario_cli(
        CASE_DIR, model=getattr(contract, "Scenario", rag_contract.Scenario)))
