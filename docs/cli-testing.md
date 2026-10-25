# Machine CLI native verification

## Opt-in query-history verification

Fixture-independent Linux subprocess coverage runs without the integration tag:

```sh
go test -count=1 ./pkg/cli -run '^TestCLIHistory(Process_|_RollbackRehearsal)'
go test -race -count=1 ./pkg/cli -run '^TestCLIHistory(Process_|_RollbackRehearsal)'
go test -count=1 ./pkg/query ./pkg/config ./pkg/gui/context ./pkg/gui/controllers ./pkg/gui/orchestrator
task test
task lint
task build
```

Subprocess tests reuse the Go test executable with production command composition,
normal saved-profile consent and real machine I/O adapters. They do not replace
verification of the shipping entry point. HOME/XDG, profiles, SQLite stores and
protocol peers are test-owned; inherited PostgreSQL/helper/SSH settings are not
used. No real PostgreSQL fixture is needed. Immediate post-exit read-only opens
prove commitment without a history-flush sleep. Default fixtures keep their
zero-state assertions; separate opted-in fixtures allow only private SQLite
artifacts and unchanged profile bytes.

Coverage includes original SQL versus distinct parameter/result/password/error
sentinels (including FTS and live WAL/SHM), fresh/v1/current concurrent starts,
held locks, corrupt/read-only/insecure stores and manual stopped-writer
remediation, inclusive 1 MiB SQL input, SIGINT/SIGTERM, cleanup failure and broken
or unread output. Test-owned protocol peers are not live PostgreSQL evidence.
Race-process shutdown overhead is separate from the history-work allowance.
Configuration parser tests exercise cancellation during large comments/dense
nodes and prevent wide/complex mapping keys entering non-cancellable conversion.
The rollback rehearsal takes a WAL-consistent backup, applies the exact-v2 down
script, tests predecessor strict-key rejection/removal and old reads/search/writes,
then verifies retained fields and intentionally lost source/blocked metadata.

Only with a designated disposable fixture configured and preflighted:

```sh
go test -tags=integration -count=1 ./test/integration -run '^TestCLIHistory_Live$'
```

This uses a separate opted-in state root while retaining existing default
fixtures' no-state cleanup checks. With saved `ReadOnly=false`, direct/nested
writes must be rejected, a SECURITY DEFINER write function must fail with
SQLSTATE 25006, and protected data must remain unchanged. Missing live fixtures,
skips and compilation-only checks are **not a live pass**. Native configuration
locking must also be exercised on each supported TUI platform; cross-compilation
does not replace native Windows/macOS runtime evidence.

For the real terminal smoke, use `testing-with-tmux` with disposable HOME/XDG
roots. Verify consent off/on/save and complete disclosure at 80x24, including
upward navigation from the bottom of the form. Open History, append externally,
reactivate, inspect source/profile/outcomes and recall without executing. Check
resolved-action logs, not the screen alone. A temporary test-only binding of
`rail.switch.query_editor` in the startup connection manager can expose the
editor without a database; such a smoke does not certify connected/live behavior
or alter production bindings. Do not use real user profiles/history for a smoke.

## Status and scope

Native macOS acceptance requires the full suite below to pass on both Intel and
Apple Silicon against PostgreSQL 17 and 18. Windows machine CLI activation
remains **pending**.
Linux/macOS now share production commands; Windows remains on legacy dispatch.
The `native-baseline` job in
[`cli-integration.yml`](../.github/workflows/cli-integration.yml) prepares six
native targets against PostgreSQL 17 and 18. Unix fixture lanes now additionally
run machine suites and shipping-binary tests; Windows retains baseline coverage.
Baseline success, compilation,
emulation of PostgreSQL tools, or skipped tests do **not** certify machine CLI
support. Beads `pgsavvy-y2rh.14` owns full-suite activation and final evidence
reconciliation for the full three-platform plan. `pgsavvy-xzdq` tracks Mac-first
integration separately; this does not close Windows or full-matrix tasks.

| Runner | Native Go/pgsavvy target | PostgreSQL tools |
| --- | --- | --- |
| `ubuntu-24.04` | linux/amd64 | native source build |
| `ubuntu-24.04-arm` | linux/arm64 | native source build |
| `macos-15-intel` | darwin/amd64 | native source build |
| `macos-15` | darwin/arm64 | native source build |
| `windows-2025` | windows/amd64 | EDB x64 binaries |
| `windows-11-arm` | windows/arm64 | EDB x64 binaries under emulation |

Unavailable runners, tools, fixtures or required tests block activation. WSL,
Git Bash and cross-builds are not substitutes for native execution. Windows ARM64
may emulate PostgreSQL x64 tools; Go and the tested pgsavvy executable must be
ARM64. Scripts compare the actual host architecture with both Go host/target
values and the expected workflow target before provisioning anything.

## Disposable fixture preflight

Run from the repository root with **Go 1.26.10**. Unix additionally requires
Python 3, curl, tar/bzip2, a native C compiler, make, bison and flex. Windows
requires native PowerShell 7. Nothing is installed globally. Do not run as root:
PostgreSQL refuses root-owned clusters.

Unix (Linux/macOS):

```sh
bash .github/scripts/cli-postgres.sh 17
bash .github/scripts/cli-postgres.sh 18
# Equivalent with go-task:
task test:cli:native PG_MAJOR=17
```

Native Windows PowerShell:

```powershell
pwsh -NoProfile -File .github/scripts/cli-postgres.ps1 -Major 17
pwsh -NoProfile -File .github/scripts/cli-postgres.ps1 -Major 18
# Equivalent with go-task and pwsh on PATH:
task test:cli:native PG_MAJOR=18
```

Each script downloads a pinned archive, validates its SHA-256 **before**
extraction, and prepares its own temporary directory and loopback-only cluster.
Source builds also install only into that directory. The scripts initialize a
UTF-8 cluster with SCRAM password authentication, create an isolated admin test
database, apply [`01_fixture.sql`](../docker/postgres/init/01_fixture.sql), and
check the PostgreSQL major, encoding, superuser rights and
`app._fixture_meta.version = 2`. A deliberate wrong-password connection must
reach authentication and fail; missing or stale fixtures are errors. The same
fixture is checked using `go run ./internal/pgprobe` with its temporary
`PGSAVVY_TEST_PG` environment value.

Each invocation generates an unpredictable password and restricts its temporary
directory to the current user (Unix mode 0700; protected Windows ACL). Passwords
are never included in the final evidence line. Windows cleanup is enabled only
after this invocation successfully creates its directory.

No existing server/database/profile/keyring is modified. Ambient service and
password files are bypassed. The selected loopback port is released immediately
before PostgreSQL starts; a port collision fails the lane rather than retrying
against another server. On exit, the scripts stop only their own cluster before
deleting their own temporary directory. A failed stop retains that directory
and fails the run rather than deleting live server data. Forced process/runner
termination may prevent cleanup; never use these scripts on a persistent or
user-owned cluster.

## Artifact pins

[`cli-postgres-versions.json`](../.github/scripts/cli-postgres-versions.json)
freezes PostgreSQL 17.6 and 18.0, including EDB revisions 17.6-1 and 18.0-1.
These are reproducible test fixtures, **not recommendations for production
PostgreSQL versions**. Changes to pins need renewed verification on every lane.

- Unix checksums are published by PostgreSQL alongside each source archive at
  its URL plus `.sha256`.
- Windows checksums were calculated from complete HTTPS downloads of the EDB
  ZIPs; no published SHA-256 sidecar was available. The official PostgreSQL
  [Windows download page](https://www.postgresql.org/download/windows/) links
  to [EDB binary archives](https://www.enterprisedb.com/download-postgresql-binaries).
  Scripts use `pgsql/bin`, not the bundled pgAdmin runtime.
- The manifest requires exactly 64 lowercase hexadecimal SHA-256 digits
  (32 bytes), approved HTTPS origins and version-specific paths. Digest
  mismatch aborts before any downloaded executable runs.

## Baseline evidence

The scripts build an exact native `CGO_ENABLED=0` binary in their owned directory,
set `PGSAVVY_TEST_BINARY` to its absolute path, and run its legacy `--version`
route. These six existing portable dispatch/output tests must execute and pass,
with no skipped or failed tests:

```text
TestMachineCLI_Dispatch
TestMachineCLI_ExitCodes
TestMachineCLI_Envelope
TestMachineCLI_Envelope_ParentDeadlineAndInput
TestMachineCLI_OutputLimit
TestMachineCLI_BrokenWriter
```

Windows additionally requires `TestMachineCLI_WindowsPipeDeadlineCapability`,
which checks deadline support on both directions of the owned test pipe. The
output fixture uses a duplex server handle so Go 1.26.10 can query completion
notification modes; its peer remains read-only, and the existing unread-output
cancellation assertions and time limits are unchanged.

The final JSON evidence line records `scope=baseline-not-native-support`, actual
GOOS/GOARCH, Go version, CGO setting, exact binary path and SHA-256, PostgreSQL
tool/server versions, fixture version, wrong-password rejection and executed
test inventory. The temporary binary is removed after the run. Only synthetic
fixture evidence is emitted; raw database errors, credentials and build/server
logs are not published. Missing tests fail even when `go test` exits zero.
Windows failures report a fixed provisioning stage and script line number;
the real initdb command also reports its numeric exit code. Its logs remain
private and are deleted by owned cleanup.
Unix failures likewise report the fixed provisioning/test stage, script line and
original exit code before owned cleanup, without exposing captured private logs.
Darwin first runs only `^TestMachineTerminalApprovalProcess$` with
`CGO_ENABLED=0` and a 60-second Go test timeout, before PostgreSQL download or
source provisioning. Failure reports `stage=terminal-process-preflight` through
the same private-log reporter and owned cleanup. A passing isolated preflight
does not replace or skip the later full native inventory, tests or evidence
checks; it distinguishes an isolated timeout from a full-suite-only failure.

After that preflight passes, Darwin runs only
`^TestMachineOpen_TargetSnapshot_SSHTLS$` 50 times with `CGO_ENABLED=0` and a
60-second Go test timeout, still before PostgreSQL provisioning. Failures report
`stage=ssh-tls-snapshot-probe`; stdout JSON and stderr remain in the private owned
directory and are deleted by normal cleanup. Only fixed test status and approved
public source locations may be published, never TLS error text, SNI/server names
or certificate data. Passing probes continue to the unchanged full native test
inventory and evidence checks; probe success alone is not native acceptance.

If the six baseline Go tests fail, the failure path may publish only their
fixed top-level names and `passed`/`failed`/`not_completed` status. The reporter
on Windows reads at most 1 MiB of UTF-8 JSON; missing, oversized or invalid logs
report incomplete status. Assertion output, unapproved subtest names, package
names, credentials, environment values and raw test/native logs remain private. This diagnostic
does not change any test, acceptance gate or failure exit status.
Unix baseline/native suite failures stream UTF-8 JSON records with a 1 MiB
per-record limit (including the newline) and a 64 MiB total scan limit, plus at
most one byte to detect overflow. Aggregate output above 1 MiB can therefore
still report failures without loading the whole log into memory. Any malformed
record or exceeded limit discards all statuses, diagnostic metadata and failure
flags, including earlier valid records; this is diagnostic output, never acceptance evidence.
The reporter's fixed allowlist in `.github/scripts/cli_test_status.py` contains
source-declared top-level `TestMachine` and `TestCLIHistory` names from the native
suite's package directories. Subtest failures still update their allowlisted
parent status. Package failures and unlisted failures expose only
boolean flags, never their names. The allowlist does not add acceptance tests.

An approved Unix diagnostic exception adds `cases` and `locations` metadata.
`cases` contains only 23 fixed labels: 14 for
`TestMachineTerminalApprovalProcess`, five for
`TestCLIHistoryProcess_CancellationAndCleanup`, `broken_false`/`broken_true` for
`TestCLIHistoryProcess_OutputPipes`, and
`backend_failure_false`/`backend_failure_true` for
`TestCLIHistoryProcess_PrivacyAndOutput`. Dynamic and nested labels stay private.
Each case records its last lifecycle `action`: `run`, `pass`, `fail` or `skip`;
failure is sticky. `run` without a terminal action is distinct from an actual
`skip`, while the existing top-level statuses are unchanged.

`locations` retains at most the first 32 distinct approved source sites, sorted
for publication. Only reconstructed public basenames and line numbers are
emitted: `terminal_unix_test.go`, `stream_unix.go`, `io_darwin_test.go`,
`query_history_process_test.go` and `machine_test.go`. The Darwin fixture helper
is included because PTY/termios setup can block before the child timeout
context exists. Line
numbers must lie within pinned public source bounds, checked against those
five repository files by regression tests. The reporter recognizes assertion
and Go panic/timeout stack locations in these four families' output, plus
package-level output where timeout stacks have no test name. Private path
prefixes, stack function/argument text, assertion values, SQL, credentials and
all other raw output remain private. These reported sites are diagnostic clues,
not proof of the root cause or native acceptance.

Source locations also cover the exact top-level
`TestMachineOpen_TargetSnapshot_SSHTLS` test. No nested case labels, TLS error
text, server names or certificate contents are published for that test.

An explicitly approved exception publishes bounded, scrubbed **secret-free
probe** diagnostics before the real fixture password is generated. The probe
runs `initdb --version`, `postgres --version`, and initialization with the same
SCRAM/pwfile arguments but a public synthetic password. It uses a fresh owned
directory and a child-only environment allowlist, with owned profile/temp paths
and no inherited credentials, PostgreSQL overrides or restriction bypass. Stdin
is closed, so the probe cannot consume caller input. It
never starts a listening server; initdb's bootstrap/single-user child processes
are still exercised. Probe success is not a replacement for the unchanged
random-password fixture, wrong-password rejection or CLI tests.

The real random-password initialization, server start/stop and PostgreSQL
clients use the same child-only environment, owned working directory,
argument-list launcher and closed stdin as the probe. Their output is never published: initialization
capture is bounded, while server/control logs stay in private owned files.
Startup, wrong-password rejection and
the CLI baseline still run against the real fixture; a probe-only success is
not accepted. Unjoined initialization work retains the owned fixture and fails
the lane.

Server control runs through a small native Go helper with file-backed standard
handles. PostgreSQL's Windows command shell can outlive `pg_ctl start`; it must
not keep the launcher's capture pipes open. An unsuccessful controller still
triggers an owned-cluster stop when a PID file exists. Controller cleanup
uncertainty retains the directory even after that stop succeeds.
Client commands receive only the generated fixture password in their child
environment. The file-backed helper keeps stdout and stderr separate so private
diagnostics cannot corrupt readiness/facts parsing. Only the deliberate
wrong-password call permits psql's connection-failure exit 2; it still requires
the authentication-failure diagnostic. Helper I/O/launch failures use reserved
exit 125 and always fail, including on this deliberate negative test.

Probe JSON records preserve previously unrecognized error text, launch errors,
decimal/hex exit codes and timeout/capture status. Each stdout/stderr capture
retains at most 65,536 characters and drains excess without retaining it.
Incomplete trailing lines are dropped at the capture boundary; known owned,
repository, profile and temporary paths plus OS account/domain/host names are
scrubbed before publication is limited to 4,096 characters per stream. Case and
slash variants are scrubbed, and JSON encoding escapes control characters.
The probe suppresses shell-escaped server startup instructions so they cannot
bypass owned-path redaction.
Legacy runner-command markers are JSON-escaped too. Invalid UTF-8 fails capture
without publishing undecodable text.
Each command has a 120-second timeout; failed process/pipe cleanup retains the
owned fixture and fails the lane rather than deleting potentially active data.
No secret-bearing fixture logs or diagnostic artifacts are published. Run
`pwsh -NoProfile -File .github/scripts/cli-postgres.test.ps1` to check parsing,
native-command stream separation, failure status, argument/environment isolation,
bounded capture, sanitization and cleanup using synthetic native helpers without
provisioning a database.

The original Ubuntu PostgreSQL 17/18 Docker integration/race lanes remain.
Race tests require a supported native C toolchain and remain **separate** from
CGO-disabled shipping evidence; a race binary cannot replace the shipping binary.
Linux still requires `go test -count=1 ./...`, `task lint`, `task build`, and
`task test:integration` against an explicitly designated disposable fixture.
No integration `TestMain` should run against an undesignated database.

The Darwin stream adapter uses isolated same-executable workers for blocking
FIFOs and output sockets. Writes acknowledge actual destination acceptance,
not IPC enqueueing. Cancellation closes private IPC, kills/waits for the worker
and joins active operations without changing borrowed flags or socket options.
Already-nonblocking endpoints retain polling; other owners must not clear their
`O_NONBLOCK` flag during use. Linux socket behavior is unchanged.

## Mac-first verification

On an actual Intel or Apple Silicon Mac with Go 1.26.10:

```sh
CGO_ENABLED=0 go build -o bin/pgsavvy .
PGSAVVY_TEST_BINARY="$PWD/bin/pgsavvy" go test -count=1 -timeout 5m \
  . ./internal/machineio ./pkg/cli ./pkg/drivers/pg ./pkg/session ./pkg/session/sshtunnel \
  -run '^TestMachine'
```

The root tests exercise JSON help, capabilities, offline discovery, enabled and
disabled profile policies, piped SQL input, a shell pipeline, and the private
worker in the shipping executable. Adapter tests include worker startup
cancellation, destination acknowledgements, malformed protocol, closed standard
slots, borrowed-handle survival, terminal identity and joined cancellation.
Linux execution of these tests does not establish Darwin kernel behavior.

For live PostgreSQL 17 and 18 acceptance, run `task test:cli:native PG_MAJOR=17`
and `task test:cli:native PG_MAJOR=18`. Each Unix fixture run now executes the
full machine selector against its exact CGO-disabled binary. It rejects skipped
or failed tests and missing required test names, and prints a package-qualified
executed inventory alongside the binary hash and native architecture. Neither
native workflow execution nor a release is implied by local source changes.

## Full acceptance handoff to pgsavvy-y2rh.14

Unix owned-fixture scripts now run the following against the **same** shipping
artifact. Windows activation and the remaining full-plan proofs are separate:

```sh
go test -count=1 -timeout 10m -tags=integration . ./internal/machineio ./pkg/cli ./pkg/drivers/pg ./pkg/session ./pkg/session/sshtunnel ./test/integration -run '^TestMachine'
```

The Windows command is the same Go command, executed in native PowerShell with
the fixture's process-local `PGSAVVY_TEST_PG` and `PGSAVVY_TEST_BINARY` values.
Neither variable should be pointed at a real-user database or a rebuilt race
artifact. The baseline selector is not an implementation of this handoff.

Required evidence for **each of twelve target/PG lanes** includes:

- All nine commands, version-1 envelopes, documented exit codes, policy-before-
  side-effects, readonly/text/null/catalog/paging/truncation behavior.
- All credential and SSH modes, exact decrypted keyring identity, same-handle
  pgpass privacy, strict pre-existing trust and explicit agent precedence.
- Real native Unix sockets/Windows pipes, console/terminal/pipe/file stream
  classes, helper descendants and startup/containment failures; injection alone
  is insufficient.
- Twenty joined stream cancellation iterations, input at most 1 s, short-budget
  output at most 2 s, aggregate startup interrupt exit 6 at most 3 s, with owned
  handles/work joined and independent invocations unaffected.
- Exact binary path/hash, native OS/architecture, Go/tool/server/fixture versions
  and an explicit executed inventory. Missing modes, required skips, missing
  fixtures, failed mechanisms or absent runners block activation.
- Final comparison of capabilities, actual configuration paths, catchable
  events, command/limit/exit documentation and workflow evidence. Rework draft
  documentation on mismatch before declaring support.

Keep Linux procfs regressions and the existing exceptions for stalled remote
filesystems, noncooperating injected streams, broken/partial output and forced
termination. Obtain scoped `p/default` security review for changed trust
boundaries. No publishing, deployments, migrations, pgpass permission repair,
native-store replacement or new CLI commands are part of these fixture lanes.
