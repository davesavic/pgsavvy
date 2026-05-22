# Menu Popup

## Purpose
Generic action-list popup used by helpers (e.g. future command palette) to present a vertical list of selectable entries.

## Trigger
`MenuPushHelper.PushMenu(...)` from any helper / controller. **No shipped global keybind opens it directly** — callers push it programmatically.

## Visible content / inputs
- Rendered body is populated by `MenuPushHelper`; `MenuContext` itself is a lifecycle skeleton with no built-in rendering — the popup helper owns the list view content.

## Keybindings while focused
- `<cr>` — `menu.confirm` (selection plumbing owned by `MenuPushHelper`; controller `Select` is currently a no-op shim)
- `<esc>` — `menu.cancel` (calls `helpers.Menu.PopMenu()`)

## Multi-step / chaining
N/A.

## Persisted state
None — popup is purely transient.

## Gaps / TODOs / dead-looking code
- `MenuController.Select` is an empty stub; selection actually flows through `MenuPushHelper` (godoc admits "popup state lives in T7b"). The `<cr>` binding therefore has no observable side-effect from the controller — relies entirely on the helper's keystroke route.
- `MenuContext` carries no list-cursor, item slice, or render hook — comments admit it ships "lifecycle skeleton only."
- No j/k cursor bindings registered at the controller level (helper-internal if any).
