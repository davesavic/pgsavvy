# Indexes rail

## Purpose
Lists indexes of the currently focused table.

## Visible state / columns shown
- One row per index, prefixed by `> ` / `  `.
- Row text: `models.Index.Name` only — no uniqueness, columns, method, predicate.
- Title bar: `Indexes`.

## Keybindings (normal mode)
- `j` / `k` — cursor down / up
- `<cr>` — explicit no-op
- `r` — RailRefresh (re-runs the indexes load for the focused table)
- `1` / `2` / `3` / `4` / `5` / `6` / `<tab>` — rail switch

## Mouse / focus interactions
- Left click pushes INDEXES onto the focus stack.
- Wheel events cancel armed chords; no scroll.

## Persisted state
- None.

## Filtering / search / sort
- None.

## Status messages / toasts / busy indicators
- None.

## Gaps / TODOs / dead-looking code
- Populate path wired at `populateIndexesRail` (`pkg/gui/orchestrator/adapters.go:283`); driven alongside COLUMNS from the composite `OnTableActivate` worker (`pkg/gui/orchestrator/gui.go:955-973`, dbsavvy-56u.1).
- Row formatter shows only the name — no UNIQUE / column list / method / predicate.
- Picker is `any` with a nil picker.
- `RegisterActions` is a no-op.
- No detail / DDL-view / drop-index keybindings.
