# Catalog inspection (Linux and macOS)

Native macOS acceptance remains pending; Windows retains legacy startup.

Profiles with `cli_access: metadata` or `row-read` permit:

```sh
pgsavvy schema list --connection reporting --limit 100 --after exact_name
pgsavvy schema list --connection reporting --include-system
pgsavvy schema tables --connection reporting --schema 'Exact.Schema'
pgsavvy schema describe --connection reporting --schema 'Exact.Schema' --table 'Exact.Table'
pgsavvy schema relationships --connection reporting --schema 'Exact.Schema' --table 'Exact.Table'
```

Names are exact, case-sensitive, literal strings; dots are never split.
No SQL, row samples, row counts or inferred row privileges are accepted/returned.
Supported kinds are tables, partitioned tables, views and materialized views.
Descriptions preserve column types/nullability, null defaults/comments, constraint
definitions and complete index SQL including expressions, predicates and INCLUDE.
Known constraint kind codes are displayed as PRIMARY KEY, UNIQUE, FOREIGN KEY,
CHECK, NOT NULL, EXCLUDE or CONSTRAINT TRIGGER; unfamiliar codes remain literal.
Relationships preserve inbound/outbound direction, composite pairing and self
references. Cross-schema names are intentionally disclosed as catalog metadata.

Lists default to 100 raw names, at most 1000. SQL reads one lookahead item.
`next_after` is the last consumed raw item, never that lookahead; `raw_complete`
and envelope completeness describe the raw catalog page, not its visible count.
An empty `items: []` page can have a continuation: keep its cursor. Do not treat
an empty visible page as EOF. No loop silently fetches additional pages.

Schema listing hides built-in system schemas and saved `hidden_schemas` globs.
`--include-system` disables only the built-in filter; saved globs still apply.
Malformed saved globs are skipped with one safe `INVALID_INPUT` warning.
Hiding is cosmetic: explicit inspection of hidden/system schemas remains allowed.
Database grants, not hidden names or catalog visibility, control row access.

Describe/relationships sections cap at 1000 each; overflow and oversized values
fail rather than returning complete-looking partial facts. Each invocation owns
one repeatable-read, read-only snapshot and closes before publication. Missing
schemas/relations return OBJECT_NOT_FOUND; errors expose no raw catalog messages.
Pages share no snapshot between invocations and are stable only for unchanged
catalogs. See `cli-contract.md` for deadlines, resource bounds and exit codes.

## Temporary local inspection

All four operations also accept the explicit temporary target instead of
`--connection NAME`, without loading saved profiles or history:

```sh
pgsavvy schema list --temporary --port 55432 --database DB_NAME \
  --username RESTRICTED_USER --passfile /ABSOLUTE/PASSFILE
pgsavvy schema tables --temporary --socket-dir /ABSOLUTE/SOCKET_DIRECTORY \
  --port 55432 --database DB_NAME --username RESTRICTED_USER \
  --passfile /ABSOLUTE/PASSFILE --schema ExactSchema
```

Provision the database/login/passfile first; uppercase values are placeholders.
The default host is `127.0.0.1`; sockets still require an explicit port and match
passfile host `localhost`. See [selection/passfile/environment rules](cli-contract.md#temporary-local-targets).
Temporary inspection and connection testing remain read-only metadata/Ping
helpers, not the submitted-query executor; `--allow-write` is invalid here.
Pagination, literal names, catalog-only output and bounds above are unchanged.
No saved `hidden_schemas` applies; the built-in system filter still applies.
Database grants remain authoritative, and local transport proves no ownership.
