# dbsavvy

A vim-style TUI database client built like [lazygit](https://github.com/jesseduffield/lazygit) — fast keyboard navigation, modal panes, and a focused workflow for browsing and querying relational databases from the terminal.

## Status

Active development. PostgreSQL is the only supported driver so far; breaking changes may occur. Work is tracked through `dbsavvy-*` epics in the beads issue tracker.

## Features

- **PostgreSQL connectivity** (pgx) with connection profiles, optional SSH tunnels, and credential strategies: OS keyring, `~/.pgpass`, password command, or interactive TUI prompt.
- **Schema browsing** — left-rail navigation across schemas, tables, columns, and indexes, with per-rail search (`/`, `n`/`N`), refresh, hidden-schema toggles, and a table-inspect modal (columns, constraints, FKs, indexes).
- **Vim-like SQL editor** — modal editing (Normal/Insert/Visual/Visual-Block/Operator-Pending) with motions, operators, text objects (including `is`/`as` for SQL statements), registers, undo/redo, `.` repeat, counts, syntax highlighting (chroma), formatting (sqlfmt), and SQL omni-completion (`<c-x><c-o>` or auto-trigger) sourced from schema objects, functions, and history.
- **Query execution** — run statement-at-cursor or all statements, EXPLAIN / EXPLAIN ANALYZE with an interactive plan tree and plan-doctor insights, write/DDL confirmation gates (including writable-CTE detection), per-statement timeouts, cancellation, and transaction control.
- **Result grids** — multiple result tabs (pin/close/cycle), streamed rows with pagination and read-to-end, in-grid search and sort, hide-columns overlay, grid ↔ expanded record view, cell/row yank to clipboard, and export to CSV/TSV/JSON (clipboard or file).
- **Inline cell editing** — edit cells by value or SQL expression, stage pending edits per table, review in a commit dialog with optimistic-concurrency conflict resolution and typed-name confirmation for writes.
- **Query history** — SQLite-backed persistent history with a recall popup.
- **Discoverability** — auto-generated cheatsheet (`?`), which-key popup for `<leader>` chords, fully customizable keybindings via YAML config.
- **Theming & i18n** — configurable colors (named/hex, truecolor-capable) and locale-aware translations with English fallback.
- **Session logs** — per-session structured JSON logs with secret redaction and automatic retention.

## Quick Start

```sh
task build       # produces bin/dbsavvy with -ldflags-injected version metadata
bin/dbsavvy      # starts the TUI and opens the connection manager
```

On first run, create a connection profile in the connection manager (or edit `~/.config/dbsavvy/connections.yml` directly), then connect. Press `?` for the keybinding cheatsheet; see `docs/keybindings.md` for the full reference.

## Configuration

XDG Base Directory layout:

| File | Location | Purpose |
|------|----------|---------|
| `config.yml` | `~/.config/dbsavvy/` | Keybindings, theme, UI/query settings |
| `connections.yml` | `~/.config/dbsavvy/` | Connection profiles |
| `state.yml` | `~/.local/state/dbsavvy/` | App state (last connection, view modes, …) |
| Session logs | `~/.local/state/dbsavvy/sessions/` | Per-session JSON debug logs (redacted) |

Useful environment variables: `DBSAVVY_LOG_DIR` (override log directory), `DBSAVVY_DISABLE_SESSION_LOG=1` (stderr-only logging), standard `XDG_*` overrides.

## Requirements

- [Go 1.25](https://go.dev/dl/)
- [go-task](https://taskfile.dev) v3 — `go install github.com/go-task/task/v3/cmd/task@latest`
- [golangci-lint](https://golangci-lint.run) v2 — `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.7.0`
- [Docker Compose](https://docs.docker.com/compose/) — optional, only needed for the Postgres / SSH-tunnel integration fixtures

### Optional dev tooling

- `gofumpt` — IDE save-on-format hook. Not required for `task fmt` or CI; golangci-lint v2 bundles `gofumpt` and `goimports` as built-in formatters.

## Development

```sh
task --list            # all available tasks
task build             # compile to bin/dbsavvy
task test              # unit tests (forwards args: task test -- -run TestX)
task lint              # golangci-lint v2
task fmt               # gofumpt + goimports via golangci-lint formatters
task vulncheck         # pinned govulncheck

task pg:up             # bring up the Postgres integration fixture
task test:integration  # integration tests (requires DBSAVVY_TEST_PG + fixture)
task test:all          # unit + integration
task pg:down           # tear down the fixture (removes container + volume)

task sshtunnel:up      # SSH bastion + private Postgres fixture (tunnel tests)
task sshtunnel:down
```

Integration tests are gated by `DBSAVVY_TEST_PG`; `internal/pgprobe` fail-loud checks reachability before the suite runs so it can't silently skip.

### Integration fixture gotcha

Bringing the Postgres fixture up against a pre-existing `pgdata` volume skips the env-driven init step — the official `postgres` image only honors `POSTGRES_USER` / `POSTGRES_DB` on an empty data directory. Before running integration tests against a fresh schema, tear the stack down with the volume:

```sh
task pg:down && task pg:up
```

## Documentation

- `docs/keybindings.md` — full keybinding reference
- `docs/QA_TEST_SUITE.md` — manual QA test suite
- `docs/review/` — per-pane behavior review notes
- `docs/dev/` — developer notes (dispatch, filesystem)

## License

[MIT](LICENSE) — Copyright (c) 2026 Dave Savic.
