# Result Tabs

## Purpose
Multi-tab streaming results panel (scope `RESULT_GRID`) hosting query results, EXPLAIN plans, and error displays in tabs.

## Visible state
- Tab strip with per-tab title (truncated SQL prefix, 40 chars, `…` suffix; pinned tabs marked).
- Active tab's grid (or Plan tree, or error body).
- Per-tab state suffix in title: `(queued)`, `(running …)`, `(N rows)`, `(cancelled, N rows)`, error message.
- Sort indicator appended to title, e.g. ` (sort: name ↑)`.

## Modes / states
- `StateQueued`, `StateRunning`, `StateDone`, `StateCancelled`, `StateError`.
- Read-to-end confirmation prompt when estimated rows exceed warn threshold (default 1 M).

## Keybindings (Normal only; scope `RESULT_GRID` unless noted)

### Tab management
- `<leader>1`..`<leader>9` — jump to tab N (GLOBAL scope; fires from any focused view)
- `gt` next tab, `gT` previous tab
- `<leader>X` close active tab
- `<leader>=` pin / unpin active tab (pinned survive `ui.result_tabs_max` eviction; default cap 8)
- `<leader>x` cancel active tab's stream

### Pagination / streaming
- `]p` next page, `[p` previous page
- `G` — expanded mode: jump to last record; grid mode: read-to-end with warn-threshold prompt
- `]G` — force read-to-end regardless of view mode

### In-grid /regex filter
- `/` — open filter prompt (once-per-tab caveat toast when tab incomplete)
- `<c-a>` — toggle filter all-columns vs cursor-column
- `n` next match, `N` previous match
- `<esc>` — clear active filter (shared chord; only fires when filter active)

### Sort
- `<leader>s` — open column-picker overlay; cycles asc → desc → clear on selection

### Hide columns
- `<leader>gH` — open hide-cols overlay (persistence gated on tab carrying row identity)

### Export
- `<leader>oe` — open the export menu for the active tab

### View toggle
- `<leader>gx` — flip grid ↔ expanded view (persisted globally via `AppState.LastResultViewMode`)

### Result-grid motions (viewMode-aware via helper)
- `j` / `k` cursor down / up
- `h` / `l` cursor left / right
- `gg` jump to first row
- `<c-d>` half-page down, `<c-u>` half-page up
- `J` / `K` wrapped-line down / up (mostly meaningful in expanded mode)
- `V` select row, `<c-v>` select block
- Rail-switch: digits `1`..`6` + `<tab>`

## Mouse interactions
- Header left-click + double-click (within `ui.mouse.double_click_ms`, default 400 ms) triggers `SetSort(col)`.
- Click on a different column resets the double-click anchor.
- Data-row clicks are no-ops (preserve row-select invariant).

## Persisted state
- `AppState.LastResultViewMode` (grid / expanded)
- `AppState.HiddenColumns[ResultIdentity]` — per-result-shape hidden column names
- Sort / filter / cursor — per-tab session state only
- Query history written to `$XDG_STATE_HOME/dbsavvy/history.sqlite` (FTS5 indexed, cap=128 channel, drop-oldest-and-warn overflow). **No UI to browse it yet.**

## Status / toasts / busy
- "no result tabs" toast.
- `(queued)` / `(running …)` / `(done — N rows)` / `(cancelled, N rows)` in tab title.
- NOTICE / WARNING counter toast (first occurrence of run + counter updates).
- Read-to-end confirmation popup with estimated-rows banner.
- Filter caveat toast on incomplete-stream filter apply.

## Gaps / TODOs / dead-looking code
- Tab title rename — NOT implemented (auto-derived from SQL prefix).
- Tab drag-reorder — NOT implemented.
- Search inside cell — NOT implemented (only /regex filter on rows).
- Cell-edit / inline mutation — NOT implemented (deferred to E10).
- FK navigation — NOT implemented.
- History UI — NOT implemented (data captured silently into `history.sqlite`).
