# Read-only machine queries (Linux)

Opt a saved PostgreSQL profile into `cli_access: row-read`, then use:

```sh
pgsavvy query --connection reporting --file query.sql --params-file params.json
cat query.sql | pgsavvy query --connection reporting --file - --max-rows 100
```

SQL has exactly one regular-file or stdin source. Parameters are one JSON
scalar/null array, for example `[9007199254740993, "literal", true, null]`.
Numbers retain their source lexemes. Nested JSON/array values must initially be
passed as strings with explicit SQL casts. Parameter stdin is not supported.

The pinned parser accepts a SELECT-family subset, including read-only WITH,
VALUES and TABLE. Multiple statements, modifying CTEs, row locking, SELECT INTO,
transaction/session commands and unsupported syntax are rejected. Original SQL
is sent through extended Parse/Bind, even without parameters. The backend owns
BEGIN READ ONLY, local timeouts and ROLLBACK on the same connection.

Columns have positional `name`, `type_oid`, `encoding: "postgres-text"`. Rows are
positional arrays: PostgreSQL text is a JSON string and SQL NULL is JSON null.
Duplicate aliases remain distinct positions. Numeric precision, JSON source
text, bytea hex, array bounds and special temporal values are not GUI-decoded.
Session formats are UTF8, ISO dates, UTC, postgres intervals, hex bytea and
extra_float_digits=3. Invalid UTF-8 is rejected.

Rows default to 100; `--max-rows` accepts 1..1000. Exact-limit EOF is complete.
An actual omitted row marks `result_complete=false`, `truncated=true` and
`truncation_reason="row_limit"`. A capped result succeeds only after the entire
tail is drained/discarded, clean terminal completion and rollback. Slow/erroring
tails invalidate the whole request. Use SQL LIMIT to limit server work/transfer;
the CLI row cap limits retained/returned rows, not server work.

SQL and parameter files each cap at 1 MiB, 256 parameters, each text cell 64 KiB,
and the entire JSON document including newline 1 MiB. Oversized data fails,
never silently clips. Invocation/connection/helper/server/cleanup bounds are in
`cli-contract.md`. No process-memory or immediate remote termination guarantee
is made. Function/extension authority remains governed by database grants;
use dedicated restricted roles and approved views, not the AST filter as a sandbox.

## Optional shared history

History is **off by default**, independently of `cli_access` and saved
`read_only`. Enable the connection form's `cli_history` toggle, or explicitly
set it in trusted saved YAML:

```yaml
connections:
  - name: reporting
    driver: postgres
    host: localhost
    database: reporting
    cli_access: row-read
    cli_history: true
```

Only the YAML Boolean `true` enables recording; null, quoted strings and unknown
settings are rejected by strict loading. There is no global, environment or
per-command consent override. Offline connection JSON remains unchanged.

After complete, nonempty UTF-8 SQL has been read, one attempt can be recorded:

- **succeeded:** the query (including a capped preview's drained tail) and
  database cleanup completed successfully.
- **failed:** parameter, credential, connection, backend, timeout/cancellation
  or database-cleanup failure.
- **blocked:** unsupported SQL or profile policy denial, without execution.

The event contains only original SQL, profile name, source `cli`, completion
timestamp, elapsed attempt duration and outcome. Duration starts after SQL input
completes and ends after database cleanup; it excludes the history append and
JSON/stdout delivery. A later encoding, broken-pipe or unread-output failure can
therefore produce exit 7 while history correctly says `succeeded`.

**Privacy:** raw SQL literals and comments are retained. Bound parameter values,
returned rows, DSNs, credentials and raw errors are not accepted by the event
API. Do not enable history for SQL containing secrets you cannot retain locally.
Disabling consent stops future recording; it does not erase existing rows.

Consent and execution use one immutable selected profile snapshot. Revocation
affects subsequent invocations, not an already selected attempt. Metadata reads
are regular-file, UTF-8 and size-bounded to 1 MiB; consent additionally requires
trusted ownership/path ancestry and a non-group/world-writable profile. Missing,
duplicate, malformed, insecure or budget-expired metadata means no recording.
Rejected SQL may read this metadata, but invokes no credential helper, driver
configuration, SSH or database connection. History consent never grants query
access: machine execution stays unconditionally read-only with `read_only: false`.

Recording is synchronous **best effort, not an audit log**. Healthy completion
within budget exposes one committed event immediately on normal process exit.
Contention, cancellation, exhausted budget, crashes or SIGKILL can lose history.
Dedicated metadata lookup plus append work share at most 250 ms; intervening
database execution does not consume that allowance. Parsing observes cancellation
and rejects structurally invalid wide mappings before uninterruptible conversion.
Append also respects the absolute cleanup cutoff and publication reserve, and
is skipped if cleanup already consumed its allowance. No abandoned writer or
main-defer flush is used. Arbitrarily stalled remote filesystems, malicious
same-user filesystem mutation and non-cooperating injected I/O are not hard-bound
or isolation guarantees.

History failure preserves the query result/error/exit. Deliverable envelopes may
include `HISTORY_UNAVAILABLE` or `HISTORY_UPGRADE_REQUIRED`; these warnings contain
no raw diagnostic and may be omitted if warning bytes alone would overflow an
otherwise valid result. Help, malformed flags, unreadable/empty/oversized SQL and
other machine commands perform no history work.

The TUI's History tab refreshes on each activation, without polling (at most
500 rows, one active read and one coalesced request, two-second read deadline).
Rows show textual source, profile and all three outcomes. Legacy history is
`tui` with its original success/failure value. Display controls are sanitized;
stored SQL and recall remain byte-for-byte original. Selecting even a blocked
row copies SQL into the editor and **does not execute it**. Review before any
manual run. Blocked rows do not supply prefix completions.

## History permissions and upgrades

The shared store is `$XDG_STATE_HOME/pgsavvy/history.sqlite`, normally
`$HOME/.local/state/pgsavvy/history.sqlite` on Linux or
`$HOME/Library/Application Support/pgsavvy/history.sqlite` on macOS when XDG
state is unset. New CLI-first
and TUI-first Unix stores use 0700 directories and 0600 DB/WAL/SHM/journal files.
This is not a Windows ACL privacy claim; Windows machine CLI activation remains
pending. The new macOS port still requires native acceptance.

Existing broadly accessible stores are **not automatically chmodded**. The TUI
can retain access to an existing store while CLI appends refuse it. For manual
remediation, stop all writers and back up consistently; verify the directory,
database and existing sidecars are the expected owned regular targets, then
restrict the store directory and files to owner access (normally 0700/0600).
Inspect unsafe writable ancestors too. Do not chmod through symlinks, hardlinks,
foreign-owned or unexpected targets; investigate those instead. Permission repair
does not undo information previously readable by others.

`HISTORY_UPGRADE_REQUIRED` means a known pending migration could not finish in
the supplied budget. Open the current/new TUI once to upgrade outside the bounded
CLI phase, then retry. Unknown-version lock contention, corrupt/unwritable stores
and unsupported future schemas are unavailable instead; inspect/remediate without
deleting or automatically downgrading the store. See
[binary rollback](cli-contract.md#binary-rollback) before using a predecessor.
