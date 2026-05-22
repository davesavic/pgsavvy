# Grid + Expanded View

## Purpose
Cell-render engine for tabular query results (`grid.View`) handling auto-sizing, scrolling, selection, yank, filter, sort, hide, expanded-mode, and ANSI sanitization.

## Visible state
- Header row (column names + type styling)
- Data rows with cell content
- Active-cell highlight + selection range styling (`SelectedRowBg`)
- Frozen first column indicator when toggled
- Sort arrow in title
- Empty-result indicator when `len(cols) == 0`

## Modes / states
- Selection: `SelectionNone` / row / cell / block
- View: `ViewModeGrid` (default) / `ViewModeExpanded`
- Auto-size sampling: first `AutoSizeSampleRowCount` rows seed column widths, then frozen
- Filter active / inactive (regex; all-cols vs cursor-col toggle)
- Sort state (col + dir + active flag)
- Hidden-col set (per-View, cleared on `SetColumns`)

## Cell rendering / sanitization
- Each value passes through `renderCellPlain` + `SanitizeCellEscapes` to neutralise server-side ANSI / control bytes.
- NULLs render dim / italic.
- JSON / BLOB cells render per `presentation` package conventions.
- SGR sequences from data are stripped to prevent grid corruption (fix `dbsavvy-u9v` for SGR-collision-with-digit cell content).

## Column widths
- `MinColumnWidth` / `MaxColumnWidth` bounds.
- Sized lazily from header + first N rows (auto-size sample), then frozen.
- Per-column width can be overridden via config defaults.

## Hidden columns
- `hiddenColSet map[int]bool` — indices into current `cols`.
- Cleared on every `SetColumns` (indices not stable across schema attaches).
- Persisted under `AppState.HiddenColumns[ResultIdentity]` as column NAMES; re-seeded on tab open.
- Excluded from `visibleColumnOrder` (and from expanded-mode column walk + yank).

## Horizontal / vertical scroll
- `rowOffset` / `colOffset` clamped each Render to keep cursor in viewport.
- `clampOffsetsLocked` re-computes window before each draw.
- Auto-prefetch: when cursor enters `PrefetchThreshold` of loaded tail, fires `onNearTail(ResultPrefetchRows)` once per growth past the last-fire marker.
- `viewHeight` tracked from last Render for `VisibleRows` (export Scope=Visible source).

## Current cell / value preview
- `cursorRow` + `cursorCol` in raw-buffer index space (not projected — filter-projected cursor is a documented gap).
- Cell under cursor highlighted; selection range painted.

## Yank (clipboard)
- `Yank()` serialises selection (or cursor cell) as TSV (tab-cols, newline-rows).
- Expanded-mode yank: per-record `col\tvalue\n` blocks separated by blank lines.
- `ClipboardWriter` wired via `SetClipboard` (production: OSC-52 / xclip / pbcopy; default no-op — **see gaps**).
- Hidden cols skipped in expanded-mode yank.

## Filter projection
- Regex-based; `allCols` toggle switches cursor-col vs union-of-all-cols.
- Captured under same RLock as render snapshot (no tearing).
- `JumpNextMatch` / `JumpPrevMatch` advance cursor.

---

## Expanded Row View (psql `\x` style)

### Toggle
`<leader>gx` from `RESULT_GRID` scope.

### Visible content
- `-[ RECORD n of ~total ]----` separator banner per record.
- Per-row: `col_name | value` lines, wrapped at value column.
- Continuation lines: `<gutter spaces> | rest_of_value`.
- Gutter sized to `max(len(col_name))` clamped to `[12, 32]`.
- `~total` from optimiser row-count estimate when known; otherwise projected count.
- `[ no records ]` banner on empty body.

### Overscan
Formats only `[cursor-1, cursor+2]` records per Render (must not format every record on a 1M-row result).

### Expanded-mode keybindings (inherits result-tab bindings)
- `j` / `k` next / previous record
- `J` / `K` wrapped-line down / up within active record
- `gg` first record
- `G` last loaded record (NOT read-to-end; `]G` is the explicit force)
- `h` / `l` no-op (no horizontal scroll — every column is shown)
- `y` yank active record (or selected range) as `col\tvalue\n` block

### Persisted state
- `AppState.LastResultViewMode` (global session preference).

## Gaps / TODOs / dead-looking code
- Filter-projected cursor — uses raw-buffer index; cursor may "vanish" outside projected rows until JumpNext lands it.
- Block-mode yank with hidden cols — naive (selects raw range; doesn't filter through hidden set).
- Inline cell editing — NOT implemented.
- Cell preview popup — NOT implemented (expanded mode IS the per-cell view).
- Sort across loaded vs full result set — sorts loaded buffer only.
- Expanded mode: no horizontal scroll on long values (wraps to next line only).
- ClipboardWriter default is no-op; grid clipboard adapter wiring is explicitly deferred (see `cross_cutting.md`).
- Persistence of expanded mode is global, not per-connection or per-tab.
