# Agent-friendly CLI research

Research date: 2026-10-15. Source snapshot: `3f8504e`.
Status: research proposal, not an approved implementation plan.

## Recommendation

Make pgsavvy a useful **external database tool**, not an AI application. Add a
small, deterministic, noninteractive CLI that scripts and agents can both call.
The agent chooses operations; pgsavvy resolves the user's saved connections,
enforces access rules, executes database operations, and returns structured data.
No model providers, API keys, chat interface, natural-language SQL generation,
embeddings, or autonomous reasoning belong in pgsavvy.

Start with connection discovery, focused schema inspection, bounded read-only
queries, and machine-readable output. Follow with estimated EXPLAIN plans and
saved-query access. Defer writes, live-TUI control, and MCP until there is a
demonstrated need. All command examples below are **proposed**, not available now.

## What exists today

These are source observations, not claims that every feature has been exercised:

| Existing foundation | Implication for a CLI |
| --- | --- |
| Startup registers `--version`, `--log-dir`, and `update`, then otherwise enters the TUI. [R1] | Database automation needs a new dispatch path before GUI initialization. |
| Saved profiles contain names, tags, endpoint settings, SSH settings, credential references, read-only and confirmation flags, and statement timeout. [R2] | Reuse profiles; do not make agents reconstruct DSNs or retrieve passwords. |
| Session interfaces expose schemas, tables, columns, indexes, constraints, inbound/outbound foreign keys, table statistics, functions, execution, streaming, EXPLAIN, and transactions. [R3] | Useful operations already have non-GUI interfaces. |
| Credential resolution supports inline password, password command, file-backed keyring, explicit pgpass, and optional prompting, in that order. [R4] | Reuse resolution, but define a strict noninteractive contract. |
| CSV, TSV, NDJSON, JSON array, Markdown, and SQL INSERT exporters exist; JSON array buffers rows. [R5] | Reuse appropriate encoders, not the entire GUI export workflow. |
| Saved queries have name/SQL storage helpers; query errors already carry SQLSTATE and diagnostic fields. [R6] | Expose saved queries and selected error fields rather than inventing parallel stores. |

Important qualifications:

- **Keyring means file backend**, not automatically the native OS credential
  store. Unlocking currently uses `PGSAVVY_KEYRING_PASSPHRASE` or a prompter. [R4]
- GUI write confirmations are not enforced by the driver interface. Existing
  `read_only` profiles set `default_transaction_read_only`; that is not a
  separate authorization system. Driver EXPLAIN explicitly leaves execution
  safety to its caller. [R7]
- The GUI export destination dispatcher offers File/Clipboard, not stdout.
  Full export drains into the grid and snapshots rows. Streaming database rows
  are subject to a 64-KiB stored-cell cap. Do not advertise that path as a
  lossless, constant-memory CLI export. [R5]
- SSH currently accepts and persists unknown host keys on first use. Opening a
  connection can also execute configured credential commands or initialize
  keyring directories. Connection testing is not a side-effect-free probe. [R4][R8]
- Default session logging includes a SQL preview and parameter hashes, not a
  guarantee that arbitrary SQL literals are private. Full-SQL/raw-parameter
  logging has environment opt-ins. [R9]

## Lessons from existing tools and specifications

1. **Borrow psql's batch behavior.** File/stdin input, a no-password-prompt
   option, fail-fast script errors, and documented exit statuses are established
   patterns. psql also supports CSV and transactions around scripts. Its
   meta-commands can execute local programs, so a pgsavvy SQL input mode should
   not implicitly become a psql-compatible scripting interpreter. [S1]
2. **Borrow GitHub CLI's selective structured output.** `gh --json` supports
   selecting fields; gcloud also distinguishes machine formats and field
   projections. Focused output avoids dumping an entire database into an
   agent's context. Do not add an embedded jq engine merely because gh has one:
   ordinary pipes plus a small `--fields` option are sufficient initially. [S2][S3]
3. **Use PostgreSQL, not SQL keywords, to enforce read-only transactions.**
   PostgreSQL defines the operations blocked in a read-only transaction.
   This is not a guarantee of no disk writes or no external side effects.
   Roles, grants, callable functions, and row-level security also matter. [S4][S5][S6]
4. **EXPLAIN and EXPLAIN ANALYZE are different tools.** ANALYZE executes the
   statement. Rollback protects ordinary transactional changes but does not
   undo sequence changes; it is not a universal simulation mode. [S7][S8]
5. **MCP is optional transport, not built-in AI.** It provides tool discovery,
   input/output schemas, and structured results. A stdio server can be a local
   subprocess without a public network listener. That is compatible with the
   product boundary, but not necessary for agents that can already run CLI
   commands. Tool annotations are not authorization controls. [S9][S10]

## Proposed features, in priority order

### 1. Connection discovery without credential disclosure

```sh
pgsavvy connections list --format json
pgsavvy connections show staging --format json
pgsavvy connections test staging --format json
```

Return exact connection names, driver, approved tags/labels, access policy, and
whether credentials need human setup. Detailed endpoint identity can be an
explicit field selection. Never serialize the connection model directly:
return a deliberately allowlisted public shape with no raw DSN, passwords,
credential-command text, keyring references, private-key paths, or pgpass paths.

Listing/showing must not connect, resolve credentials, execute helpers, or create
configuration/state files. `test` is an explicit network/credential operation.
Require an exact `--connection NAME` on every database operation; reject unknown
or ambiguous names. Never fall back to the TUI's last-used connection. Bind the
resolved target for the entire invocation rather than re-reading a mutable name
halfway through execution.

**Why first:** this is pgsavvy's main advantage over making the agent call psql:
the user's existing profiles and connection behavior remain inside pgsavvy.

### 2. Focused schema discovery

```sh
pgsavvy schema list --connection staging --format json
pgsavvy schema tables --connection staging --schema public --format json
pgsavvy schema describe --connection staging --schema public --table orders --format json
pgsavvy schema relationships --connection staging --schema public --table orders --format json
```

Provide columns/types/nullability/defaults, primary/foreign keys, indexes, and
inbound/outbound relationships. Include estimated row counts only when requested
and label estimates; avoid an implicit `COUNT(*)`. Support name filtering,
field selection, and bounded/paginated listings. Include comments only as data,
never instructions. Do not sample table contents by default.

Separate schema/table arguments avoid ambiguous parsing of dots inside quoted
identifiers. Quote identifiers through existing PostgreSQL facilities, and bind
values instead of interpolating them. The underlying interface already exposes
most of this metadata. [R3]

**User outcome:** an agent can understand joins and valid columns before writing
SQL, instead of guessing names or asking for a full database dump.

### 3. Bounded, read-only query execution

```sh
pgsavvy query --connection staging --file query.sql --format json --max-rows 100
pgsavvy query --connection staging --file - --params-file params.json --format json
```

Support one SQL statement from a file/stdin, bound `$1` parameters, explicit
schema, and limits on rows, response bytes, individual cells, connection time,
statement time, and total invocation time. SQL and parameter values in files or
stdin avoid putting sensitive content into shell argument history. Never source
shell scripts or interpret psql backslash commands from SQL input.

Start with one-shot processes and one connection/session per invocation. Wrap
execution in an explicitly owned read-only transaction; reject transaction and
session-control statements that could escape its policy. Use PostgreSQL's
extended protocol to reject multiple statements server-side; a SQL parser may
help classify operations, but neither regex classification nor protocol choice
is the authorization boundary. The extended Parse message accepts at most one
statement. [S11]

Limits must be enforced while consuming the stream, not after buffering all
rows. Stop/cancel and clean up when limits are reached; report exactly what was
truncated. An output row cap does not bound server work: aggregations can scan a
large table before producing one row. Use server statement/lock timeouts plus a
client deadline, with version-appropriate settings. PostgreSQL documents these
timeouts separately. [S12]

### 4. Explain queries without executing them by default

```sh
pgsavvy explain --connection staging --file query.sql --format json
```

Return the native JSON plan plus existing deterministic diagnostics where
available, distinguishing estimates from measured values. Plan-based advice is
not AI integration. Bound plan size/time and avoid claiming estimated costs are
milliseconds. PostgreSQL explicitly defines cost units as arbitrary. [S7]

Do not include `--analyze` in the first release. If added later, classify it as
execution, require separate permission, and preserve the same data/timeout
restrictions as queries. Plain EXPLAIN still involves planning and locks; it is
not a blanket guarantee that arbitrary extension or user-defined code is safe.

### 5. Saved queries and controlled exports

```sh
pgsavvy queries list --format json
pgsavvy queries show daily-orders --format json
pgsavvy queries run daily-orders --connection staging --format json
pgsavvy export --connection staging --file query.sql --format csv --output orders.csv
```

Saved-query listing should omit SQL unless requested. Running one must use the
same execution policy as ad hoc SQL: being saved is not approval. Later, support
user-authored query templates with typed parameters and named output fields.
Today's saved-query model contains only name and SQL, so a parameter declaration
schema would be a new capability. [R6]

CSV/TSV and explicitly framed NDJSON are useful export targets; stream directly
from the database rather than through result tabs. Do not silently clip cells
in an export advertised as lossless. Reject an oversized value or implement a
separate, explicit preview mode. Ensure file writes are private, avoid accidental
overwrite, and publish the final file only on complete success. Treat general
exports as data disclosure, even though the SQL is read-only.

### 6. Machine-readable command discovery

```sh
pgsavvy capabilities --format json
pgsavvy command-schema query --format json
```

Publish command names, flags, input/output schemas, format versions, supported
driver features, required permissions, and side-effect categories. Keep these
generated from the actual command definitions to avoid documentation drift.
A short checked-in usage guide can teach external agents the workflow; no
runtime AI dependency or agent-specific detection is needed.

### Later candidates, not first-release requirements

- A focused schema snapshot/diff export for offline analysis and repeatable
  context, with schema/version provenance and no row samples by default.
- A handoff that opens a proposed SQL file in the TUI for human inspection,
  without executing it. Do not equate this with controlling a running TUI.
- Explicitly permissioned operational diagnostics, such as locks or activity,
  accounting for sensitive query text and database privileges.
- Reviewed write execution, transactional batches, and optional MCP adapters,
  only after their authorization/lifecycle contracts are designed.

## Automation contract

These are proposed requirements, not existing guarantees:

- Machine mode never starts a TUI, reads from `/dev/tty`, opens a browser/editor,
  launches a pager, displays spinners, checks for updates, or asks for input.
  Missing/unavailable credentials produce a typed error with safe remediation.
- Stdout contains only the requested data format. Diagnostics go to stderr;
  JSON failures use a documented error envelope. Never expose a raw wrapped
  database error by default: detail/hint/where/notice text can contain values.
- Prefer a column-metadata array plus positional rows in canonical JSON. This
  preserves duplicate column labels. Define lossless representations for
  `int8`/numeric, timestamps, bytea, arrays, JSON, and SQL NULL; do not silently
  convert large integers to imprecise JavaScript numbers. Existing object JSON
  exporters are not automatically the canonical machine contract. [R5]
- Include output schema version, resolved connection name, row count returned,
  warnings, and explicit completeness/truncation metadata. A deliberately capped
  preview can be a successful request while still being an incomplete result.
- NDJSON should have documented metadata/row/final-summary event types. Missing
  final summary means incomplete output. CSV needs exit status and optional
  sidecar metadata because mixing control records into CSV corrupts the format.
- Document stable exit categories: success, invalid input, unavailable
  connection/credentials, policy denied, database failure, timeout/cancel, and
  I/O/incomplete output. Exact numeric assignments can be frozen during design;
  do not collapse all failures into a human-readable `log.Fatal` message.
- SIGINT/SIGTERM, broken pipes, credential-helper timeouts, and stalled
  connections require bounded cleanup. Cancellation is not proof that a future
  writable transaction did not commit; never automatically retry an ambiguous
  write. Do not promise arbitrary query pagination across fresh processes:
  stable ordering/snapshot semantics need explicit design.

## Safety model: tool access is delegated authority

### Enforce access in the operation layer

Recommend opt-in CLI exposure per connection, with distinct disabled,
metadata-only, and row-read scopes. Keep unrestricted SQL/export separate from
metadata discovery. Existing tags, hidden-schema settings, and GUI confirmation
flags are not a database ACL. In particular, a schema filter on discovery does
not restrict arbitrary SQL to those schemas.

Prefer dedicated least-privilege database logins, approved views/column grants,
and row-level security where appropriate. Review function EXECUTE privileges
and security-definer functions as well as table SELECT: PostgreSQL grants
function execution to PUBLIC by default, and owners/superusers/BYPASSRLS roles
can bypass row-level policies. [S5][S6] The CLI must not broaden a saved profile's
read-only setting or bypass a required user policy with a generic `--yes`.

**Boundary limitation:** an agent with the user's unrestricted shell and file
access can read editable policy files, invoke other database clients, and
potentially reach credential sources directly. CLI scopes protect cooperative
use and mistakes; they do not sandbox that agent. Strong isolation needs a
restricted execution identity/environment and restricted database credentials.
Do not claim that a flag or a same-user approval file proves human consent.

### Credentials, SSH, and privacy

- Preserve the existing credential resolver, while preventing all interactive
  fallback in machine mode. Unlock/setup is a separate human action. Passphrases
  and raw DSNs must never be printed as a tool result. PostgreSQL recommends
  password files over `PGPASSWORD` because process environments can be exposed;
  pgpass also has documented private-permission requirements. [S13][S14]
- Treat configured password/passphrase commands as trusted executable code,
  with timeouts and cancellation. Do not permit SQL/results or untrusted
  workspace configuration to introduce or override such commands.
- Recommend strict pre-established SSH host-key verification for noninteractive
  execution, rather than silently inheriting first-use trust. Unknown keys
  should require user setup outside the agent operation. This needs an explicit
  change from current behavior, not merely a new CLI flag. [R8]
- Default CLI audit events should record operation, target identifier, timing,
  outcome, and counts without SQL literals, parameters, or row data. Make
  history persistence explicit and avoid silently inheriting verbose logging
  environment switches. Local logs may contain sensitive data even when no
  password is present. [R9]
- Database comments, identifiers, stored SQL, and row values are untrusted data.
  JSON escaping prevents control-character corruption, not prompt injection.
  Enforce permissions independently of what the external model reads. OWASP
  recommends least privilege and action-specific approval outside the model.
  Read-only access can still disclose personal or confidential data to the
  external agent/provider; users must authorize that disclosure. [S15]

### Writes, if ever added

Keep writes out of the first slice. For later support, prefer proposal/review/
execute with approval bound to the exact SQL, parameters, resolved target,
policy, and expiry, verified through a boundary the agent cannot forge. Use
transactions where supported, but do not market rollback as a full dry run.
An `--allow-write` flag can express intent in trusted scripts; it cannot prove
that a human approved an agent's action. [S8][S15]

## CLI versus MCP

**Build the CLI first.** It is useful to humans, scripts, CI, and shell-capable
agents and does not require an agent-specific protocol. Keep policy and
operation implementations separate from terminal formatting so the TUI and CLI
can share appropriate logic without constructing GUI state.

If an MCP-only client later needs access, add a thin optional local stdio adapter
or separate wrapper over that same operation/policy layer. MCP adds discovery
and schemas, not reasoning. It should not introduce model credentials, a public
HTTP service, weaker permissions, or a second independent SQL executor. MCP's
stdio contract also forbids unrelated stdout output. [S9][S10]

## Smallest worthwhile first release

Ship `connections list/show/test`, `schema list/tables/describe/relationships`,
and `query`, with canonical JSON, file/stdin SQL, bound parameters, exact
connection selection, noninteractive credential errors, explicit read-only
transactions, bounded results/timeouts, and opt-in connection access. Include a
machine-readable capability summary; a full per-command schema API can follow.
Do not ship the query command without its safety/output contract.

Estimated EXPLAIN and saved-query reads/runs are the next useful slice. Leave
lossless bulk exports, connection mutation, history browsing, SQL writes, and
MCP for separate decisions. No implementation duration was measured here.

Before shipping, verify: no-TTY behavior; no credential disclosure in output,
errors or logs; exact target selection; multiple-statement/control-statement
rejection; writes rejected on a restricted database role; unknown SSH keys
rejected; timeout/cancellation cleanup; duplicate columns; large numbers/NULL;
oversized values; result-limit metadata; and partial/broken-pipe output behavior.
Use isolated PostgreSQL/SSH fixtures, not the user's real connections.

Relevant tracked follow-ups already exist: `pgsavvy-pjtk` for pgpass/DSN target
precedence, `pgsavvy-k6qm` for stalled SSH-read cancellation, and `pgsavvy-kkc`
for live SQL-INSERT export encoder wiring. These are investigation/design inputs,
not assertions that this research fixed them. Recheck their status and source
when implementation begins; none should be concealed by an automation wrapper.

## Evidence and limitations

Reviewed current source and official PostgreSQL, GitHub CLI, Google Cloud CLI,
MCP, and OWASP documentation. PostgreSQL citations are pinned to version 18;
check supported server versions before choosing settings/features. MCP citations
are pinned to the 2025-11-25 specification, not a claim about the latest revision.

Runtime checks: `mise exec -- go run -ldflags '-X main.version=research-check
-X main.buildSource=source' . --version` printed the injected version;
`mise exec -- go run . --format=json` rejected the unregistered flag before GUI
startup. No real connection profiles/credentials were inspected, no database was
queried, and no full application test suite was run. Proposed commands, security
properties, performance, and protocol compatibility remain unimplemented and
unverified.

### Repository sources

- [R1] [CLI dispatch and startup](../pkg/app/entry_point.go#L55-L95), [GUI launch](../pkg/app/entry_point.go#L157-L188), [main and registered driver](../main.go#L24-L36).
- [R2] [Connection profile fields](../pkg/models/connection.go#L5-L48), [connection loading and executable-config trust](../pkg/config/connections.go#L43-L100).
- [R3] [Driver/session/stream interfaces](../pkg/drivers/driver.go#L20-L117).
- [R4] [Credential waterfall](../pkg/session/credentials.go#L39-L145), [file-only keyring and unlocking](../pkg/session/credentials_keyring.go#L38-L143).
- [R5] [Formats/destinations and buffered row sources](../pkg/gui/controllers/helpers/ui/result_tabs_helper.go#L3807-L3941), [buffered JSON array](../pkg/gui/exporter/jsonarray.go#L10-L64), [stream cell cap](../pkg/drivers/pg/cell_cap.go#L11-L24).
- [R6] [Saved-query model](../pkg/models/saved_query.go#L3-L9), [saved-query writers](../pkg/config/queries_write.go#L18-L90), [structured query errors](../pkg/drivers/errors.go#L27-L42).
- [R7] [GUI confirmations](../pkg/gui/controllers/query_editor_controller.go#L636-L708), [read-only connection defaults](../pkg/session/profile.go#L210-L220), [EXPLAIN caller responsibility](../pkg/drivers/pg/session.go#L682-L687).
- [R8] [SSH trust-on-first-use behavior](../pkg/session/sshtunnel/knownhosts.go#L21-L71).
- [R9] [SQL previews and parameter logging](../pkg/session/log_helpers.go#L12-L89).

### External primary sources

- [S1] [PostgreSQL 18 psql](https://www.postgresql.org/docs/18/app-psql.html): batch input, prompts, scripting, exit codes, local-program meta-commands.
- [S2] [GitHub CLI formatting](https://cli.github.com/manual/gh_help_formatting): JSON field selection and formatting.
- [S3] [gcloud formats](https://docs.cloud.google.com/sdk/gcloud/reference/topic/formats): machine formats and projections.
- [S4] [PostgreSQL 18 SET TRANSACTION](https://www.postgresql.org/docs/18/sql-set-transaction.html): read-only restrictions and limits.
- [S5] [PostgreSQL 18 privileges](https://www.postgresql.org/docs/18/ddl-priv.html): table/column/function privileges and PUBLIC defaults.
- [S6] [PostgreSQL 18 row security](https://www.postgresql.org/docs/18/ddl-rowsecurity.html): owner/superuser bypass and security-definer considerations.
- [S7] [PostgreSQL 18 EXPLAIN](https://www.postgresql.org/docs/18/sql-explain.html): JSON plans, arbitrary cost units, and ANALYZE execution.
- [S8] [PostgreSQL 18 sequence functions](https://www.postgresql.org/docs/18/functions-sequence.html): sequence changes survive rollback.
- [S9] [MCP tools, 2025-11-25](https://modelcontextprotocol.io/specification/2025-11-25/server/tools): discovery, schemas, structured results, and safety requirements.
- [S10] [MCP transports, 2025-11-25](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports): local stdio subprocess and stdout contract.
- [S11] [PostgreSQL 18 protocol flow](https://www.postgresql.org/docs/18/protocol-flow.html#PROTOCOL-FLOW-EXT-QUERY): extended Parse rejects multiple statements.
- [S12] [PostgreSQL 18 client settings](https://www.postgresql.org/docs/18/runtime-config-client.html): statement, transaction, lock, and idle timeouts.
- [S13] [PostgreSQL 18 password files](https://www.postgresql.org/docs/18/libpq-pgpass.html): pgpass and permissions.
- [S14] [PostgreSQL 18 environment variables](https://www.postgresql.org/docs/18/libpq-envars.html): password-environment exposure warning.
- [S15] [OWASP prompt-injection prevention](https://cheatsheetseries.owasp.org/cheatsheets/LLM_Prompt_Injection_Prevention_Cheat_Sheet.html): untrusted tool data, least privilege, and action-specific approval.
