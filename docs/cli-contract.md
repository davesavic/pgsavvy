# Machine CLI contract

Linux and macOS share machine CLI dispatch; Windows retains legacy startup.
The macOS implementation in this source tree awaits native acceptance; a
cross-build is not evidence that its runtime contract has passed.
Production dispatch composes the implemented
connection, catalog, query, capability and LLM-guide handlers before legacy startup.
It never initializes TUI configuration, persistent logs or themes. Explicit state
exceptions are the persistent approval lock described below and opted-in history:
a query may append to the shared history
store only when its exact trusted saved profile has `cli_history: true`.
Absent/false/unresolved consent preserves the default no-history-artifacts rule.

When publication succeeds, commands return one version-1 JSON object followed
by a newline. Broken output or an exhausted outer deadline may prevent delivery;
SQL may already have executed. Never automatically retry a failed invocation.
The envelope
has `schema_version`, `ok`, `command`, optional `connection`, `data` or `error`,
`warnings` (always an array), `result_complete`, `truncated`, and an optional
`truncation_reason` (`row_limit`, `page_limit`, or full-only `result_limit`).
Writable-full queries can additionally expose `execution.status`
(`not_started`, `completed`, `failed`, `unknown`), `effects_may_have_occurred`,
and `prepared_transactions_may_remain`. Possible effects begins at SQL dispatch;
these fields preserve available evidence, not commit/rollback guarantees.
Error responses contain no partial result rows. A connection is a selected
profile name, never an endpoint. Backend work and cleanup finish before encoding.
Error messages and remediation are allowlisted; raw backend errors are private.
Diagnostic stderr is best-effort and follows the stdout publication attempt;
its safe outcome code reflects that attempt. A failed diagnostic sink does not
suppress a healthy stdout document or change its exit status.

| Exit | Meaning |
| --- | --- |
| 0 | Success, including an explicitly capped preview |
| 2 | Invalid input or unsupported SQL |
| 3 | Unavailable connection, credentials or SSH trust |
| 4 | Profile policy or approval denied/unavailable |
| 5 | Database failure or missing object |
| 6 | Timeout or cancellation |
| 7 | I/O, encoding or result-resource failure |

Output including the envelope and newline is at most 1 MiB. Encoding overflow
publishes a safe error instead of partial data. A failing/broken output writer
returns exit 7 without appending another document. An open unread pipe is also
bounded by the invocation deadline, not only a pipe whose reader has closed.

Limits: SQL and parameter input each 1 MiB; 256 parameters; profile name 256
bytes; rows default 100, maximum 1000; PostgreSQL-text cell 64 KiB; invocation
defaults to 30 seconds; connection 10 seconds; helper 5 seconds and 32 KiB stdout;
statement defaults to 10 seconds; lock 1 second; fresh cleanup at most 2 seconds.
Writable full retains at most 256 results and 1000 aggregate rows. Retention
overflow drains later SQL/COMMIT before returning a resource error; a protocol
message exceeding 8 MiB or transport failure can abort with unknown outcome.
Explicit `--timeout DURATION` sets both the total invocation budget and the
statement ceiling, for example:

```sh
pgsavvy query --connection dev --file slow.sql --timeout=2m
```

It accepts a positive Go duration up to `2147483647ms`, PostgreSQL's
signed 32-bit millisecond limit (`capabilities.limits.timeout_max_ms`). Zero,
negative, malformed and larger durations are errors. Positive sub-millisecond
values round up to 1ms for PostgreSQL only; the invocation keeps its exact duration.
A shorter positive saved profile `statement_timeout` remains authoritative.
An empty/zero saved limit uses the CLI ceiling, not an unlimited query. Startup,
session and guarded transaction settings use that same effective statement limit.
With no flag, the existing 30s invocation and 10s statement defaults remain.
`invocation_seconds` and `statement_seconds` in capabilities describe these
defaults, not hard maxima. The total budget includes stream acquisition, input,
approval, connection, queries, cleanup and publication; it is not a promise of
that much SQL execution time (see the reserve below). Earlier caller cancellation
still wins. Connection/helper/lock/cleanup limits and TUI settings are unchanged.
`--format json` is the only format.
Duplicate/conflicting/unknown flags are errors. Command-specific help is JSON.

`pgsavvy llm-help` (also `pgsavvy llm-help --help`) returns the complete offline
LLM operating guide in this same envelope. It shares the command catalogue,
usage strings, global flags, limits, exit codes and query policy with
`capabilities`, and adds workflow examples, permission/approval guidance,
result interpretation and recovery rules. Global `--format json` and `--timeout`
apply normally. It does not read profile configuration or SQL stdin, acquire
credentials or approval, connect to a database, or create history/log/config
artifacts. Examples are not permission to change saved policy or approve writes.

The invocation reserves up to four seconds, or 40% of a shorter budget, inside
its deadline: half for cleanup and half for publication (publication at most two
seconds). Backend work receives
the earlier deadline, calculated from the effective deadline including its parent.
Work cancellation interrupts owned input without prematurely closing output.
A parent cancellation can prevent error delivery; no
publication continues past the outer invocation deadline. Cancellable handles
must interrupt and join blocked I/O when closed. Arbitrarily stalled remote
filesystems and non-cooperating injected streams are excluded.

On macOS, blocking inherited pipes and output sockets use private workers in
the same executable. No extra program is installed and no public command is
added. Workers receive only their stream and private protocol channels, not the
caller's environment. Destination writes are acknowledged before publication
completes; enqueueing bytes into a worker does not count as delivery. If a worker
dies before acknowledging, remaining delivery is unknown and never replayed.
Closing a wrapper kills and waits for its worker and joins active operations;
borrowed stream flags and socket options are not changed. Replacing/removing
the executable during an invocation can prevent worker startup and fails with
the existing resource error. Forced process termination remains outside the
graceful-cleanup guarantee.

Bare startup, version, log-directory selection and `update` remain legacy routes.
Unknown command words never fall back to the TUI. Commands and dependencies are
injected into `Dispatch`, without package-init registration or GUI construction.

Production SIGINT/SIGTERM cancel operation/input work, join owned cleanup, and
attempt one timeout-error document while stdout remains available. A blocked or
broken output handle can prevent delivery; no second document is appended.
Publication is the commitment point: a signal after publication starts cannot
replace a partially written result. Use `connections show -- -NAME` to select a
profile whose literal name starts with a dash (similarly for connection test).
Offline profiles expose only name, driver, tags, label, effective cli_access,
saved read_only and credential_status="not_checked". `row-read` always uses the
guarded SELECT/READ ONLY transaction path. `full` plus `read_only=true` uses
that same path; only explicit `full` plus `read_only=false` permits arbitrary
SQL. Approval never bypasses profile policy, server read-only defaults or grants.

Full confirmation is an OR over classified top-level DML/DDL with the matching
`confirm_writes`/`confirm_ddl` enabled. This heuristic deliberately has gaps for
opaque SQL and side-effectful functions; both flags false deliberately means no
approval. It is not complete mutation detection. Configure full explicitly and
use restricted logins; see [configuration and examples](cli-query.md#explicit-full-access).

Approval either uses the controlling terminal (exact case-sensitive connection
name, then lowercase `yes`), or query-only `--approve --confirm-connection NAME`
with both flags and an exact name. Partial/mismatched flags never fall back to
the terminal. Flags acknowledge caller intent, not human participation. Approval
is immutable-invocation-only: no cache, sharing or persisted authorization.
Terminal work never consumes SQL stdin or JSON stdout/stderr. Approval precedes
credential/network setup, counts against the deadline, and cancellation closes
and joins owned work. Cooperating ceremonies exclude one another via a persistent
per-UID lock in Linux `/tmp` or Darwin `/private/tmp`, independent of XDG/TMPDIR;
busy/unsafe acquisition fails before reading. Never unlink/replace that inode.

`APPROVAL_REQUIRED`, `APPROVAL_MISMATCH`, `APPROVAL_UNAVAILABLE` and
`APPROVAL_CANCELLED` return exit 4. Malformed/conflicting flags return
`INVALID_INPUT`/2; SIGINT/SIGTERM/deadline return `TIMEOUT`/6. No raw approval
response or backend error is public. Opted-in history records approval denials
as blocked and deadline/signal failures as failed; history is effects-unverified,
not durability evidence. No additional approval/parameter/credential logging is
introduced.

Full sends original unparameterized native SQL once without an application
transaction wrapper; parameters support one statement only. Streaming
COPY FROM STDIN/TO STDOUT is unsupported. PostgreSQL native batching, maintenance
transaction restrictions and role grants remain authoritative. Unfinished local
transactions roll back with an explicit error. Deliberate PREPARE TRANSACTION
can leave durable prepared work despite idle state/connection close; inspect
and resolve owned GIDs explicitly. Configured SET ROLE is not an immutable
security boundary within the login's grants. See [execution details](cli-query.md#native-execution-and-results).

## Binary rollback

1. Stop new CLI invocations, TUI/history/profile writers and automation. Quiesce
   approval waiters too; cancel and **join** local work before editing profiles.
2. Independently inspect server backends and `pg_prepared_xacts`. Resolve only
   owned residual transactions/backends/prepared GIDs with an explicit recovery
   decision. Transport abort or local process exit does not prove server rollback.
3. Replace **every** `cli_access: full` with a mode supported by the target older
   binary while writers remain stopped. Old binaries reject unknown full values.
4. Complete the configuration/history compatibility steps below, then replace
   the binary and verify before resuming work. **Binary rollback does not reverse
   executed SQL**, committed effects or durable prepared transactions.

Back up configuration and make a **WAL-consistent
history backup** with SQLite backup/VACUUM INTO, not a copy of only a live main
database. Preserve the backup and inspect its exact schema version.

For a v2 history store and a v1 predecessor, run the guarded offline
[`rollback_002.sql`](../pkg/query/sql/rollback_002.sql) on the stopped-writer
copy. With the SQLite shell, enable `.bail on` before `.read`; on any error,
rollback/close and investigate, never continue to commit. Never auto-downgrade
or run this script on an unknown/future schema. IDs, SQL, completion timestamps,
durations, existing metrics and FTS searches remain. **Source labels are lost;
blocked becomes the legacy unsuccessful/failed value.** Re-upgrading cannot
reconstruct that metadata without the preserved v2 backup.

Remove every `cli_history` key from the offline configuration copy. Remove other
keys only if the exact target predecessor does not know them—for example,
`cli_access` when rolling back before machine-CLI support. Verify predecessor
strict parsing plus history read/search/append before replacing the binary and
resuming writers. Roll back cooperating configuration-lock writers together;
do not delete a live shared lock sidecar. Never restore new-only keys while the
predecessor is active. No automatic profile rewrite occurs.

Cooperating profile writers retain their existing locking/revocation behavior;
arbitrary manual YAML writers can race. The emergency procedure requires
quiescence and is not a new manual-writer concurrency guarantee.
