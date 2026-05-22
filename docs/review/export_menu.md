# Export Menu

## Purpose
Popup (scope `EXPORT_MENU`) opened by `<leader>oe` for picking Format / Destination / Scope and starting an export of the active tab.

## Visible state
- Header `Export result`.
- Three selector rows: `Format`, `Destination`, `Scope`. Active field marked `> `; others `  `.
- Hint footer: `(↑/↓ field, ←/→ value, <cr> export, <esc> cancel)`.
- Annotations:
  - `(disabled — result is not a single base table)` on SQL INSERTs row when not eligible.
  - `WARNING: buffered format on ≥ N rows — Confirm disabled.` when bufferedThresholdExceeded.
  - `Full scope ignores your filter — press y to confirm (or change Scope).` when filter+full-scope conflict.

## Modes / states
- Normal-mode dispatch; field cursor + per-field value index.
- Hard-block flags: `bufferedThresholdExceeded`, filter+full-scope-requires-typed-YES, SQL-INSERTs-disabled.

## Keybindings (Normal only, scope `EXPORT_MENU`)
- `j` / `<down>` — next field
- `k` / `<up>` — previous field
- `l` / `<right>` — next value of active field
- `h` / `<left>` — previous value (Format cursor skips disabled SQL-INSERTs row)
- `<cr>` — confirm + execute export (blocked when `ConfirmBlockedReason() != ""`)
- `<esc>` / `q` — cancel and close
- `y` — typed-YES confirmation for Full-scope-with-active-filter warning

## Formats (in render order)
- CSV (streaming, `.csv`)
- TSV (streaming, `.tsv`)
- NDJSON (streaming, `.ndjson`)
- JSON Array (buffered, `.json`)
- Markdown (buffered, `.md`)
- SQL INSERTs (streaming, `.sql`) — only when `ResultIdentity.HasRowIdentity` (single base table); otherwise hidden / disabled

## Destinations
- File (writes to `.partial` then atomic rename on Footer success; auto-aborts on cancel; mode `0o600`)
- Clipboard (in-memory buffer, pushed to ClipboardWriter on Close; default cap `defaultExportClipboardMaxBytes = 16 MiB`)
- stdout (`os.Stdout` wrapped in nopCloser; Close is a no-op)

## Scopes
- Visible — current viewport's `[rowOffset, rowOffset+viewHeight)`
- Loaded — all buffered rows with active filter + sort projection applied
- Full — every server row from a fresh stream; **ignores active filter**, arrival order

## Size / threshold gates
- `ExportBufferedRowWarnThreshold` (default 100 000 rows; configurable via `cfg.UI.Export.BufferedRowWarnThreshold`)
- When estimated rows exceed threshold AND format is buffered (Markdown / JSON Array): Confirm is HARD-BLOCKED with "buffered format over threshold — pick a streaming format". User must switch to a streaming format.
- Clipboard destination capped at `defaultExportClipboardMaxBytes`.
- Full scope while filter active: requires typed `y` confirmation.

## Run orchestration
- `exporter.Run(ctx, Format, Destination, RowSource, progress)` drives Header → Iterate(Row*) → Footer.
- Progress callback fires every 5 000 rows or 1 second (whichever first), plus a final tick on success.
- Cancellation: `ctx.Done()` interrupts at next row boundary; file dest removes `.partial`, clipboard discards buffer, stdout returns.
- On success, descriptor (filename / "clipboard" / "stdout") returned for toast.

## Persisted state
- None (menu state per-invocation).

## Status / toasts / busy
- Progress ticks during long-running exports.
- Final toast with descriptor on success.
- Warning footer in menu body for buffered-threshold / filter+full-scope conflicts.

## Gaps / TODOs / dead-looking code
- No format-specific options exposed (CSV delimiter, JSON pretty-print, SQL INSERTs batch size, NULL representation).
- No filename customisation in menu (auto-generated via `exporter/filename.go`).
- No "open file after export" affordance.
- No export-history / re-run-last-export.
- Clipboard write failure on size-cap excess — defensive abort but no explicit user-facing toast wired.
- **AD-17 mismatch**: bd memory states "hard-block above threshold"; reality is the menu enforces the block only for buffered formats — see `cross_cutting.md` GAPS for the buffered-threshold informational vs. blocking discrepancy.
- Clipboard destination half-wired: `buildDestination` passes `ClipboardWriter == nil`; payloads are buffered to memory and discarded on Close. The grid clipboard adapter wiring is explicitly deferred.
