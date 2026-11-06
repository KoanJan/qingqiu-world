#!/usr/bin/env bash
set -euo pipefail

# Exercise the real CI script in a disposable tree so failure propagation is
# checked without running builds or touching developer data.
test_root="$(mktemp -d "${TMPDIR:-/tmp}/qingqiu-ci-test.XXXXXX")"
trap 'rm -rf "$test_root"' EXIT
mkdir -p "$test_root/ci/audit" "$test_root/server" "$test_root/web" "$test_root/bin"
cp "$(dirname "$0")/ci.sh" "$test_root/ci/ci.sh"

cat > "$test_root/bin/go" <<'SH'
#!/usr/bin/env bash
if [[ "$1" == test && "${FAIL_GO_TEST:-0}" == 1 ]]; then
  echo "simulated Go test failure" >&2
  exit 23
fi
if [[ "$1" == build && "${2:-}" == -o ]]; then
  printf '#!/bin/sh\nexit 0\n' > "$3"
  chmod +x "$3"
fi
exit 0
SH
cat > "$test_root/bin/npm" <<'SH'
#!/usr/bin/env bash
if [[ "${FAIL_WEB_BUILD:-0}" == 1 && "$1" == run && "${2:-}" == build ]]; then
  echo "simulated Web build failure" >&2
  exit 24
fi
exit 0
SH
cat > "$test_root/bin/npx" <<'SH'
#!/usr/bin/env bash
exit 0
SH
chmod +x "$test_root/bin/go" "$test_root/bin/npm" "$test_root/bin/npx"

run_case() {
  local name="$1" want_status="$2" fail_go="$3" fail_web="$4"
  local output="$test_root/$name.log"
  local status=0
  env PATH="$test_root/bin:$PATH" FAIL_GO_TEST="$fail_go" FAIL_WEB_BUILD="$fail_web" \
    QINGQIU_SKIP_CI_CONTRACT=1 \
    bash "$test_root/ci/ci.sh" > "$output" 2>&1 || status=$?
  if [[ "$want_status" == zero && "$status" -ne 0 ]] ||
     [[ "$want_status" == nonzero && "$status" -eq 0 ]]; then
    echo "$name: unexpected CI exit status $status" >&2
    cat "$output" >&2
    return 1
  fi
  echo "$name: CI exit status $status"
}

run_case all_checks_pass zero 0 0
run_case go_test_fails nonzero 1 0
run_case web_build_fails nonzero 0 1
