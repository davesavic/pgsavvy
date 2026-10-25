#!/usr/bin/env bash
set -euo pipefail

repo=$(cd "$(dirname "$0")/../.." && pwd -P)
PYTHONDONTWRITEBYTECODE=1 python3 "$repo/.github/scripts/cli-test-status.test.py"
temp=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/pgsavvy-fixture-test.XXXXXXXX")
trap 'rm -rf "$temp"' EXIT

# Exercise the production EXIT trap without downloading or starting PostgreSQL.
python3 - "$repo/.github/scripts/cli-postgres.sh" "$temp/cleanup.sh" "$temp/preflight.sh" <<'PY'
import pathlib, sys
source = pathlib.Path(sys.argv[1]).read_text()
start = source.index("cleanup() {")
end_marker = "trap 'exit 143' TERM"
end = source.index(end_marker, start) + len(end_marker)
pathlib.Path(sys.argv[2]).write_text(source[start:end] + "\n")
start = source.index('if [ "$host_os" = darwin ]; then', end)
end = source.index("\nfi", start) + len("\nfi")
assert end < source.index("stage=download")
pathlib.Path(sys.argv[3]).write_text(source[start:end] + "\n")
PY

python3 - "$temp/native-tests.jsonl" <<'PY'
import json, pathlib, sys
name = "TestMachineIO_DarwinFIFOEOF"
with pathlib.Path(sys.argv[1]).open("w") as log:
    log.write(json.dumps({"Action": "run", "Test": name}) + "\n")
    for _ in range(300):
        log.write(json.dumps({"Action": "output", "Test": name,
                              "Output": "private-output-sentinel" * 200}) + "\n")
    log.write(json.dumps({"Action": "fail", "Test": name + "/private-subtest-sentinel",
                          "Package": "private-package-sentinel"}) + "\n")
assert pathlib.Path(sys.argv[1]).stat().st_size > 1024 * 1024
PY

cat >"$temp/terminal-preflight.jsonl" <<'JSON'
{"Action":"run","Test":"TestMachineTerminalApprovalProcess"}
{"Action":"run","Test":"TestMachineTerminalApprovalProcess/idle"}
{"Action":"output","Output":"\t/private-path-sentinel/internal/machineio/stream_unix.go:58 +0x123\n"}
JSON

for stage in initdb terminal-process-preflight baseline-tests native-tests evidence; do
  for stop_result in 0 1; do
    for result in 0 37; do
      root="$temp/pgsavvy-native.$stage-$stop_result-$result"
      mkdir -p "$root/data" "$root/install/bin"
      touch "$root/data/postmaster.pid"
      cat >"$root/install/bin/pg_ctl" <<'SH'
#!/usr/bin/env bash
printf 'stopped\n' >"$STOP_MARKER"
echo 'private-stop-sentinel'
exit "$STOP_RESULT"
SH
      chmod +x "$root/install/bin/pg_ctl"
      export STOP_RESULT="$stop_result" STOP_MARKER="$temp/stopped"
      rm -f "$STOP_MARKER"
      cp "$temp/native-tests.jsonl" "$root/native-tests.jsonl"
      cp "$temp/terminal-preflight.jsonl" "$root/terminal-preflight.jsonl"
      set +e
      bash -euo pipefail -c '
        root=$1; pg="$root/install/bin"; stage=$2; repo=$5
        source "$3"
        bash -c '\''echo "{\"Action\":\"fail\",\"Test\":\"TestMachineCLIProcess_Query\",\"Output\":\"private-test-sentinel\"}"; echo private-stderr-sentinel >&2; exit "$1"'\'' _ "$4" >"$root/tests.jsonl" 2>"$root/tests.err"
      ' _ "$root" "$stage" "$temp/cleanup.sh" "$result" "$repo" >"$temp/public" 2>&1
      actual=$?
      set -e
      expected=$result
      [ -f "$STOP_MARKER" ]
      if [ "$stop_result" -ne 0 ]; then expected=1; fi
      [ "$actual" -eq "$expected" ]
      if [ "$result" -ne 0 ]; then
        grep -Eq "^native fixture failed: stage=$stage line=[0-9]+ exit=37$" "$temp/public"
        if [ "$stage" != initdb ]; then
          grep -q '"test": "TestMachineCLIProcess_Query", "status": "failed"' "$temp/public"
          grep -q '"test": "TestMachineIO_DarwinFIFOEOF", "status": "failed"' "$temp/public"
          grep -q '"case": "idle", "action": "run"' "$temp/public"
          grep -q '"file": "stream_unix.go", "line": 58' "$temp/public"
        fi
      fi
      if grep -q 'private-.*-sentinel' "$temp/public"; then
        echo 'private fixture output escaped' >&2; exit 1
      fi
      if [ "$stop_result" -eq 0 ]; then
        [ ! -e "$root" ]
      else
        [ -d "$root/data" ]
        grep -q '^owned cluster stop failed; retained fixture:' "$temp/public"
      fi
    done
  done
done

mkdir -p "$temp/tools"
cat >"$temp/tools/go" <<'SH'
#!/usr/bin/env bash
[ "$CGO_ENABLED" = 0 ]
printf '%s\n' "$*" >"$GO_ARGS"
cat "$GO_JSON"
echo 'private-preflight-stderr-sentinel' >&2
exit "$GO_RESULT"
SH
chmod +x "$temp/tools/go"
export GO_ARGS="$temp/go-args" GO_JSON="$temp/terminal-preflight.jsonl"
for host in darwin linux; do
  for result in 0 37; do
    root="$temp/pgsavvy-native.preflight-$host-$result"
    mkdir -p "$root"
    rm -f "$GO_ARGS" "$temp/continued"
    export GO_RESULT="$result"
    set +e
    PATH="$temp/tools:$PATH" bash -euo pipefail -c '
      repo=$1; root=$2; host_os=$3; pg="$root/no-server"; stage=manifest
      source "$4"
      source "$5"
      touch "$6"
    ' _ "$repo" "$root" "$host" "$temp/cleanup.sh" "$temp/preflight.sh" "$temp/continued" >"$temp/public" 2>&1
    actual=$?
    set -e
    [ ! -e "$root" ]
    if [ "$host" = darwin ]; then
      [ "$(cat "$GO_ARGS")" = "-C $repo test -json -count=1 -timeout 60s ./internal/machineio -run ^TestMachineTerminalApprovalProcess$" ]
      [ "$actual" -eq "$result" ]
    else
      [ ! -e "$GO_ARGS" ]
      [ "$actual" -eq 0 ]
    fi
    if [ "$host-$result" = darwin-37 ]; then
      [ ! -e "$temp/continued" ]
      grep -Eq '^native fixture failed: stage=terminal-process-preflight line=[0-9]+ exit=37$' "$temp/public"
      grep -q '"case": "idle", "action": "run"' "$temp/public"
      grep -q '"file": "stream_unix.go", "line": 58' "$temp/public"
    else
      [ -e "$temp/continued" ]
    fi
    if grep -q 'private-.*-sentinel' "$temp/public"; then
      echo 'private preflight output escaped' >&2; exit 1
    fi
  done
done

root="$temp/pgsavvy-native.reporter-failure"
mkdir -p "$root"
set +e
bash -euo pipefail -c '
  root=$1; pg="$root/no-server"; stage=native-tests; repo="$root/missing-reporter"
  source "$2"
  exit 19
' _ "$root" "$temp/cleanup.sh" >"$temp/public" 2>&1
actual=$?
set -e
[ "$actual" -eq 19 ]
[ ! -e "$root" ]
grep -q '^test status reporter unavailable$' "$temp/public"
echo 'Unix fixture failure diagnostics and cleanup passed'
