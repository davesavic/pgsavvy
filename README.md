# pgsavvy

[![CI](https://github.com/davesavic/pgsavvy/actions/workflows/ci.yml/badge.svg)](https://github.com/davesavic/pgsavvy/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

A vim-style TUI PostgreSQL client built like [lazygit](https://github.com/jesseduffield/lazygit) — fast keyboard navigation, modal panes, and a focused workflow for browsing and querying your database from the terminal. The name is short for "PostgreSQL savvy".

![pgsavvy demo](docs/demo.gif)

## Status

Active development. PostgreSQL is the only supported driver so far; breaking changes may occur.

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

## In action

**SQL omni-completion** — auto-triggered table/column suggestions sourced from schema objects, functions, and history (`<c-n>`/`<c-p>` to navigate, `<c-y>` to accept):

![pgsavvy omni-completion](docs/demo-autocomplete.gif)

**Foreign-key navigation** — open the relationship panel (`<leader>gr`) to see a row's parents and children, then follow a foreign key into the referenced row with `gd` (and `<c-o>` to jump back):

![pgsavvy foreign-key navigation](docs/demo-relationships.gif)

## Install

### go install

```sh
go install github.com/davesavic/pgsavvy@latest
```

### Build from source

```sh
git clone https://github.com/davesavic/pgsavvy.git
cd pgsavvy
task build       # produces bin/pgsavvy with -ldflags-injected version metadata
```

### Release binaries

Prebuilt binaries are published on the [Releases](https://github.com/davesavic/pgsavvy/releases) page — download the archive for your platform and put the `pgsavvy` binary on your `PATH`.

See the [install & usage guide](docs/INSTALL.md) for full details.

## Quick Start

```sh
pgsavvy          # starts the TUI and opens the connection manager
```

On first run, create a connection profile in the connection manager (or edit `~/.config/pgsavvy/connections.yml` directly), then connect. Press `?` for the keybinding cheatsheet; see [docs/keybindings.md](docs/keybindings.md) for the full reference.

## Configuration

XDG Base Directory layout:

| File | Location | Purpose |
|------|----------|---------|
| `config.yml` | `~/.config/pgsavvy/` | Keybindings, theme, UI/query settings |
| `connections.yml` | `~/.config/pgsavvy/` | Connection profiles |
| `state.yml` | `~/.local/state/pgsavvy/` | App state (last connection, view modes, …) |
| Session logs | `~/.local/state/pgsavvy/sessions/` | Per-session JSON debug logs (redacted) |

Useful environment variables: `PGSAVVY_LOG_DIR` (override log directory), `PGSAVVY_DISABLE_SESSION_LOG=1` (stderr-only logging), standard `XDG_*` overrides. See [docs/INSTALL.md](docs/INSTALL.md#environment-variables) for the full list.

## Requirements

- [Go 1.25](https://go.dev/dl/)
- [go-task](https://taskfile.dev) v3 — `go install github.com/go-task/task/v3/cmd/task@latest`
- [golangci-lint](https://golangci-lint.run) v2 — `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.7.0`
- [Docker Compose](https://docs.docker.com/compose/) — optional, only needed for the Postgres / SSH-tunnel integration fixtures

## Development

```sh
task --list            # all available tasks
task build             # compile to bin/pgsavvy
task test              # unit tests (forwards args: task test -- -run TestX)
task lint              # golangci-lint v2
task fmt               # gofumpt + goimports via golangci-lint formatters
task vulncheck         # pinned govulncheck

task pg:up             # bring up the Postgres integration fixture
task test:integration  # integration tests (requires PGSAVVY_TEST_PG + fixture)
task test:all          # unit + integration
task pg:down           # tear down the fixture (removes container + volume)

task sshtunnel:up      # SSH bastion + private Postgres fixture (tunnel tests)
task sshtunnel:down
```

Integration tests are gated by `PGSAVVY_TEST_PG`; `internal/pgprobe` fail-loud checks reachability before the suite runs so it can't silently skip. See [CONTRIBUTING.md](CONTRIBUTING.md) for the full contributor workflow.

### Integration fixture gotcha

Bringing the Postgres fixture up against a pre-existing `pgdata` volume skips the env-driven init step — the official `postgres` image only honors `POSTGRES_USER` / `POSTGRES_DB` on an empty data directory. Before running integration tests against a fresh schema, tear the stack down with the volume:

```sh
task pg:down && task pg:up
```

## Documentation

- [docs/INSTALL.md](docs/INSTALL.md) — install & usage guide
- [docs/keybindings.md](docs/keybindings.md) — full keybinding reference
- [CONTRIBUTING.md](CONTRIBUTING.md) — development workflow for contributors
- [SECURITY.md](SECURITY.md) — vulnerability disclosure

## License

[MIT](LICENSE) — Copyright (c) 2026 Dave Savic.
