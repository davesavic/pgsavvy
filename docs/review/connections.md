# Connections rail

## Purpose
Lists configured connection profiles from disk so the user can pick one to open.

## Visible state / columns shown
- One row per profile, prefixed by `> ` (cursor) or `  ` (other rows).
- Per-row decoration: optional connection `Icon` + space, followed by the profile `Name` (Name, not Label — chosen so duplicate Labels stay distinguishable).
- Empty-state hint when zero profiles exist: `No connections yet.\nPress a to add`.
- Title bar: `Connections`.
- Frame color: active (`theme.ActiveBorder`) when focused, inactive otherwise.

## Keybindings (normal mode)
- `j` — move cursor down
- `k` — move cursor up
- `<cr>` — connect to selected profile; on error, swallows error and surfaces a 4 s toast (e.g. "already connected" rewritten from the sentinel)
- `a` — open the chained add-connection prompt flow (driver → name → DSN)
- `r` — RailRefresh (re-load profiles from disk)
- `1` / `2` / `3` / `4` / `5` / `6` — jump focus to Schemas / Tables / Columns / Indexes / QueryEditor / active Result tab
- `<tab>` — cycle forward through rails

## Mouse / focus interactions
- Left click pushes CONNECTIONS onto the focus stack.
- Wheel up / down / shift-wheel — registered handlers cancel any armed chord; no actual scroll wired yet.
- Active rail gets the themed active border color.

## Persisted state
- Reads profiles from `connections.yml` via `Deps.ConnectionsProvider()`; the rail itself does not persist anything.
- `AppState.LastConnectionID` is written after a successful Connect by `connectInvoker.Connect` (alongside `RecentConnectionIDs` LIFO/dedupe, cap=10) via `MutateAndSave` AFTER `wireQueryRuntime` succeeds (dbsavvy-56u.1).
- On boot, `restoreConnectionsCursor()` (`pkg/gui/orchestrator/gui.go:979`) seats the rail cursor on the profile matching `AppState.LastConnectionID`.
- `AppState.LastBufferUUIDs` is touched on Connect (query-editor buffer hydration), not by the rail itself.

## Filtering / search / sort
- None. Profiles render in provider's slice order.

## Status messages / toasts / busy indicators
- Toast on Connect failure (4 s), with sanitized message; "already connected" rewritten to a short phrase.
- No "Connecting…" or "Loading…" indicator; Connect runs synchronously in the handler (no busy spinner on this rail).
- Empty-state hint replaces row body when zero profiles.

## Gaps / TODOs / dead-looking code
- No delete / edit / disconnect / duplicate-profile bindings; only add (`a`) is wired.
- Wheel scroll handlers are stubs (cancel-arm only).
- `EmptyStateHook` is invoked with a `nil *common.Common` argument (deferred polish).
