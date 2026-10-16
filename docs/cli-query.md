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
