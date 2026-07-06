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

> **Note:** `go install` builds carry no embedded version metadata, so
> `pgsavvy --version` reports a placeholder and `pgsavvy update` (see below)
> refuses to self-update. Use a release binary if you want in-place updates.

### Build from source

```sh
git clone https://github.com/davesavic/pgsavvy.git
cd pgsavvy
task build       # produces bin/pgsavvy
```

See [CONTRIBUTING.md](../CONTRIBUTING.md) for the full development toolchain.

### Release binaries

Prebuilt binaries are published on the
[GitHub Releases](https://github.com/davesavic/pgsavvy/releases) page. Each
asset is the raw `pgsavvy` binary for one OS/arch (named
`pgsavvy_<tag>_<os>_<arch>`, plus `.exe` on Windows) alongside a
`checksums.txt`. Download the binary for your platform, make it executable, and
put it on your `PATH`:

```sh
# example for linux/amd64; adjust the asset name for your platform
curl -fsSLo pgsavvy https://github.com/davesavic/pgsavvy/releases/latest/download/pgsavvy_<tag>_linux_amd64
chmod +x pgsavvy
mv pgsavvy ~/.local/bin/   # or anywhere on your PATH
```

## Updating (`pgsavvy update`)

A release binary can update itself in place:

```sh
pgsavvy update
```

This downloads the matching asset from the latest GitHub Release, verifies its
SHA256 against `checksums.txt`, and atomically replaces the running executable.
Re-run `pgsavvy` afterwards to use the new version.

- `pgsavvy update` takes no arguments — a stray flag or version is rejected.
- Already on the latest release? It prints an up-to-date message and exits.
- Builds without release metadata (`go install`, dev/source builds) refuse to
  self-update.
- Package-manager / read-only installs (Homebrew, Nix) refuse and tell you to
  update via that manager instead.
- On Windows the previous executable is left beside the new one as a hidden
  `.old` file (a running `.exe` cannot be deleted).

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

`icon` is a free-form glyph rendered as a prefix beside the connection in the
picker, title bars, and commit dialog. Any string works — an emoji, a Nerd Font
glyph, or a plain symbol:

```yaml
connections:
  - name: prod
    driver: postgres
    dsn: postgres://app@db.prod:5432/app?sslmode=require
    label: Production
    color: red
    icon: "🚨"        # emoji

  - name: local
    driver: postgres
    dsn: postgres://postgres@localhost:5432/postgres?sslmode=disable
    icon: ""        # Nerd Font glyph (requires a patched terminal font)
```

When a connection is the active one, the picker shows `●` in place of its icon.

### Colors

The connection `color` field and every theme color field (`config.yml` under
`theme:`, e.g. `keyword`, `table_header`) accept the same color token
vocabulary. One vocabulary resolves everywhere — inline content (grid cells,
side rails, status bar, connection rows, suggestions, cheatsheet, editor syntax
highlighting) **and** pane borders — and applies to both foreground and
background fields:

| Class | Tokens | Example |
|-------|--------|---------|
| Named (16) | `black` `red` `green` `yellow` `blue` `magenta` `cyan` `white` and their bright variants `brightblack` `brightred` `brightgreen` `brightyellow` `brightblue` `brightmagenta` `brightcyan` `brightwhite` | `color: cyan` |
| Gray alias | `gray` / `grey` → `brightblack` | `comment: gray` |
| 256-palette | `colorN` where `N` is `0`–`255` | `keyword: color39` |
| Hex | `#rgb` or `#rrggbb` (case-insensitive) | `color: "#ff8800"` |

Tokens are case-insensitive. An empty or unrecognized token leaves the element
untinted (theme fields fall back to the default theme).

> **Terminal support:** color tokens are emitted as authored — pgsavvy does not
> detect terminal capabilities or down-convert to a nearer color. On terminals
> without 256-color or truecolor support, `colorN` and `#hex` tokens may render
> incorrectly. Stick to the 16 named colors for maximum portability.

Setting `NO_COLOR` (any value) disables color on the inline content path, so
themed content renders plain regardless of the tokens above.

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
| `PGSAVVY_LOG_INCLUDE_SQL=full` | Include **full** SQL text in session logs — **WARNING:** passwords in SQL (e.g. `ALTER USER ... PASSWORD '...'`) are written to disk unredacted. This is a forensic-only option. |
| `PGSAVVY_LOG_INCLUDE_PARAMS=1` | Include bound query parameters in session logs |
| `PGSAVVY_KEYRING_PASSPHRASE` | Passphrase for the file-backed keyring |
| `NO_COLOR` | Disable color on the inline content path (see [Colors](#colors)) |

Standard `XDG_*` variables (`XDG_CONFIG_HOME`, `XDG_STATE_HOME`, …) relocate
the directories above.
