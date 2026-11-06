#!/usr/bin/env python3
"""Run this case through the shared S-track semantic runner."""

from pathlib import Path
import sys

CASE_DIR = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(CASE_DIR.parent))
from semantic_runner import cli

if __name__ == "__main__":
    raise SystemExit(cli(CASE_DIR))
