#!/usr/bin/env python3
"""Run every deterministic cross-component case and report each exit status."""

from pathlib import Path
import subprocess
import sys


TRACK_DIR = Path(__file__).resolve().parent
ROOT = TRACK_DIR.parents[2]


def main() -> int:
    cases = sorted(path for path in TRACK_DIR.iterdir()
                   if path.is_dir() and path.name != "__pycache__")
    if not cases:
        print("D: no case directories found", file=sys.stderr)
        return 1

    results = []
    for case in cases:
        runner = case / "scripts" / "run.py"
        if not runner.is_file():
            print(f"{case.name}: ERROR: missing scripts/run.py", file=sys.stderr)
            results.append((case.name, "missing runner"))
            continue
        print(f"\n=== D/{case.name} ===", flush=True)
        try:
            completed = subprocess.run([sys.executable, str(runner)], cwd=ROOT)
            results.append((case.name, f"exit {completed.returncode}"))
        except OSError as exc:
            print(f"{case.name}: ERROR: {exc}", file=sys.stderr)
            results.append((case.name, "could not start"))

    print("\nD case results:")
    for name, result in results:
        print(f"  {name}: {result}")
    return 0 if all(result == "exit 0" for _, result in results) else 1


if __name__ == "__main__":
    sys.exit(main())
