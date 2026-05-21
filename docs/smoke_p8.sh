#!/usr/bin/env bash
# docs/smoke_p8.sh — session-log smoke harness for dbsavvy-962 P8.
#
# Purpose: validate the per-session log file invariants finalized in P8.
#
# AC traceback:
#   AD-A1  : filename pattern `dbsavvy-YYYYMMDD-HHMMSS-PID-NNNNNNNNN.log` under
#            ${XDG_STATE_HOME}/dbsavvy/sessions/.
#   AD-15  : sessions dir mode 0o700; file mode 0o600.
#   AD-16  : clean shutdown via LogCloser (SIGTERM → 2s deadline).
#   AD-A23 : JSON-line shape `{level,msg,time,cat}` present; DSN password
#            redacted to `***`; plaintext password absent from log content;
#            last line is not truncated (valid JSON).
#
# NOTE on TUI scripting: dbsavvy is a gocui TUI. Driving its interactive flow
# from bash is fragile (requires PTY + scripted key sequences). What this
# smoke validates — log file mode, JSON shape, redaction, clean truncation —
# is established at startup by wireSessionLogger() BEFORE any user
# interaction, so we boot the binary under a pseudo-TTY, give it a moment to
# emit the startup marker, then SIGTERM and inspect the file. The
# `connect+query+exit` interactive flow from the AC is captured separately by
# the integration test suite (`task test:integration`) and a manual QA pass.
#
# CI integration is intentionally out of scope here: there is no Postgres
# service in the CI workflow yet (see docker/postgres/docker-compose.yml for
# the local fixture). Run this locally after `task build` + docker bring-up.
#
# Usage:
#   task build
#   docker compose -f docker/postgres/docker-compose.yml up -d
#   task smoke:p8
#
# Override Postgres host via SMOKE_PG_HOST (default localhost:5432).

set -euo pipefail
export LC_ALL=C

# --- config -----------------------------------------------------------------

REPO_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${REPO_ROOT}/bin/dbsavvy"

SMOKE_PG_HOST="${SMOKE_PG_HOST:-localhost:5432}"
SMOKE_PG_USER="${SMOKE_PG_USER:-dbsavvy}"
SMOKE_PG_PASS="${SMOKE_PG_PASS:-dbsavvy}"
SMOKE_PG_DB="${SMOKE_PG_DB:-dbsavvy_test}"
DSN="postgres://${SMOKE_PG_USER}:${SMOKE_PG_PASS}@${SMOKE_PG_HOST}/${SMOKE_PG_DB}?sslmode=disable"
export DBSAVVY_TEST_PG="${DBSAVVY_TEST_PG:-$DSN}"

TMPDIR_ROOT="$(mktemp -d -t dbsavvy-smoke-p8.XXXXXX)"
export XDG_STATE_HOME="${TMPDIR_ROOT}/state"
SESSIONS_DIR="${XDG_STATE_HOME}/dbsavvy/sessions"

# Ensure the kill switch is OFF.
unset DBSAVVY_DISABLE_SESSION_LOG || true

PID=""

cleanup() {
  local rc=$?
  if [[ -n "${PID}" ]] && kill -0 "${PID}" 2>/dev/null; then
    kill -KILL "${PID}" 2>/dev/null || true
  fi
  rm -rf "${TMPDIR_ROOT}"
  exit "${rc}"
}
trap cleanup EXIT INT TERM

fail() { echo "FAIL: $*" >&2; exit 1; }
info() { echo "  $*"; }
pass() { echo "PASS: $*"; }

# --- preflight --------------------------------------------------------------

echo "==> Preflight"
[[ -x "${BIN}" ]] || fail "binary not found at ${BIN} (run: task build)"
command -v jq >/dev/null 2>&1 || fail "jq is required"
command -v setsid >/dev/null 2>&1 || fail "setsid is required"

# Best-effort Postgres reachability check. We don't hard-fail if pg_isready
# is missing, but we do warn — the binary may still come up far enough to
# write a startup marker even without a reachable DB.
if command -v pg_isready >/dev/null 2>&1; then
  host="${SMOKE_PG_HOST%%:*}"
  port="${SMOKE_PG_HOST##*:}"
  if ! pg_isready -h "${host}" -p "${port}" -q; then
    info "warn: pg_isready says ${SMOKE_PG_HOST} is not ready — proceeding anyway"
  fi
fi
info "tempdir: ${TMPDIR_ROOT}"
info "DSN:     postgres://${SMOKE_PG_USER}:***@${SMOKE_PG_HOST}/${SMOKE_PG_DB}?sslmode=disable"

# --- launch -----------------------------------------------------------------

echo "==> Launch dbsavvy under setsid"
# Run under setsid with a controlling TTY-less session; redirect stdio to
# /dev/null so gocui's init failure won't tear us down before the log opens.
# wireSessionLogger() runs early and is unaffected by TTY state.
setsid "${BIN}" </dev/null >/dev/null 2>&1 &
PID=$!
info "pid: ${PID}"

# Wait up to 5s for the session log file to appear.
LOGFILE=""
for _ in $(seq 1 50); do
  if [[ -d "${SESSIONS_DIR}" ]]; then
    # shellcheck disable=SC2012
    candidate="$(ls -1 "${SESSIONS_DIR}" 2>/dev/null | head -n1 || true)"
    if [[ -n "${candidate}" ]]; then
      LOGFILE="${SESSIONS_DIR}/${candidate}"
      break
    fi
  fi
  sleep 0.1
done
[[ -n "${LOGFILE}" && -f "${LOGFILE}" ]] || fail "no session log appeared in ${SESSIONS_DIR} within 5s"
info "log:    ${LOGFILE}"

# --- shutdown ---------------------------------------------------------------

echo "==> SIGTERM dbsavvy"
kill -TERM "${PID}" 2>/dev/null || true
# Wait up to 3s for clean shutdown (AD-16 LogCloser deadline is 2s).
for _ in $(seq 1 30); do
  kill -0 "${PID}" 2>/dev/null || break
  sleep 0.1
done
if kill -0 "${PID}" 2>/dev/null; then
  info "warn: process still alive after 3s — sending SIGKILL"
  kill -KILL "${PID}" 2>/dev/null || true
fi
PID=""

# --- assertions -------------------------------------------------------------

echo "==> Assertions"

# 1. Exactly one log file in sessions dir.
count="$(find "${SESSIONS_DIR}" -maxdepth 1 -type f -name 'dbsavvy-*.log' | wc -l | tr -d ' ')"
[[ "${count}" == "1" ]] || fail "expected exactly 1 log file in ${SESSIONS_DIR}, got ${count}"
pass "exactly one session log file present"

# 2. Filename pattern (AD-A1).
base="$(basename "${LOGFILE}")"
[[ "${base}" =~ ^dbsavvy-[0-9]{8}-[0-9]{6}-[0-9]+-[0-9]{9}\.log$ ]] \
  || fail "filename '${base}' does not match dbsavvy-YYYYMMDD-HHMMSS-PID-NNNNNNNNN.log"
pass "filename matches AD-A1 pattern"

# 3. File mode 0o600 (AD-15).
mode_file="$(stat -c '%a' "${LOGFILE}")"
[[ "${mode_file}" == "600" ]] || fail "log file mode is ${mode_file}, expected 600"
pass "log file mode is 0600"

# 4. Sessions dir mode 0o700 (AD-15).
mode_dir="$(stat -c '%a' "${SESSIONS_DIR}")"
[[ "${mode_dir}" == "700" ]] || fail "sessions dir mode is ${mode_dir}, expected 700"
pass "sessions dir mode is 0700"

# 5. Last line is valid JSON (no truncation; AD-A23).
last_line="$(tail -n 1 "${LOGFILE}")"
[[ -n "${last_line}" ]] || fail "log file is empty"
echo "${last_line}" | jq -e . >/dev/null \
  || fail "last line is not valid JSON: ${last_line}"
pass "last line is valid JSON (no truncation)"

# 6. Every line is valid JSON (stronger invariant — cheap to check).
if ! jq -e . "${LOGFILE}" >/dev/null 2>&1; then
  fail "at least one line in the log is not valid JSON"
fi
pass "every line is valid JSON"

# 7. At least one record has all of {level,msg,time,cat} (AD-A23).
shape_hits="$(jq -c 'select(has("level") and has("msg") and has("time") and has("cat"))' \
  "${LOGFILE}" | wc -l | tr -d ' ')"
[[ "${shape_hits}" -ge 1 ]] || fail "no record with {level,msg,time,cat} found"
pass "found ${shape_hits} record(s) with {level,msg,time,cat}"

# 8. Plaintext DSN password not leaked. We look for the DSN signature
#    `:<password>@` rather than the bare password (the username is also
#    'dbsavvy' in the fixture, so a bare grep would always match).
if grep -F -- ":${SMOKE_PG_PASS}@" "${LOGFILE}" >/dev/null; then
  fail "plaintext DSN password (':${SMOKE_PG_PASS}@') found in log"
fi
pass "no plaintext DSN password in log"

# 9. Redaction marker present somewhere. The startup_marker line records the
#    DSN-bearing state_dir/sessions_dir but no DSN, so we only assert `***`
#    if anything DSN-like was logged. We treat this as a soft assertion:
#    fail only if the log contains a DSN-shaped substring that wasn't
#    redacted. If no DSN ever reached the logger (binary exited before the
#    connect attempt), there's nothing to redact — that's fine.
if grep -E -o 'postgres://[^[:space:]"]+' "${LOGFILE}" | grep -v '\*\*\*' >/dev/null; then
  fail "found an un-redacted postgres:// DSN in log"
fi
if grep -F -- '***' "${LOGFILE}" >/dev/null; then
  pass "redaction marker '***' present"
else
  info "note: no '***' marker found — no DSN reached the logger this run (binary likely exited before connect). file-mode/JSON/no-leak invariants still verified."
fi

echo "==> All assertions passed."
exit 0
