# Cross-Cutting Subsystems Review

Audit of subsystems that aren't bound to a single panel but underpin many. Source: working tree as of commit `288e31e`.

## Keybinding System

### Purpose
Trie + matcher pipeline that turns one keystroke at a time into a dispatched `Command`, supporting vim-style chord prefixes, counts, registers, leader expansion, which-key popup, and disabled-action toasts.

### User-observable behavior
- Chord prefixes buffer keys, display a which-key hint after a configurable delay, then either fire on the next key or time out.
- Leader (default `<space>`) and local-leader (default `,`) tokens expand at Build time from the runtime `leader`/`local_leader` config fields.
- Insert/Command-mode passthrough: a printable rune that doesn't match a binding (and no partial is in flight) is forwarded to the underlying text area; non-printable but editor-safe keys (`<bs>`, `<del>`, arrows, `<home>`, `<end>`) also fall through.
- Vim-style count collection (1..999999 with overflow clamp) in Normal/Visual modes only; digits in Insert/Command are text.
- Register prefix `"<x>` modelled as a one-key buffer, guarded so user-bound `"` is not stolen.
- Ambiguous-leaf timeout fires the shorter leaf when timer expires; insert-mode pending runes are flushed to the text area on the main loop.
- Disabled-binding refusals raise a toast `"<label>: <reason>"`.
- `:reload` swaps the entire TrieSet atomically; any in-flight pending chord is cancelled before publish, so a partial cannot cross a reload boundary.

### Triggers / keybinds (globally-bound, not context-specific)
- `<c-c>` — `app.quit` (GLOBAL, Normal). Shipped by `QuitController`.
- `?` — `help.cheatsheet` (GLOBAL, Normal). Shipped by `QuitController` (handler is currently a stub).
- `<leader>q` — `app.quit` (GLOBAL, Normal). Shipped via `GetDefaultConfig().Keybindings`.
- `:` — `command.open` (Normal, `scope: all`). Shipped by `keys.DefaultCommandLineBindings`.
- `<esc>` — `command.cancel` (COMMAND_LINE, ModeCommand). Same source.
- `<cr>` — `command.submit` (COMMAND_LINE, ModeCommand). Same source.
- `<leader>1` .. `<leader>9` — `result.tab.jumpN` (GLOBAL, Normal). Shipped by `ResultTabsController` so the digit jumps fire from any focused view.
- Rail-switch chords `1` `2` `3` `4` `5` `6` and `<tab>` — `rail.switch.{schemas,tables,columns,indexes,query_editor,results,next}`. Re-published per side-rail context (CONNECTIONS, SCHEMAS, TABLES, COLUMNS, INDEXES, QUERY_EDITOR, RESULT_GRID) rather than at GLOBAL, so each focused view receives them. Source: `controllers/shared.go` + `RegisterRailSwitchActions`.

(`<c-l>` / `<c-w>v` / generic focus-cycling chords described in design docs are NOT bound; only `<tab>` cycles.)

### Persisted state / file locations
- Bindings persist in `$XDG_CONFIG_HOME/dbsavvy/config.yml` under `keybindings:`. Loader reads via `config.LoadUserConfig` (afero); writes via atomic-yaml helper at mode 0600, parent dir 0700.
- Default leader `' '`, local-leader `','`, `timeout_len` 1s, `ttimeout_len` 50ms, `whichkey_delay` 300ms (`config.GetDefaultConfig`).

### Gaps / TODOs / dead-looking code
- `keys.DispatchResult.Cancelled` and `Swallowed` are documented as "reserved"; `Cancelled` IS now used by master editor for insert-flush, but `Swallowed` has no production emit site beyond the `<nop>` sentinel.
- `ChordBinding.OpensMenu` and `ShowInBar` fields are parsed from YAML but no consumer reads them; the options bar reads bindings directly.
- `command:` shorthand records a `CustomCmd` source for cheatsheet glyph rendering, but the dispatch handler is `NopSentinel` — custom shell-style commands are NOT wired yet (epic E11 deferred).
- `KeybindingService` is stateless; its existence as a struct is forward-looking.
- `allKnownContexts` is a manually-curated slice; adding a new ContextKey requires touching this list (the dlp.11 completeness test will fail loudly, but the coupling is fragile).

## Modes

### Purpose
Bitmask `types.Mode` (Normal=0, Insert=2, Visual, VisualLine, VisualBlock, OperatorPending, Command, Replace) threaded through every keybinding dispatch and the matcher's count/register/passthrough logic.

### User-observable behavior
- Mode label is the first section of the status bar (`status.BuildStatusLine`); empty `modeLabel` collapses the slot.
- Mode determines insert/command passthrough behaviour: a printable rune outside a binding goes to the underlying gocui text area.
- Per-scope `ModeStore` so two contexts can have independent modes (e.g. query editor in Insert while a popup is in Command).
- Visual / VisualLine / VisualBlock are declared but only Normal/Insert/Command/Visual have observable dispatch paths today.

### Triggers / keybinds
Mode transitions are controller-internal (e.g. `i` enters Insert in `vim_editor_controller`). No global mode-switch chord.

### Persisted state / file locations
None directly; mode is process-local. `AppState.LastResultViewMode` is a separate "view mode" enum unrelated to editor mode.

### Gaps / TODOs / dead-looking code
- `ModeVisualLine`, `ModeVisualBlock`, `ModeOperatorPending`, `ModeReplace` are declared and round-trippable through `Mode.String()`, but no controller currently transitions into them. `<c-v>` visual-block is parsed in the config grammar and the result tab `<c-v>` chord exists but its handler is `ResultSelectBlock` (not a mode-entry).
- `modes/` subpackage is described as "stub" in CLAUDE.md and contains no production code.
- `Mode.Has` has special-case semantics for `ModeNormal` (because zero-sentinel) which any future composite-mode code must remember; documented but easy to miss.

## Session & Credentials

### Purpose
Credential resolution waterfall, pgx pool wiring, SQL session lifecycle, query history persistence, and run-handle bookkeeping for the query execution pipeline.

### User-observable behavior
- Credentials resolved in order: inline `password` → `password_command` (shell) → `keyring` (99designs file backend) → explicit `pgpass` file → interactive prompter. Each empty result falls through; each error short-circuits.
- TUI mode installs `TUIRefusePrompter`: the final step refuses with a typed sentinel so the toast layer can advise "configure password_command, keyring, or pgpass".
- CLI mode (non-TUI) uses `TerminalPrompter` (`golang.org/x/term.ReadPassword`); writes "<hint>: " to stderr.
- Loose-mode keyring item files (perm > 0600) trigger a stderr warning AND are auto-chmoded to 0600.
- Pgpass files with world/group perms refuse to load (libpq parity).
- Non-loopback host with `sslmode=disable` (or default `prefer` falling back to plaintext) prints a single stderr `WARN`.
- DSN inline credentials are scrubbed before any error string leaves the package (`RedactDSN`).
- `statement_timeout` validated against a regex AND a hard-coded unit allowlist before interpolation into the SET command.
- Pool: MinConns 2, MaxConns 8, lifetime 30m, idle 5m, healthcheck 1m. `AfterConnect` re-applies `SET statement_timeout` and (optionally) `SET default_transaction_read_only = on` on every recycled conn.
- Each executed statement records to `HistoryRecorder` (panic-recovered).

### Persisted state / file locations
- Connection profiles: `$XDG_CONFIG_HOME/dbsavvy/connections.yml` (atomic-write, single creator via O_EXCL; YAML wrapper `{connections: [...]}` with KnownFields strict decode).
- Keyring file backend: `$XDG_DATA_HOME/dbsavvy/keyring/` (passphrase via `$DBSAVVY_KEYRING_PASSPHRASE` env, falling back to prompter).
- Query history: `$XDG_STATE_HOME/dbsavvy/history.sqlite` (SQLite + FTS5, WAL mode, single connection, background writer goroutine, 128-entry channel with drop-oldest overflow, batch flush every 100ms or 50 entries).
- Explicit pgpass: profile-specified path, mode 0600 enforced.

### Gaps / TODOs / dead-looking code
- `ResolvePassword`'s prompter-empty-result returns `errNoCredentialMechanism` — but the TUI prompter ALWAYS returns the refusal sentinel, so the "prompter present + empty value" branch is unreachable in TUI mode.
- DSN parsing in `parseDSNFields` only handles URL-form DSNs; keyword/value DSNs ("host=... user=...") are explicitly out of scope. Affects pgpass matching.
- `History.dropped` atomic counter is incremented on channel overflow but never exposed to the user (no toast / no logs).
- `History` SQLite path is hardcoded inside `gui.go` line 981 (`filepath.Join(env.GetStateDir(), "history.sqlite")`); not user-configurable. No rotation/pruning.
- `noopHistoryRecorder` is the fallback; if `query.New` fails at startup the GUI continues with no history (silent).
- `credentials_exec_windows.go` exists but the non-windows variant is `//go:build !windows`; Windows password_command parity is not exercised in CI.

## App State Persistence

### Purpose
Persistent per-user state stored at `$XDG_STATE_HOME/dbsavvy/state.yml`, serialised under a mutex with a 500ms debounced background save (and explicit Flush on shutdown).

### User-observable behavior
- Last selected connection is restored on next launch.
- Recent connections list ordering follows MRU.
- Last result view mode (table / expanded) restored.
- Per-connection buffer UUID — query-editor buffer content survives restart, scoped by hashed connection ID.
- Per-connection hidden schemas / per-(connection, table) hidden columns survive restart.
- Startup-tip overlay shows only when `StartupTipsSeenAt` is zero; stamped on dismiss.

### Persisted state / file locations
- Path: `$XDG_STATE_HOME/dbsavvy/state.yml`.
- Mode: temp file `*.tmp` written at 0600, atomic-rename to final; parent dir 0700.
- Format: YAML.

### AppState fields
- `LastConnectionID string` — last selected connection.
- `RecentConnectionIDs []string` — MRU.
- `LastBufferUUIDs map[string]string` — hashed-connID → buffer UUID; values created lazily via crypto/rand v4 UUIDs.
- `LastTheme string` — declared, persistence path exists but no consumer toggles theme.
- `LastResultViewMode string` — "table" / "expanded" per dbsavvy-uv0.7.
- `StartupTipsSeenAt time.Time` — first-run tip dismissal stamp.
- `Version string` — declared; no migration logic yet.
- `StatementTimeoutOverride map[string]string` — declared; not yet wired to a UI surface.
- `HiddenSchemas map[string][]string` — per-connID list of hidden schema names.
- `HiddenColumns map[string]map[string][]string` — connID → (baseTable → hidden columns) for dbsavvy-uv0.6.
- `LastSessionSettings map[string]map[string]string` — declared; consumer is unclear.

### Gaps / TODOs / dead-looking code
- `Version` field exists but no upgrade/downgrade code reads it.
- `StatementTimeoutOverride` and `LastSessionSettings` are declared but no helper writes them.
- `LastTheme` persisted but no theme picker; theme is only set via `config.yml`.
- `AppState.Save` documents that concurrent mutation will panic yaml.Marshal; the only safe writer is `AppStateStore.MutateAndSave`, but `AppState` is still exposed via `Common.AppState` in `NewCommon` — call sites could in principle mutate directly and crash.
- `connIDHashKey` truncates SHA-256 to 8 bytes; collision probability is negligible at this scale but the choice is undocumented.

## Exporters & Run Orchestrator

### Purpose
`pkg/gui/exporter/` provides a Format/Destination/RowSource three-axis pipeline; `Run` drives a single export to completion with progress callbacks, cancellation, and atomic-rename for file destinations.

### User-observable behavior
- Opened via `<leader>oe` from a result tab.
- Menu picks Format × Destination × Scope.
- File destination writes to `*.partial` then atomically renames; failure path removes the partial and reports an error.
- Clipboard destination buffers up to `clipboard_max_bytes` (default 16 MiB, cap 1 GiB) and pushes the buffer once on Close; over-cap writes fail.
- Stdout destination wraps `os.Stdout` in a `nopCloser` so subsequent process writes still work.
- Scope=Full triggers `ReadToEnd` and blocks the worker goroutine until all server rows are drained.
- Scope=Full while a filter is active raises a typed-YES confirmation popup ("y" to confirm).
- Buffered formats (Markdown, JSON Array) display a "≥ N rows" warning label when estimated rows exceed `buffered_row_warn_threshold` (default 100k).
- Progress callback ticks at min(every 5000 rows, every 1s); a final tick fires after Footer.
- All cell values pass through `grid.SanitizeCellEscapes` (CSV, TSV, Markdown, SQL INSERTs); JSON Array / NDJSON skip it because `encoding/json` already escapes C0 controls and ANSI escapes into safe `\u` sequences.

### Format → Destination matrix

| Format       | Streaming | File | Clipboard | stdout | Sanitizer route          |
|--------------|-----------|------|-----------|--------|--------------------------|
| CSV          | yes       | yes  | yes (cap) | yes    | grid.SanitizeCellEscapes |
| TSV          | yes       | yes  | yes (cap) | yes    | grid.SanitizeCellEscapes |
| NDJSON       | yes       | yes  | yes (cap) | yes    | encoding/json (implicit) |
| JSON Array   | no (buf)  | yes  | yes (cap) | yes    | encoding/json (implicit) |
| Markdown     | no (buf)  | yes  | yes (cap) | yes    | grid.SanitizeCellEscapes |
| SQL INSERTs  | yes*      | yes  | yes (cap) | yes    | driver encoder           |

`SQL INSERTs` is only offered when the active tab's `ResultIdentity.HasRowIdentity` is true (i.e. the result is identifiable to a base table); otherwise the option is omitted.

### Size gates (AD-17)
- `buffered_row_warn_threshold` (default 100k) — buffered formats display the warn label; the user still confirms. There is NO hard block above this threshold in the current code path: it is informational only. AD-17's "hard-block above threshold" intent is NOT enforced in master today.
- `clipboard_max_bytes` (default 16 MiB, hard cap 1 GiB) — clipboard destination's `cappedBuffer.Write` returns an error when the limit is exceeded; this IS a hard block.

### Path sanitisation (AD-15)
- `SanitizeComponent` strips everything outside `[A-Za-z0-9._-]` to `_`, strips leading dots, truncates to 100 bytes, returns `"_"` for empty result.
- `DefaultFilename` builds `<conn>_<table>_<UTC-RFC3339-ish-ts>.<ext>` with each component sanitised.
- `ContainedUnder` rejects path-traversal attempts; the file destination calls it before opening the partial.
- Partial file opened with `O_WRONLY|O_CREATE|O_EXCL|O_TRUNC` at mode `0600`.
- On Close, atomic `os.Rename` partial → final. On error, `Abort` removes the partial (best-effort).

### Cell sanitiser (AD-16)
- `grid.SanitizeCellEscapes` strips ANSI CSI/OSC escape introducers and C0 controls (except `\t` and `\n`); plain text and `\t`/`\n` are preserved verbatim.
- Routed via column-names and per-cell-value through CSV/TSV/Markdown/SQL writers; bypassed by JSON variants because `encoding/json` already produces safe output.

### Run lifecycle
1. `dest.Open()` → `(io.WriteCloser, descriptor, error)`.
2. `format.Header(cols, w)`.
3. `src.Iterate(fn)` calls `fn` per row; ctx-cancel checked at row boundary.
4. Per-row `format.Row(r, w)` with progress callbacks.
5. `format.Footer(w)`.
6. `wc.Close()` (atomic rename for file dest).
7. Final progress tick.
- On any error: file dest's `Abort` removes `.partial`; clipboard buffer is discarded; stdout left open.

### Gaps / TODOs / dead-looking code
- Clipboard destination is wired with `ClipboardWriter == nil` (test stub) in `buildDestination` — clipboard payloads are buffered and DISCARDED on Close today. The grid clipboard adapter wiring is explicitly "follow-up" per the comment.
- The `buffered_row_warn_threshold` is shown as a label but the user can still confirm — no hard-block path exists for non-clipboard destinations, contrary to AD-17's intent.
- Progress callbacks are tick-based; no UI surface yet beyond toast updates (a dedicated progress popup is not implemented).
- `exporter.Run` ignores `progress != nil` for any tick interval ≠ 5000-rows-mod or 1s-elapsed; the first row never produces a progress tick.
- `RowSource.Iterate` cannot be paused; cancellation is only checked at row boundary.

## Theme / Colors

### Purpose
Global palette state held in `pkg/theme/` with a single `Apply(cfg)` swap point. Built-in `default_dark` theme initialised at `init()` and on first `Current()` call.

### User-observable behavior
- 41 colour fields (`ActiveBorder` … `PromptFg`) governing borders, selected rows, syntax highlight (numeric/string/keyword/comment/identifier/operator), backgrounds, popups, menus, table headers, gutters, cursor, search highlights, diffs, prompts.
- Loaded from `config.yml`'s `theme:` block; unknown colour strings are stored verbatim (rendering layer decides interpretation).
- `NO_COLOR` env var (any non-empty value) suppresses accent colors in renderers that opt in (e.g. EXPLAIN plan cost coloring). Resolved lazily on first `IsMonochrome()` call and cached for the process lifetime (per AD-11).

### Persisted state / file locations
- User overrides in `$XDG_CONFIG_HOME/dbsavvy/config.yml` under `theme:`.

### Gaps / TODOs / dead-looking code
- `parseStyle` accepts attribute tokens (`bold`, `underline`, `italic`, `on <bg>`) and populates `Style.Bg` / `Bold` / `Underline` / `Italic` (dbsavvy-56u.4); see `pkg/theme/theme.go:168`. Single-token "red"-style values still land as `Fg` for backward compat.
- `themeState` exposes 41 fields but consumers across the codebase only read a handful (the rest are dead-looking).
- `AppState.LastTheme` is persisted but no runtime theme-switcher exists; restart-only theme reload via `config.yml` edit.
- `IsMonochrome` is cached after first call — tests that mutate `NO_COLOR` mid-process after the first read will not see the change (documented behaviour).
- No light/high-contrast built-in theme — only `builtin/default_dark.go`.

## I18N

### Purpose
Embedded English translation set plus afero-backed overlay loader. Tries afero FS first, then embedded `translations/*.json` fallback.

### User-observable behavior
- All user-facing strings come from `*i18n.TranslationSet` (e.g. action descriptions, button labels, toasts).
- Overlay JSON oversize (>1 MiB) is rejected and English baseline used; malformed JSON is logged WARN and English baseline used.

### Persisted state / file locations
- Embedded: `pkg/i18n/translations/<lang>.json`. Only `en.json` is bundled.
- Overlay search path (when afero FS is supplied): `translations/<lang>.json` relative to the afero root.

### Locales bundled
- English only (`en.json`). No other locales shipped.

### Gaps / TODOs / dead-looking code
- `entry_point.Start` now calls `i18n.DetectLocale()` → `i18n.LoadAndMerge(nil, lang, log)` (dbsavvy-56u.3, `pkg/app/entry_point.go:99-102`). The afero-FS overlay path is reachable; production passes `nil` so only the embedded `en.json` baseline applies until a real FS source is wired.
- `pkg/i18n/testdata/i18n/` exists for tests only; no user-facing locale directory.

## Logging

### Purpose
slog-backed logger opened by `logs.Open` from `entry_point.Start`.

### User-observable behavior
- Errors from `OnWorker` panics, history recorder panics, session SSL warnings, etc. are appended to the log file.
- `BuildPgxConfig` writes the SSL-disable WARN directly to `os.Stderr` (NOT via the slog handler) — so it surfaces in the terminal before the TUI takes over.

### Persisted state / file locations
- Path: `$XDG_STATE_HOME/dbsavvy/dbsavvy.log` (or `$DBSAVVY_LOG_DIR` / `--log-dir` override).
- Mode: file 0600, parent dir 0700.
- Default level: DEBUG into the file, WARN+ also mirrored to stderr.
- Open flags: `O_APPEND|O_CREATE|O_WRONLY` (no rotation).

### Gaps / TODOs / dead-looking code
- `logs.Open` is called from `entry_point.Start` (`pkg/app/entry_point.go:179`); the older `logs.Init` reference in earlier review docs was stale (logrus was superseded by slog). On `logs.Open` failure without an override the process falls back to a stderr slog handler; with an override the process exits with the error.
- No log rotation, no multi-process safety (docstring acknowledges this; deferred to E12).
- No CLI flag to override log level; users would need an env var or rebuild.
- The stderr SSL warning bypasses the slog handler, so the file log never sees it.

## Config File

### Purpose
YAML user config at `$XDG_CONFIG_HOME/dbsavvy/config.yml` overlaying `GetDefaultConfig()`.

### User-observable behavior
- Created on first run with the default-config YAML body (mode 0600; parent dir 0700 created if missing).
- Idempotent first-creation via `O_CREATE|O_EXCL` — concurrent first-run callers race-safe.
- Existing parent dir is NOT chmoded back to 0700 (M10c).
- Sanitize pass strips control bytes from keybinding `Description`, `Tag`, `Key` after decode.

### Persisted state / file locations
- `$XDG_CONFIG_HOME/dbsavvy/config.yml`, mode 0600.

### Configurable fields
- `config_version int`, `leader`, `local_leader`, `timeout`, `timeout_len`, `ttimeout_len`, `whichkey_delay`.
- `theme.*` (41 fields, `Fg` only).
- `keybindings: [{mode, scope, key, action|command, description, tag, show_in_bar, opens_menu}]`.
- `ui.mouse.{enabled, double_click_ms}`.
- `ui.result_page_size`, `ui.result_prefetch_rows`, `ui.prefetch_threshold`, `ui.read_to_end_warn_threshold`, `ui.filter_max_regex_bytes`.
- `ui.export.{buffered_row_warn_threshold, clipboard_max_bytes}`.

### Gaps / TODOs / dead-looking code
- `ValidateUserConfig` is now invoked from `pkg/app/entry_point.go` AFTER `NewGui` and BEFORE `g.RunAndHandleError()` (dbsavvy-56u.3); on validation error the entry point emits to stderr, calls `g.Close()`, and exits 1. Build-time warnings still surface independently.
- `config_version` field exists but no migration code reads it.
- `KeybindingConfig.OpensMenu` and `ShowInBar` are parsed but unread.

## Tasks / Background Work

### Purpose
`ResultBufferManager` (a SQL-row analogue of lazygit's ViewBufferManager) owns the lifecycle of a single in-flight streaming query: launches via `OnWorker`, drains an initial fill, then switches to a chan-driven pull loop for `ReadRows`/`ReadToEnd`. The orchestrator's `OnUIThread` / `OnUIThreadContentOnly` / `OnWorker` provide the threading seams.

### User-observable behavior
- Busy-spinner bottom-right reflects `BusyCount() > 0` (atomic int64 incremented per `OnWorker` spawn, decremented on return).
- Query streams paint the first page synchronously then drip rows.
- Starting a second query with a different task key cancels the in-flight task (closes RowStream, waits for the worker to finish) before launching the new one. Same-key duplicate launches are silently no-op.
- OnWorker panics are recovered and logged via `Common.Log.Errorf`; the TUI keeps running.
- Shutdown waits for every in-flight `OnWorker` goroutine via `workersWG.Wait()` so goleak-based assertions can detect leaks.

### Persisted state / file locations
None directly. Indirect: streaming results may eventually be written via the export run pipeline.

### Gaps / TODOs / dead-looking code
- `BusyCount` is exposed but no production caller other than the status renderer; smoke tests are the primary consumer.
- `pkg/tasks/doc.go` has the placeholder body `// Package tasks ...` — the package's design isn't documented.
- `OnWorker` panic logging swallows the recovered error after logging; no toast or visible signal that a worker died.
- `goroutine_id_test.go` suggests there was a per-goroutine debug helper, but no production code calls it.

---

# Top-level GAPS list

Cross-cutting issues that need follow-up. Order is rough severity, not strict. See `GAPS.md` for the consolidated panel-aware list.

1. **AD-17 "hard-block above threshold" is informational only** — `buffered_row_warn_threshold` is just a label; no destination refuses above the threshold. Only `clipboard_max_bytes` is a real hard block.
2. **Clipboard export destination has no real clipboard writer wired** — `buildDestination` passes `nil`; payloads are buffered and discarded (tracked as epic `dbsavvy-8so`).
3. **`command:` shorthand is registered but dispatch is `NopSentinel`** — custom shell-style commands are placeholders for E11.
4. **Dead AppState fields** — `Version`, `LastTheme`, `StatementTimeoutOverride`, `LastSessionSettings` are persisted but unread.
5. **`KeybindingConfig.OpensMenu` and `ShowInBar` are unread** — declared, sanitised, never consumed.
6. **`pkg/tasks/doc.go` is a placeholder** — package design undocumented.
7. **`pkg/modes/` is a documented stub** with no production code.
8. **Mode bits `ModeVisualLine`, `ModeVisualBlock`, `ModeOperatorPending`, `ModeReplace` declared but unused** in production dispatch paths.
9. **History dropped-count is invisible** — `History.dropped` counter increments on channel overflow but never surfaces to the user.
10. **History sqlite path is non-configurable and never rotated**.
11. **`AppState.LastTheme` exists with no theme switcher** — palette is reload-only from `config.yml`.
12. **`DispatchResult.Swallowed`** has only the `<nop>` sentinel emit site; future-use placeholder.
13. **Stderr SSL warning bypasses the slog handler** — never lands in the log file.
14. **No CLI flag for log level** — needs rebuild or env var to change.
15. **`HistoryRecorder` panic-recovers but `noopHistoryRecorder` fallback is silent** — a history-open failure at startup leaves no breadcrumb in the UI.
