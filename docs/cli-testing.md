# Machine CLI native verification

## Status and scope

Native Windows/macOS support is **pending**. The `native-baseline` job in
[`cli-integration.yml`](../.github/workflows/cli-integration.yml) prepares six
native targets against PostgreSQL 17 and 18. Baseline success, compilation,
emulation of PostgreSQL tools, or skipped tests do **not** certify machine CLI
support. Beads `pgsavvy-y2rh.14` owns full-suite activation and final evidence
reconciliation. Task `.9` remains open until all twelve baseline lanes run.

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

Run from the repository root with **Go 1.26.9**. Unix additionally requires
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

The final JSON evidence line records `scope=baseline-not-native-support`, actual
GOOS/GOARCH, Go version, CGO setting, exact binary path and SHA-256, PostgreSQL
tool/server versions, fixture version, wrong-password rejection and executed
test inventory. The temporary binary is removed after the run. Only synthetic
fixture evidence is emitted; raw database errors, credentials and build/server
logs are not published. Missing tests fail even when `go test` exits zero.
Windows failures report a fixed provisioning stage and script line number;
the real initdb command also reports its numeric exit code. Its logs remain
private and are deleted by owned cleanup.

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

The real random-password initialization uses the same child-only environment,
owned working directory, argument-list launcher and closed stdin as the probe.
Its bounded output is never published. Startup, wrong-password rejection and
the CLI baseline still run against the real fixture; a probe-only success is
not accepted. Unjoined initialization work retains the owned fixture and fails
the lane.

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

The Darwin stream adapter rejects blocking output sockets rather than risking
an uncancellable kernel send. Output sockets and FIFOs must already have
`O_NONBLOCK` set; other owners must not clear it while the adapter is in use.
Acquisition never changes caller-owned flags or socket options. Blocking stream
socket input remains supported. Linux socket behavior is unchanged.

## Full acceptance handoff to pgsavvy-y2rh.14

After the adapters and shared commands land, extend the owned-fixture scripts
and workflow to run the full selector against the **same** shipping artifact:

```sh
go test -count=1 -timeout 10m -tags=integration ./internal/machineio ./pkg/cli ./pkg/drivers/pg ./pkg/session ./pkg/session/sshtunnel -run '^TestMachine'
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
