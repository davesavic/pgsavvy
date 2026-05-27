# Filesystem I/O Rule

dbsavvy abstracts file I/O behind [`spf13/afero`](https://github.com/spf13/afero)
so that production code can be exercised in-memory under tests. This doc states
the rule, lists the packages that follow it, and documents the justified raw
`os.*` exceptions.

The package split below was derived from `git grep` over the working tree
(commands at the bottom) — keep it in sync if a new file-I/O site lands.

## The rule

- **Use `afero` for all testable file I/O** — reading, writing, creating,
  `MkdirAll`, `Stat`, opening config / state / log / history files. Take an
  `afero.Fs` (or `afero.Afero`) as a dependency so tests inject
  `afero.NewMemMapFs()` and production injects `afero.NewOsFs()`.
- **Raw `os.*` is allowed ONLY at a true process boundary** that afero does not
  (or cannot) model:
  - **Environment**: `os.Getenv`, `os.Environ`.
  - **Process / host facts**: `os.TempDir`, `os.UserHomeDir`.
  - **Terminal**: file-descriptor (`*.Fd()`) / `term.IsTerminal` checks.
  - A few **explicitly justified** disk operations that afero can't express
    safely (see exceptions).

If you find yourself reaching for `os.Open` / `os.Create` / `os.WriteFile` /
`os.ReadFile` / `os.MkdirAll` in production code, default to `afero` unless your
case is on the exceptions list below.

## Packages that use afero (production code)

- `pkg/app` (`entry_point.go`)
- `pkg/common` (`app_state.go`, `app_state_store.go`, `common.go`, `dummies.go`)
- `pkg/config` (`app_config.go`, `config_write.go`, `connections.go`,
  `connections_write.go`, `doc.go`)
- `pkg/gui/controllers/helpers/data` (`connection_form_helper.go`)
- `pkg/gui/editor` (`persistence.go`)
- `pkg/gui/orchestrator` (`gui.go`)
- `pkg/i18n` (`i18n.go`, `doc.go`)
- `pkg/logs` (`options.go`, `session_logger.go`, `session_logger_forceclose_*.go`)
- `pkg/utils` (`atomic_yaml.go`)

## Justified raw-`os` exceptions (production code)

Each of these uses raw `os.*` for a reason afero can't satisfy:

| File | Call | Why raw `os` |
| --- | --- | --- |
| `pkg/gui/exporter/dest_file.go:35` | `os.OpenFile(..., O_WRONLY\|O_CREATE\|O_EXCL\|O_TRUNC, 0o600)` | Atomic exclusive-create (`O_EXCL`) export write; afero's `MemMapFs` does not honor `O_EXCL` semantics, and the export target is a real on-disk download path. |
| `pkg/session/credentials_keyring.go:135,138` | `os.MkdirAll(..., 0o700)` | Keyring data dir must exist with tight perms on the real OS before the OS keyring backend (99designs/keyring) opens it — it operates on the real FS, not afero. |
| `pkg/session/credentials.go:144` | `os.ReadFile(path)` | Reads a user-configured `.pgpass`-style credentials file from the real FS; path/mode already validated (`//nolint:gosec`). |
| `pkg/query/history.go:57` | `os.MkdirAll(filepath.Dir(path), 0o755)` | Creates the parent dir for the SQLite history DB, which `database/sql` + the sqlite driver open on the real FS by path (not via afero). |
| `pkg/app/entry_point.go:74` | `os.MkdirAll(stateDir, 0o700)` | Bootstrap: creates the state dir before the afero-backed `OsFs` is handed to the rest of the app; also reads env via `os.Getenv` (`entry_point.go:69,175`) — a true process boundary. |

Env/terminal/process-boundary raw-`os` usage (`os.Getenv`, `os.Environ`,
`os.UserHomeDir`, `*.Fd()`) lives in `pkg/env/download_dir.go`,
`pkg/logs/redact.go`, `pkg/logs/session_logger_forceclose_*.go`,
`pkg/session/credentials_exec*.go`, `pkg/session/credentials_keyring.go`, and
`pkg/session/credentials_prompt.go`. These are allowed by the rule and are not
file-content I/O.

## Adding a new file-I/O site

Take an `afero.Fs` as a dependency and write through it:

```go
type Store struct {
	fs afero.Fs
}

func NewStore(fs afero.Fs) *Store { return &Store{fs: fs} }

func (s *Store) Save(path string, data []byte) error {
	if err := s.fs.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return afero.WriteFile(s.fs, path, data, 0o600)
}
```

Production wires `afero.NewOsFs()`; tests wire `afero.NewMemMapFs()`. For
atomic writes prefer the existing helper `utils.AtomicWriteYAML(fs, path, v,
mode)` (`pkg/utils/atomic_yaml.go:22`) rather than hand-rolling temp-file +
rename. Only drop to raw `os.*` if your case matches the exceptions table
above (atomic `O_EXCL`, a real-FS-only backend like keyring / sqlite, or a
process boundary).
