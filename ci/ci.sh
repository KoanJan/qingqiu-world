#!/usr/bin/env bash
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
FAIL=0

run_step() {
  local desc="$1"
  shift
  echo "  → $desc"
  if "$@"; then
    echo "    ✓ OK"
  else
    echo "    ✗ FAILED"
    FAIL=1
  fi
}

echo "=== CI: Backend (Go) ==="
if [ "${QINGQIU_SKIP_CI_CONTRACT:-0}" != 1 ]; then
  run_step "CI exit-status contract" bash "$ROOT/ci/test_ci_exit_status.sh"
fi
cd "$ROOT/server"

run_step "go build ./..."        go build ./...
run_step "go vet ./..."           go vet ./...

run_step "go test ./... (short)" go test ./... -count=1 -short


echo ""
echo "=== CI: Frontend (Web) ==="
cd "$ROOT/web"

run_step "npm ci"                npm ci --silent
run_step "tsc --noEmit"          npx tsc --noEmit
run_step "npm run build"         npm run build

echo ""
echo "=== CI: Audit ==="
cd "$ROOT/ci/audit"
run_step "go build audit"        go build -o "$ROOT/audit" ./cmd/audit/
cd "$ROOT"
run_step "audit scan"            ./audit scan

echo ""
if [ "$FAIL" -eq 0 ]; then
  echo "=== CI: All critical checks passed ==="
else
  echo "=== CI: Some checks failed ==="
  exit 1
fi
