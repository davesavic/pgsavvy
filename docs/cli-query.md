# Machine queries (Linux and macOS)

Native macOS acceptance remains pending; Windows retains legacy startup.

## Guarded reads

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

Queries default to a 10s statement limit within a 30s total command budget.
Use `--timeout=2m` for a longer command and statement budget; the total still
includes connection, cleanup and output, with up to 4s (or 40% of shorter budgets)
reserved for cleanup/publication. A shorter positive saved `statement_timeout`
still applies; empty/zero profile values use the CLI limit rather than disabling
it. This applies to both guarded and writable full queries. See the
[timeout contract](cli-contract.md) for bounds and cancellation behavior.

Columns have positional `name`, `type_oid`, `encoding: "postgres-text"`. Rows are
positional arrays: PostgreSQL text is a JSON string and SQL NULL is JSON null.
Duplicate aliases remain distinct positions. Numeric precision, JSON source
text, bytea hex, array bounds and special temporal values are not GUI-decoded.
Initial session formats are UTF8, ISO dates, UTC, postgres intervals, hex bytea and
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

## Explicit full access

Configure the saved profile in `connections.yml`:

```yaml
connections:
  - name: development
    driver: postgres
    host: localhost
    database: app
    user: app_operator
    cli_access: full
    read_only: false
    confirm_writes: true
    confirm_ddl: true
    cli_history: false
```

Full includes metadata and guarded reads. `full` plus `read_only: true` always
uses the guarded SELECT validation and READ ONLY transaction path above, even
with approval flags. The client never forces a server/DSN read-only default off.

Writable full analyzes all top-level statements before connecting. Approval is
required if any statement is heuristically classified as DML with
`confirm_writes`, or DDL with `confirm_ddl`. This deliberately uses the existing
TUI heuristic: opaque syntax, unknown statements and functions with effects
can evade classification; false positives are possible. Both confirmation flags
false deliberately means no approval. This is not a security sandbox.

Without flags, approval lazily acquires the controlling terminal, **not SQL
stdin, stdout or stderr**. Read the bounded preview (omitted content is labelled),
type the exact case-sensitive saved name, then lowercase `yes`. SQL may still
arrive through stdin:

```sh
cat change.sql | pgsavvy query --connection development --file -
pgsavvy query --connection development --file change.sql \
  --approve --confirm-connection development
```

The second path requires both flags together with the exact name; partial or
mismatched flags fail without terminal fallback. Explicit flags acknowledge
caller intent, **not proof of a human**. Neither path elevates policy/privileges
or persists approval. Each invocation captures its SQL, parameters and selected
profile once; no shared approval cache exists. Approval completes before
credential helpers, SSH or database setup and consumes the invocation deadline.
Cooperating interactive processes share one persistent per-UID OS-local lock,
independent of XDG/TMPDIR; a competing ceremony fails before reading. Explicit
flags need no terminal or ceremony lock. Never unlink the live lock inode.

### Native execution and results

Unparameterized SQL is sent **once, unchanged, as one native PostgreSQL batch**;
analysis never splits execution and there is no application transaction wrapper.
Parameters are supported for exactly one statement. Streaming `COPY FROM STDIN`
and `COPY TO STDOUT` are rejected anywhere in a batch; server-side COPY remains
subject to PostgreSQL privileges. Initial `standard_conforming_strings=on`
aligns analysis with server scanning, including DSNs requesting off.

PostgreSQL owns implicit batch transactions, explicit COMMIT/ROLLBACK and
permissions. A standalone VACUUM can work while `SELECT 1; VACUUM ...` fails
because PostgreSQL prohibits it inside that native batch transaction. CREATE
DATABASE has similar restrictions and requires the relevant role privilege.
Configured SET ROLE is not an immutable security boundary: SQL can RESET/SET
ROLE within the login's grants and change session formatting. Prefer restricted
logins as well as restricted configured roles.

Writable-full `data.results` is an array of `command_tag`, `columns`, `rows`,
`truncated` and optional `truncation_reason`; column/value encoding remains
positional PostgreSQL text. Guarded output remains `data.columns`/`data.rows`.
The default 100 / maximum 1000 row cap is **aggregate**, with at most 256 retained
results, 64 KiB per cell and 1 MiB for the complete JSON document. Row/result
caps can succeed with truncation. Retained-byte exhaustion or invalid output
cells stop retention, **not execution**: later statements and COMMIT are drained,
then a bounded resource error returns without partial rows. An 8 MiB protocol
message ceiling or transport failure can instead abort execution.

Optional full-only top-level `execution` has `status` (`not_started`, `completed`,
`failed`, `unknown`), `effects_may_have_occurred`, and
`prepared_transactions_may_remain` when relevant. Possible effects begins at
SQL dispatch. A failure can follow an earlier COMMIT; command tags and status
do not prove durability. An unfinished local transaction is rolled back and
reported as incomplete, never silently committed. Deliberate PREPARE TRANSACTION
can survive connection close with idle transaction status; inspect
`pg_prepared_xacts` independently and explicitly COMMIT/ROLLBACK PREPARED only
the GIDs you own and intend to resolve.

Execution and output delivery are separate: broken stdout or an exhausted outer
deadline may prevent JSON entirely, even after effects occurred. **Never blindly
retry SQL** on an error, missing document or timeout. Independently inspect
database state and prepared transactions before deciding on recovery.

## Temporary atomic queries

No saved profile or `cli_access` is needed. First provision a dedicated restricted
login, local database and protected passfile **outside** the CLI. Clear nonempty
PostgreSQL configuration environment settings listed in
[the temporary target contract](cli-contract.md#temporary-local-targets).
Uppercase strings below are placeholders, not provisioned fixtures:

```sh
printf '%s\n' 'SELECT 1;' | pgsavvy query --temporary --port 55432 \
  --database DB_NAME --username RESTRICTED_USER --passfile /ABSOLUTE/PASSFILE --file -
pgsavvy query --temporary --port 55432 --database DB_NAME \
  --username RESTRICTED_USER --passfile /ABSOLUTE/PASSFILE \
  --file ./REVIEWED_CRUD.sql --allow-write
pgsavvy connections test --temporary --port 55432 --database DB_NAME \
  --username RESTRICTED_USER --passfile /ABSOLUTE/PASSFILE
```

The second command is only for caller-authorized writes. Every invocation is
read-only unless `query --allow-write` is supplied; this switch never overrides
server read-only policy or grants. No approval ceremony, saved selector or history
consent applies. Existing `--file`, `--params-file`, `--schema`, `--max-rows` and
`--timeout` constraints remain; parameters still support only one statement.

### Conservative supported grammar

- Parser-supported SELECT/WITH/VALUES/TABLE, recursive read CTEs, subqueries,
  joins and UNION/INTERSECT/EXCEPT.
- Ordinary INSERT (including DEFAULT VALUES, INSERT SELECT and parser-supported
  ON CONFLICT), UPDATE including FROM, DELETE **without USING**, and RETURNING.
- Multiple supported statements and fully traversed modifying CTEs; any detected
  write anywhere requires `--allow-write`.

The entire script is validated before dispatching **any submitted SQL**. Reject
unsupported syntax anywhere, including transaction controls and aliases
(BEGIN/START TRANSACTION/END/ABORT/COMMIT/ROLLBACK), chaining, savepoints and
prepared transactions; DDL/TRUNCATE, DO/CALL, all COPY, EXPLAIN, SET/RESET,
PREPARE/EXECUTE, SELECT INTO, row locks and unknown statement/table nodes.
DELETE USING, UPDATE/DELETE ORDER BY or LIMIT, dialect UPSERT, RETURNING NOTHING,
bracketed sources, index/join hints and AS OF SYSTEM TIME are unsupported.
This pinned parser subset is **not PostgreSQL grammar equivalence**; accepted
syntax/types/names still must be accepted by the server.

Lexical restrictions also reject bare CR ending a `--` comment (CRLF is allowed),
vertical tab outside literals/comments, adjacent quoted continuation across
whitespace, U& Unicode escapes and N/B/X-prefixed literals, unsupported E-string
escapes, unquoted identifiers changed by Unicode normalization, and unquoted
`experimental_`/`testing_` aliases. Ordinary strings with doubled quotes,
dollar-quoted strings and supported E strings remain available. E escapes are
restricted to the shared spelling set: backslash, single/double quote,
`b f n r t x u U`, and octal digits `0..7`; decoded text must remain valid UTF-8
without NUL. Quoted names and strings do not become SQL keywords. These guards
are conservative compatibility restrictions, not a function sandbox.

### Owned transaction and limits

Each temporary query owns one transaction (BEGIN READ ONLY by default, BEGIN
with opt-in). Original unparameterized SQL is sent once, unchanged, in one
simple-query message, not separately by statement. Parameters use the existing
single-statement binding path. Every result is drained and validated, including
omitted rows. Precommit SQL, result-resource/format or exact success-envelope
encoding errors prevent commit and cause rollback/disposal. Requested row
truncation is normal success and **permits commit**; it limits retained output,
not database effects or work.

At most 256 submitted statements are accepted. Aggregate rows default to 100,
maximum 1000; SQL/parameter input and output each cap at 1 MiB, with 256 parameters,
64 KiB cells and an 8 MiB protocol-message ceiling. Unlike saved full execution,
hard result/encoding failures are precommit failures, not a reason to drain and
commit. Success uses `data.results` even for reads, the same positional
PostgreSQL-text values as full mode. Top-level `target`, `execution` and
`transaction_outcome` retain available evidence. See the
[outcome table](cli-contract.md#transaction-and-output-contract).

`committed` means acknowledged COMMIT, not guaranteed output delivery. Cleanup
or stdout failure can produce a nonzero exit after commit; an interrupted COMMIT
can be `unknown`. Never automatically retry. Rollback covers transactional row
changes, **not sequence consumption or permitted external effects**. Functions,
SECURITY DEFINER routines, triggers, extensions, memberships and grants remain
the externally provisioned login's responsibility. CRUD can delete all accessible
rows. No automatic database-object/fixture cleanup or worktree ownership proof
is promised; saved full semantics above are unchanged.

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
- **blocked:** unsupported SQL, profile policy or approval denial, without execution.

The event contains only original SQL, profile name, source `cli`, completion
timestamp, elapsed attempt duration and outcome. Duration starts after SQL input
completes and ends after database cleanup; it excludes the history append and
JSON/stdout delivery. Approval time is included. A later encoding, broken-pipe or unread-output failure can
therefore produce exit 7 while history correctly says `succeeded`.

**Privacy:** raw SQL literals and comments are retained. Bound parameter values,
returned rows, DSNs, credentials and raw errors are not accepted by the event
API. Approval responses, approval flags and parameters are never recorded.
Do not enable history for SQL containing secrets you cannot retain locally.
Disabling consent stops future recording; it does not erase existing rows.

Consent and execution use one immutable selected profile snapshot. Revocation
affects subsequent invocations, not an already selected attempt. Metadata reads
are regular-file, UTF-8 and size-bounded to 1 MiB; consent additionally requires
trusted ownership/path ancestry and a non-group/world-writable profile. Missing,
duplicate, malformed, insecure or budget-expired metadata means no recording.
Rejected SQL may read this metadata, but invokes no credential helper, driver
configuration, SSH or database connection. History consent never grants query
access: only explicit full plus `read_only: false` permits writable execution.

History shows the recorded attempt outcome first, for example
`[cli succeeded] reporting | SELECT ...`, so narrow views retain the result.
`succeeded`/`failed` is an attempt outcome, not commit/rollback evidence. Even
failed full SQL may have committed effects or left prepared transactions.
Independently inspect state before replaying.

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
CLI phase. This does not authorize automatic SQL replay; use a future deliberately
chosen invocation to check recording. Unknown-version lock contention, corrupt/unwritable stores
and unsupported future schemas are unavailable instead; inspect/remediate without
deleting or automatically downgrading the store. See
[binary rollback](cli-contract.md#binary-rollback) before using a predecessor.
