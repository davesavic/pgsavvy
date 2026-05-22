# Tables rail

## Purpose
Lists tables in the activated schema; row activation is a placeholder for a future inline table-data editor (DESIGN.md §17).

## Visible state / columns shown
- One row per table, prefixed by `> ` / `  `.
- Row text: `models.Table.Name` only — no schema prefix, type, or row count.
- Title bar: `Tables`.

## Keybindings (normal mode)
- `j` / `k` — cursor down / up
- `<cr>` — `DoubleClickStub`: shows the toast "Table data editing is not yet available." (3 s); table arg ignored
- `1` / `2` / `3` / `4` / `5` / `6` / `<tab>` — rail switch

## Mouse / focus interactions
- Left click pushes TABLES onto the focus stack.
- Double-click invokes the same `DoubleClickStub` on the cursor table.
- Wheel events cancel armed chords; no scroll.

## Persisted state
- None. Items populated via orchestrator's `populateTablesRail` worker on schema `<cr>`.

## Filtering / search / sort
- None. Driver order.

## Status messages / toasts / busy indicators
- 3 s deferred-editor toast on activation.
- LoadTables errors logged and silently swallowed.
- No "Loading…" indicator.

## Gaps / TODOs / dead-looking code
- Activation toast is a permanent placeholder for the deferred inline table editor.
- `<cr>` does NOT populate COLUMNS or INDEXES rails — those rails have no populate path.
- `RefreshHelper.RefreshTables` exists but no keybinding triggers it.
- No delete / truncate / `\d`-style detail.
- No filter / search / sort for large schemas.
- `RegisterActions` is an empty body kept "for symmetry".
