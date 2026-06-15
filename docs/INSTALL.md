# Installing and using pgsavvy

pgsavvy is a vim-style terminal UI for PostgreSQL — keyboard-driven schema
browsing, a modal SQL editor, streamed result grids, inline cell editing, and
EXPLAIN plan trees. This guide covers installation, first-run connection
setup, and the configuration files pgsavvy reads.

## Installation

### go install

```sh
go install github.com/davesavic/pgsavvy@latest
```

This places a `pgsavvy` binary in `$(go env GOPATH)/bin` (add it to your
`PATH`).

### Build from source

```sh
git clone https://github.com/davesavic/pgsavvy.git
cd pgsavvy
task build       # produces bin/pgsavvy
```

See [CONTRIBUTING.md](../CONTRIBUTING.md) for the full development toolchain.

### Release binaries

Prebuilt binaries are published on the
[GitHub Releases](https://github.com/davesavic/pgsavvy/releases) page. Download
the archive for your platform, extract the `pgsavvy` binary, and put it on your
`PATH`.

## First run

Launch the binary:

```sh
pgsavvy
```

On first launch pgsavvy opens the **connection manager**. Create a connection
profile there (host, port, database, credentials), save it, and connect.
Profiles are persisted to `~/.config/pgsavvy/connections.yml`, which you can
also edit directly.

Press `?` at any time for the keybinding cheatsheet, and see
[keybindings.md](keybindings.md) for the full reference and how to remap keys.

## Connection profiles (`connections.yml`)

`~/.config/pgsavvy/connections.yml` holds a list of connection profiles. A
minimal example:

```yaml
connections:
  - name: local
    driver: postgres
    dsn: postgres://postgres@localhost:5432/postgres?sslmode=disable

  - name: staging (read-only, via bastion)
    driver: postgres
    dsn: postgres://app@db.internal:5432/app?sslmode=require
    read_only: true
    confirm_writes: true
    hidden_schemas: [pg_catalog, information_schema]
    ssh_tunnel:
      host: bastion.example.com
      user: deploy
      port: 22
      identity_file: ~/.ssh/id_ed25519
```

Credentials resolve through a waterfall — prefer one of these over a plaintext
`password`:

| Field | Meaning |
|-------|---------|
| `password_command` | Command whose stdout is the password |
| `keyring` | Reference into the OS keyring / encrypted file backend |
| `pgpass` | Path to a `~/.pgpass`-style file |
| `password` | Plaintext fallback (development only) |

Useful per-profile options include `read_only`, `confirm_writes`,
`confirm_ddl`, `statement_timeout`, `hidden_schemas`, `role`, and presentation
fields (`label`, `color`, `icon`, `tags`).

## Configuration files

pgsavvy follows the XDG Base Directory layout:

| File | Location | Purpose |
|------|----------|---------|
| `config.yml` | `~/.config/pgsavvy/` | Keybindings, theme, UI/query settings |
| `connections.yml` | `~/.config/pgsavvy/` | Connection profiles |
| `state.yml` | `~/.local/state/pgsavvy/` | App state (last connection, view modes, …) |
| Session logs | `~/.local/state/pgsavvy/sessions/` | Per-session JSON debug logs (redacted) |

### Environment variables

| Variable | Effect |
|----------|--------|
| `PGSAVVY_LOG_DIR` | Override the session-log directory |
| `PGSAVVY_DISABLE_SESSION_LOG=1` | Disable file logging (stderr only) |
| `PGSAVVY_LOG_INCLUDE_SQL=1` | Include SQL text in session logs |
| `PGSAVVY_LOG_INCLUDE_PARAMS=1` | Include bound query parameters in session logs |
| `PGSAVVY_KEYRING_PASSPHRASE` | Passphrase for the file-backed keyring |

Standard `XDG_*` variables (`XDG_CONFIG_HOME`, `XDG_STATE_HOME`, …) relocate
the directories above.
