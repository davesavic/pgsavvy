# Schemas rail

## Purpose
Lists schemas in the active connection so the user can pick one to drive the TABLES rail.

## Visible state / columns shown
- One row per schema, prefixed by `> ` / `  `.
- Row text: `models.Schema.Name`.
- Title bar: `Schemas`.

## Keybindings (normal mode)
- `j` / `k` — cursor down / up
- `<cr>` — fires `OnSchemaActivate(name)`; orchestrator dispatches `populateTablesRail` on a worker goroutine
- `H` — hide the schema under the cursor; persists to `AppState.HiddenSchemas[connID]`
- `U` — unhide the schema under the cursor; if anchored, shows a confirmation popup before applying
- `<leader>H` — toggles `showHiddenMode` (in-memory only); `renderRows` consults the flag so runtime-hidden schemas become visible while the toggle is on
- `r` — RailRefresh (re-runs schemas load for active connection)
- `1` / `2` / `3` / `4` / `5` / `6` / `<tab>` — rail switch

## Mouse / focus interactions
- Left click pushes SCHEMAS onto the focus stack.
- Wheel events cancel armed chords; no scroll yet.

## Persisted state
- `AppState.HiddenSchemas[connID]` (debounced save via `AppStateStore`).
- Profile-level + built-in hidden patterns applied at populate time.
- `showHiddenMode` is in-memory (`atomic.Bool`); not persisted.

## Filtering / search / sort
- Built-in + profile-level patterns filter at populate.
- Runtime `AppState.HiddenSchemas` are NOT applied at populate; `H`/`U` toggle handles them.
- No interactive search/sort.

## Status messages / toasts / busy indicators
- Confirmation popup on `U` for an "anchored" unhide.
- LoadSchemas error after Connect is logged and silently swallowed.
- No "Loading…" indicator.

## Gaps / TODOs / dead-looking code
- `<leader>H` is now visible: `renderRows` consults `showHiddenMode` (`pkg/gui/context/schemas_context.go:67`, dbsavvy-56u.4).
- LoadSchemas errors swallowed silently (no toast).
- `H`/`U` updates `AppState` but does NOT re-populate the rail — refresh only on next Connect or via `r`.
- No cursor restore for last-selected schema.
