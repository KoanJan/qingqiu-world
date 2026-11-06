#!/usr/bin/env python3
"""Validate a semantic run's scenario identity and artifact-backed result."""

from pathlib import Path
import sys

CASE_DIR = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(CASE_DIR.parent))
from base_contracts import ResultBase, load_case_contract, result_cli
import rag_contract

if __name__ == "__main__":
    contract = load_case_contract(CASE_DIR)
    raise SystemExit(result_cli(
        CASE_DIR,
        scenario_model=getattr(contract, "Scenario", rag_contract.Scenario),
        result_model=getattr(contract, "Result", ResultBase)))
