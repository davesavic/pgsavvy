# Machine CLI contract

Linux and macOS share machine CLI dispatch; Windows retains legacy startup.
The macOS implementation in this source tree awaits native acceptance; a
cross-build is not evidence that its runtime contract has passed.
Production dispatch composes the implemented
connection, catalog, query and capability handlers before legacy startup.
It never initializes TUI configuration, persistent logs or themes. History is
the sole explicit state exception: a query may append to the shared history
store only when its exact trusted saved profile has `cli_history: true`.
Absent/false/unresolved consent preserves the default no-history-artifacts rule.

Commands return one version-1 JSON object followed by a newline. The envelope
has `schema_version`, `ok`, `command`, optional `connection`, `data` or `error`,
`warnings` (always an array), `result_complete`, `truncated`, and an optional
`truncation_reason` (`row_limit` or `page_limit`). A connection is a selected
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
| 4 | Profile policy denied |
| 5 | Database failure or missing object |
| 6 | Timeout or cancellation |
| 7 | I/O, encoding or result-resource failure |

Output including the envelope and newline is at most 1 MiB. Encoding overflow
publishes a safe error instead of partial data. A failing/broken output writer
returns exit 7 without appending another document. An open unread pipe is also
bounded by the invocation deadline, not only a pipe whose reader has closed.

Limits: SQL and parameter input each 1 MiB; 256 parameters; profile name 256
bytes; rows default 100, maximum 1000; PostgreSQL-text cell 64 KiB; invocation
30 seconds; connection 10 seconds; helper 5 seconds and 32 KiB stdout;
statement 10 seconds; lock 1 second; fresh cleanup at most 2 seconds.
`--timeout` may lower the invocation budget; `--format json` is the only format.
Duplicate/conflicting/unknown flags are errors. Command-specific help is JSON.

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
saved read_only and credential_status="not_checked". Saved read_only is a TUI
preference: machine queries are always read-only regardless of that field.

## Binary rollback

Stop all CLI/TUI/history/profile writers and automation; finish active
invocations before downgrading. Back up configuration and make a **WAL-consistent
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
