# Columns rail

## Purpose
Lists columns of the currently focused table.

## Visible state / columns shown
- One row per column, prefixed by `> ` / `  `.
- Row text: `models.Column.Name` only — no type, nullability, default, PK marker.
- Title bar: `Columns`.

## Keybindings (normal mode)
- `j` / `k` — cursor down / up
- `<cr>` — explicit no-op (controller passes `func(_) error { return nil }`)
- `r` — RailRefresh (re-runs the columns load for the focused table)
- `1` / `2` / `3` / `4` / `5` / `6` / `<tab>` — rail switch

## Mouse / focus interactions
- Left click pushes COLUMNS onto the focus stack.
- Wheel events cancel armed chords; no scroll.

## Persisted state
- None.

## Filtering / search / sort
- None.

## Status messages / toasts / busy indicators
- None.

## Gaps / TODOs / dead-looking code
- Populate path is wired at `populateColumnsRail` (`pkg/gui/orchestrator/adapters.go:252`), driven from the composite `OnTableActivate` worker (`pkg/gui/orchestrator/gui.go:955`). The "no populate path" claim in earlier docs was a documentation artifact — code was always wired.
- Row formatter shows only the name — type/nullability/PK/default all missing.
- Picker is `any` with a nil picker; cannot resolve a domain entity even if wired later without changing the generic param.
- `RegisterActions` is a no-op.
- No keybindings for column detail, copy-name, or jump-to-DDL.
