#!/usr/bin/env bash
# Owned native Linux/macOS fixture; Windows retains its separate baseline.
set -euo pipefail

major=${1:?usage: cli-postgres.sh 17|18}
case "$major" in 17|18) ;; *) echo 'unsupported PostgreSQL major' >&2; exit 1 ;; esac
repo=$(cd "$(dirname "$0")/../.." && pwd -P)
case "$(uname -s)" in Linux) host_os=linux ;; Darwin) host_os=darwin ;; *) exit 1 ;; esac
case "$(uname -m)" in x86_64) host_arch=amd64 ;; aarch64|arm64) host_arch=arm64 ;; *) exit 1 ;; esac
if [ "$host_os" = darwin ] && [ "$(sysctl -in sysctl.proc_translated 2>/dev/null || true)" = 1 ]; then
  echo 'translated execution is not native evidence' >&2; exit 1
fi
for tool in go python3 curl tar make cc bison flex; do command -v "$tool" >/dev/null; done
for pair in "GOOS:$host_os" "GOARCH:$host_arch" "GOHOSTOS:$host_os" "GOHOSTARCH:$host_arch"; do
  [ "$(go env "${pair%%:*}")" = "${pair#*:}" ] || { echo 'non-native Go toolchain' >&2; exit 1; }
done
[ "${PGSAVVY_EXPECT_GOOS:-$host_os}" = "$host_os" ]
[ "${PGSAVVY_EXPECT_GOARCH:-$host_arch}" = "$host_arch" ]
[ "$(go env GOVERSION)" = go1.26.10 ] || { echo 'Go 1.26.10 required' >&2; exit 1; }

root=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/pgsavvy-native.XXXXXXXX")
pg="$root/install/bin"
stage=manifest
cleanup() {
  result=$1
  failure_line=$2
  trap - EXIT INT TERM
  if [ "$result" -ne 0 ]; then
    # Command text and captured logs can contain the private fixture password.
    printf 'native fixture failed: stage=%s line=%s exit=%s\n' "$stage" "$failure_line" "$result" >&2
    case "$stage" in
      terminal-process-preflight|ssh-tls-snapshot-probe|baseline-tests|native-tests|evidence)
        for log in terminal-preflight.jsonl ssh-tls-snapshot.jsonl tests.jsonl native-tests.jsonl; do
          python3 "$repo/.github/scripts/cli_test_status.py" "$root/$log" 2>/dev/null || echo 'test status reporter unavailable' >&2
        done
        ;;
    esac
  fi
  if [ -f "$root/data/postmaster.pid" ]; then
    if ! "$pg/pg_ctl" -D "$root/data" -m immediate -w -t 30 stop >"$root/stop.log" 2>&1; then
      echo "owned cluster stop failed; retained fixture: $root" >&2
      exit 1
    fi
  fi
  # root comes only from this invocation's mktemp, never caller-supplied input.
  python3 - "$root" <<'PY'
import pathlib, shutil, sys
root = pathlib.Path(sys.argv[1])
if root.is_symlink() or not root.name.startswith("pgsavvy-native."):
    raise SystemExit("refusing unexpected cleanup target")
shutil.rmtree(root)
assert not root.exists(), "owned fixture survived cleanup"
print('{"scope":"owned-native-cleanup","cluster_stopped":true,"root_removed":true,"socket_removed":true}')
PY
  exit "$result"
}
trap 'cleanup "$?" "$LINENO"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

if [ "$host_os" = darwin ]; then
  stage=terminal-process-preflight
  CGO_ENABLED=0 go -C "$repo" test -json -count=1 -timeout 60s ./internal/machineio \
    -run '^TestMachineTerminalApprovalProcess$' >"$root/terminal-preflight.jsonl" 2>"$root/terminal-preflight.err"
  stage=ssh-tls-snapshot-probe
  CGO_ENABLED=0 go -C "$repo" test -json -count=50 -timeout 60s ./pkg/drivers/pg \
    -run '^TestMachineOpen_TargetSnapshot_SSHTLS$' >"$root/ssh-tls-snapshot.jsonl" 2>"$root/ssh-tls-snapshot.err"
fi

stage=manifest
python3 - "$repo/.github/scripts/cli-postgres-versions.json" "$major" >"$root/pin" <<'PY'
import json, re, sys
with open(sys.argv[1], encoding="utf-8") as f:
    manifest = json.load(f)
assert manifest["schema_version"] == 1
entry = manifest["postgres"][sys.argv[2]]
pin = entry["unix"]
assert re.fullmatch(r"[0-9a-f]{64}", pin["sha256"])
version = entry["version"]
assert re.fullmatch(re.escape(sys.argv[2]) + r"\.\d+", version)
assert pin["url"] == f"https://ftp.postgresql.org/pub/source/v{version}/postgresql-{version}.tar.bz2"
print(version)
print(pin["url"])
print(pin["sha256"])
PY
{ IFS= read -r version; IFS= read -r url; IFS= read -r digest; } <"$root/pin"
stage=download
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
  --connect-timeout 30 --max-time 600 "$url" -o "$root/postgres.tar.bz2"
stage=digest
python3 - "$root/postgres.tar.bz2" "$digest" <<'PY'
import hashlib, sys
with open(sys.argv[1], "rb") as f:
    digest = hashlib.sha256()
    for chunk in iter(lambda: f.read(1024 * 1024), b""):
        digest.update(chunk)
    actual = digest.hexdigest()
if actual != sys.argv[2]:
    raise SystemExit("PostgreSQL artifact digest mismatch")
PY
stage=extraction
tar -xjf "$root/postgres.tar.bz2" -C "$root"
stage=postgres-build
(
  cd "$root/postgresql-$version"
  ./configure --prefix="$root/install" --without-icu --without-readline --without-zlib
  make -j2
  make install
) >"$root/build.log" 2>&1 || { result=$?; echo 'owned PostgreSQL build failed' >&2; exit "$result"; }
stage=postgres-tools
python3 - "$pg" "$host_arch" <<'PY'
import pathlib, struct, sys
for name in ("psql", "postgres"):
    with (pathlib.Path(sys.argv[1]) / name).open("rb") as f:
        header = f.read(20)
    if header[:4] == b"\x7fELF":
        endian = "<" if header[5] == 1 else ">"
        arch = {62: "amd64", 183: "arm64"}.get(struct.unpack(endian + "H", header[18:20])[0])
    elif header[:4] == b"\xcf\xfa\xed\xfe":
        arch = {0x1000007: "amd64", 0x100000c: "arm64"}.get(struct.unpack("<I", header[4:8])[0])
    else:
        raise SystemExit("unsupported PostgreSQL executable format")
    if arch != sys.argv[2]:
        raise SystemExit("non-native PostgreSQL executable")
PY
[ "$("$pg/psql" --version)" = "psql (PostgreSQL) $version" ]

# Ignore ambient libpq configuration, including user-owned service/password files.
stage=environment
unset PGHOST PGHOSTADDR PGPORT PGDATABASE PGUSER PGPASSWORD PGSERVICE PGOPTIONS
export PGSERVICEFILE="$root/no-services" PGPASSFILE="$root/no-pgpass" PGCONNECT_TIMEOUT=5
export PGSSLMODE=disable
export XDG_CONFIG_HOME="$root/config" XDG_DATA_HOME="$root/share" XDG_STATE_HOME="$root/state"
export XDG_CACHE_HOME="$root/cache"
password=$(python3 -c 'import secrets; print(secrets.token_hex(32))')
printf '%s\n' "$password" >"$root/password"
chmod 600 "$root/password"
stage=initdb
"$pg/initdb" -D "$root/data" -U pgsavvy --encoding=UTF8 --no-locale \
  --auth-local=scram-sha-256 --auth-host=scram-sha-256 --pwfile="$root/password" >"$root/init.log" 2>&1
port=$(python3 - <<'PY'
import socket
with socket.socket() as s:
    s.bind(("127.0.0.1", 0))
    print(s.getsockname()[1])
PY
)
cat >>"$root/data/postgresql.conf" <<EOF
listen_addresses = '127.0.0.1'
port = $port
unix_socket_directories = '$root/socket'
unix_socket_permissions = 0700
max_prepared_transactions = 20
EOF
mkdir -m 700 "$root/socket"
stage=server-start
"$pg/pg_ctl" -D "$root/data" -l "$root/server.log" -w -t 30 start >"$root/start.log" 2>&1
export PGPASSWORD="$password"
stage=createdb
"$pg/createdb" -h 127.0.0.1 -p "$port" -U pgsavvy pgsavvy_test >"$root/createdb.log" 2>&1
psql=("$pg/psql" -X -w -h 127.0.0.1 -p "$port" -U pgsavvy -d pgsavvy_test -v ON_ERROR_STOP=1)
stage=fixture
"${psql[@]}" -f "$repo/docker/postgres/init/01_fixture.sql" >"$root/fixture.log" 2>&1
stage=temporary-fixture
temporary_password=$(python3 -c 'import secrets; print(secrets.token_hex(32))')
# Only this invocation's disposable cluster is provisioned; the shipping CLI
# receives an unprivileged login, never the observer/admin credential.
{
printf "CREATE ROLE pgsavvy_temporary LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '%s';\n" "$temporary_password"
cat <<'SQL'
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
REVOKE TEMP ON DATABASE pgsavvy_test FROM PUBLIC;
CREATE SCHEMA temporary_cli;
CREATE TABLE temporary_cli.effects(id integer PRIMARY KEY, value text);
CREATE TABLE temporary_cli.deferred(id integer UNIQUE DEFERRABLE INITIALLY DEFERRED);
CREATE TABLE temporary_cli.children(id integer PRIMARY KEY, parent integer REFERENCES temporary_cli.effects(id));
CREATE SEQUENCE temporary_cli.attempts;
CREATE SEQUENCE temporary_cli.dispatch_guard;
CREATE SEQUENCE temporary_cli.rollback_attempts;
GRANT USAGE ON SCHEMA temporary_cli TO pgsavvy_temporary;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA temporary_cli TO pgsavvy_temporary;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA temporary_cli TO pgsavvy_temporary;
SQL
} | "${psql[@]}" >"$root/temporary-fixture.log" 2>&1
printf '127.0.0.1:%s:pgsavvy_test:pgsavvy_temporary:%s\nlocalhost:%s:pgsavvy_test:pgsavvy_temporary:%s\n' \
  "$port" "$temporary_password" "$port" "$temporary_password" >"$root/temporary.pass"
chmod 600 "$root/temporary.pass"
export PGSAVVY_TEMP_FIXTURE_ROOT="$root" PGSAVVY_TEMP_FIXTURE_PASSFILE="$root/temporary.pass"
export PGSAVVY_TEMP_FIXTURE_SOCKET="$root/socket"
export PGSAVVY_TEMP_FIXTURE_TOKEN=$(python3 -c 'import secrets; print(secrets.token_hex(32))')
# Prove SCRAM over both owned transports, including socket password rejection.
for endpoint in 127.0.0.1 "$root/socket"; do
  PGPASSWORD="$temporary_password" "$pg/psql" -X -w -h "$endpoint" -p "$port" -U pgsavvy_temporary -d pgsavvy_test \
    -v ON_ERROR_STOP=1 -Atc 'SELECT 1' >"$root/temporary-ready" 2>"$root/temporary-ready.err"
  [ "$(cat "$root/temporary-ready")" = 1 ]
  if PGPASSWORD=deliberately-wrong "$pg/psql" -X -w -h "$endpoint" -p "$port" -U pgsavvy_temporary -d pgsavvy_test \
    -Atc 'SELECT 1' >"$root/temporary-wrong" 2>"$root/temporary-wrong.err"; then
    echo 'temporary wrong password accepted' >&2; exit 1
  fi
  grep -q 'password authentication failed' "$root/temporary-wrong.err"
done
stage=readiness
"${psql[@]}" -Atc 'SELECT 1' >"$root/ready" 2>"$root/ready.err"
[ "$(cat "$root/ready")" = 1 ]
[ "$("${psql[@]}" -Atc 'SHOW max_prepared_transactions' 2>"$root/prepared.err")" = 20 ]
stage=authentication
if PGPASSWORD=deliberately-wrong "${psql[@]}" -Atc 'SELECT 1' >"$root/wrong.out" 2>"$root/wrong.err"; then
  echo 'wrong password accepted' >&2; exit 1
fi
grep -q 'password authentication failed' "$root/wrong.err" || { echo 'wrong-password check did not reach authentication' >&2; exit 1; }
stage=fixture-facts
facts=$("${psql[@]}" -Atc "SELECT current_setting('server_version_num'), current_setting('server_encoding'), (SELECT version FROM app._fixture_meta), rolsuper FROM pg_roles WHERE rolname = current_user" 2>"$root/facts.err")
IFS='|' read -r server encoding fixture superuser <<<"$facts"
if [ "$((server / 10000))" != "$major" ] || [ "$encoding" != UTF8 ] || [ "$fixture" != 2 ] || [ "$superuser" != t ]; then
  echo 'fixture version/auth/encoding mismatch' >&2; exit 1
fi
export PGSAVVY_TEST_PG="postgres://pgsavvy:$password@127.0.0.1:$port/pgsavvy_test?sslmode=disable"
stage=temporary-ownership
"${psql[@]}" -Atc "SELECT system_identifier::text, current_setting('data_directory') FROM pg_control_system()" >"$root/temporary-identity" 2>"$root/temporary-identity.err"
python3 - "$root" <<'PY'
import json, os, pathlib, sys
root = pathlib.Path(sys.argv[1])
identifier, directory = (root / "temporary-identity").read_text().strip().split("|")
assert identifier.isdecimal() and int(identifier) > 0
assert pathlib.Path(directory).resolve() == (root / "data").resolve()
metadata = {"token": os.environ["PGSAVVY_TEMP_FIXTURE_TOKEN"], "observer_dsn": os.environ["PGSAVVY_TEST_PG"],
            "system_identifier": identifier, "data_directory": str((root / "data").resolve())}
fd = os.open(root / "temporary-owner", os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
with os.fdopen(fd, "w") as owned:
    json.dump(metadata, owned)
PY
export CGO_ENABLED=0
cd "$repo"
stage=probe
go run ./internal/pgprobe >"$root/probe.log" 2>&1
binary="$root/pgsavvy"
stage=cli-build
go build -ldflags '-X main.version=native-preflight -X main.buildSource=fixture' -o "$binary" . >"$root/go-build.log" 2>&1
export PGSAVVY_TEST_BINARY="$binary"
stage=cli-version
"$binary" --version >"$root/version" 2>"$root/version.err"
grep -q 'native-preflight' "$root/version"
selector='^TestMachineCLI_(Dispatch|ExitCodes|Envelope|Envelope_ParentDeadlineAndInput|OutputLimit|BrokenWriter)$'
stage=baseline-tests
go test -json -count=1 -timeout 5m ./pkg/cli -run "$selector" >"$root/tests.jsonl" 2>"$root/tests.err"
stage=native-tests
# Inventory the platform's compiled tests, including every history/approval/
# executor regression. Evidence must show each actually passed, never skipped.
native_selector='^(TestMachine|TestCLIHistory|TestTemporary)'
go test -json -list "$native_selector" -tags=integration \
  . ./internal/machineio ./pkg/cli ./pkg/drivers/pg ./pkg/session ./pkg/session/sshtunnel ./test/integration \
  >"$root/native-inventory.jsonl" 2>"$root/native-inventory.err"
go test -json -count=1 -timeout 10m -tags=integration \
  . ./internal/machineio ./pkg/cli ./pkg/drivers/pg ./pkg/session ./pkg/session/sshtunnel ./test/integration \
  -run "$native_selector" >"$root/native-tests.jsonl" 2>"$root/native-tests.err"
stage=evidence
PYTHONDONTWRITEBYTECODE=1 python3 - "$root" "$host_os" "$host_arch" "$version" "$server" "$repo" <<'PY'
import hashlib, json, pathlib, re, subprocess, sys
sys.path.insert(0, str(pathlib.Path(sys.argv[6]) / ".github/scripts"))
from cli_test_status import TEST_NAMES
root = pathlib.Path(sys.argv[1])
public_packages = {"github.com/davesavic/pgsavvy" + suffix for suffix in (
    "", "/internal/machineio", "/pkg/cli", "/pkg/drivers/pg", "/pkg/session",
    "/pkg/session/sshtunnel", "/test/integration")}
def public_names(values):
    return sorted({value for value in values
        if value.partition("::")[0] in public_packages
        and value.partition("::")[2] in TEST_NAMES})
required = {"TestMachineCLI_" + n for n in ("Dispatch", "ExitCodes", "Envelope", "Envelope_ParentDeadlineAndInput", "OutputLimit", "BrokenWriter")}
passed, baseline_passed = set(), set()
for line in (root / "tests.jsonl").read_text().splitlines():
    event = json.loads(line)
    if event["Action"] in ("skip", "fail"):
        raise SystemExit("baseline contains skipped/failed tests")
    if event["Action"] == "pass" and "Test" in event:
        passed.add(event["Test"])
        baseline_passed.add(event["Package"] + "::" + event["Test"])
if not required <= passed:
    raise SystemExit("required baseline tests missing")
required_native = {
    "TestMachineCLINative_Discovery", "TestMachineCLINative_ProfilePolicyAndInput",
    "TestMachineCLINative_ShellPipe", "TestMachineCLINative_HelperExecutable",
    "TestMachineCLINative_GroupSignal",
    "TestMachineCLINative_InvalidInputHandle",
    "TestMachineIO_Ownership", "TestMachineIO_Cancellation", "TestMachineIO_Sockets",
    "TestMachineIO_TerminalFlags", "TestMachineIO_DarwinFIFOEOF",
    "TestMachineIO_ProcessCancellation", "TestMachineIO_ProcessStartupCancellation",
    "TestMachineIO_ProcessSocketCancellation",
    "TestMachineIO_ProcessOutputAcknowledged", "TestMachineIO_ProcessProtocolFailure",
    "TestMachineIO_ProcessClosedStandardSlots", "TestMachineCredentials_Precedence",
    "TestMachineCredentials_NoPrompt", "TestMachineCredentials_KeyringExactIdentity",
    "TestMachineSSH_Trust", "TestMachineSSH_Auth", "TestMachineSSH_AgentCancellation",
    "TestMachineSSH_OwnedAbort", "TestMachineCLIProcess_Discovery", "TestMachineCLIProcess_Test",
    "TestMachineCLIProcess_Query", "TestMachineCLIProcess_Schema", "TestMachineCLIProcess_Signals",
    "TestMachineCLIFullApprovalProcess", "TestMachineCLIFullPostgresTerminal",
    "TestMachineFullQueryProcessTransactions", "TestMachineFullQueryProcessRestrictedPolicy",
    "TestMachineFullQueryProcessDeliveryAndDeadline",
    "TestMachineCLIBatchAnalysisPostgresDifferential",
    "TestMachineExecutePostgresTransactions", "TestMachineExecutePostgresLimits",
    "TestMachineExecutePostgresPolicyAndMaintenance", "TestMachineExecutePostgresCancelCommit",
    "TestTemporaryAtomicPostgres", "TestTemporaryCLIIntegration", "TestTemporaryCLILexicalPostgres",
    "TestTemporaryCLIProcessIsolation", "TestTemporaryCLIProcessCommitInterruption",
    "TestTemporaryCLIProcessBrokenStdout",
    "TestTemporaryCLIObserverOwnership", "TestTemporaryCLIProcessObserverOwnership",
}
inventory = set()
for line in (root / "native-inventory.jsonl").read_text().splitlines():
    event = json.loads(line)
    name = event.get("Output", "").strip()
    if event["Action"] == "output" and name.startswith(("TestMachine", "TestCLIHistory", "TestTemporary")) and "/" not in name:
        inventory.add(event["Package"] + "::" + name)
for prefix in ("TestMachineExecute", "TestMachineApproval", "TestCLIHistory", "TestTemporary"):
    if not any(item.split("::", 1)[1].startswith(prefix) for item in inventory):
        raise SystemExit("required test family missing: " + prefix)
native_passed, native_names = set(), set()
native_pass_events = 0
interrupted_rows = broken_stdout_exit = None
rollback_observations = set()
for line in (root / "native-tests.jsonl").read_text().splitlines():
    event = json.loads(line)
    if event["Action"] in ("skip", "fail"):
        raise SystemExit("native machine suite contains skipped/failed tests")
    if event["Action"] == "pass" and "Test" in event:
        native_pass_events += 1
        native_names.add(event["Test"])
        native_passed.add(event["Package"] + "::" + event["Test"])
    if event["Action"] == "output" and event.get("Test") == "TestTemporaryCLIProcessCommitInterruption":
        match = re.search(r"interrupted COMMIT attempts=1, reported=unknown, eventual observer rows=([01]) \(not prescribed\)", event.get("Output", ""))
        if match:
            interrupted_rows = int(match[1])
    if event["Action"] == "output" and event.get("Test") == "TestTemporaryCLIProcessBrokenStdout":
        match = re.search(r"broken stdout exit=([1-9][0-9]*); independent observer durable rows=1 attempts=1", event.get("Output", ""))
        if match:
            broken_stdout_exit = int(match[1])
    if event["Action"] == "output" and event.get("Test", "").startswith("TestTemporaryCLIIntegration/"):
        match = re.search(r"rollback proof: id=(101[0-4]) marker_attempts=1 backend_terminated=true rows=0", event.get("Output", ""))
        if match:
            rollback_observations.add(int(match[1]))
if not required_native <= native_names:
    raise SystemExit("required native machine tests missing")
if not inventory <= native_passed:
    raise SystemExit("compiled native/history inventory not fully passed")
if interrupted_rows is None or broken_stdout_exit is None:
    raise SystemExit("temporary interruption/publication observations missing")
if rollback_observations != set(range(1010, 1015)):
    raise SystemExit("temporary independent rollback observations missing")
binary = root / "pgsavvy"
print(json.dumps({"scope": "machine-cli-linux-darwin", "goos": sys.argv[2], "goarch": sys.argv[3],
    "go_version": subprocess.check_output(["go", "version"], text=True).strip(), "cgo_enabled": "0",
    "binary": str(binary), "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
    "postgres_tools": sys.argv[4], "postgres_tools_arch": sys.argv[3], "server_version_num": sys.argv[5], "fixture_version": 2,
    "wrong_password_rejected": True, "max_prepared_transactions": 20,
    "temporary_scram_tcp_socket": True, "temporary_passfile_mode": "0600",
    "temporary_observer_ownership": "exact-dsn-and-cluster-identity",
    "native_pass_events": native_pass_events, "native_skip_events": 0, "native_fail_events": 0,
    "temporary_independent_rollback_scenarios": len(rollback_observations),
    "interrupted_commit_attempts": 1, "interrupted_commit_outcome": "unknown", "interrupted_eventual_rows": interrupted_rows,
    "broken_stdout_exit": broken_stdout_exit, "broken_stdout_durable_rows": 1, "broken_stdout_attempts": 1,
    "native_selector": "^(TestMachine|TestCLIHistory|TestTemporary)", "required_inventory": public_names(inventory),
    "tests_passed": public_names(baseline_passed), "native_tests_passed": public_names(native_passed)}))
PY
