# Selection Popup

## Purpose
Generic list-picker popup (driver picker, connection-add flow picker, etc.).

## Visible content
- Line 1: `state.Label()`.
- Then one line per choice prefixed `> ` (cursor) or `  `.
- Rendered only when `state.Active() == true`; nil-safe otherwise.
- State source: `ChoiceHelper` (passed via `SelectionContext.SetState`); helper holds label, choices, cursor.

## Trigger / how to open
- A caller invokes `ChoiceHelper.Choose(label, choices, onSubmit, onCancel)` (e.g. driver picker in the connection-add flow), which pushes the SELECTION context and lights up `Active()`.

## Keybindings while focused (Normal, scope `SELECTION`)
- `<up>` / `k` — `SelectionUp` (cursor -1; helper clamps to 0)
- `<down>` / `j` — `SelectionDown` (cursor +1; helper clamps to `len(choices)-1`)
- `<cr>` — `SelectionConfirm` (calls `helper.Submit(cursor)`)
- `<esc>` — `SelectionCancel` (calls `helper.Cancel()`)

## Mouse interactions
- No selection-specific mouse handler today.

## Status / dismissal
- `Submit(idx)` invokes the caller's `onSubmit(idx)` and pops the context.
- `Cancel()` invokes the caller's `onCancel()` and pops the context.

## Gaps / TODOs / dead-looking code
- No multi-select.
- No filter / search.
- No scroll / page navigation for long lists.
- No mouse click-to-select.
- `Submit` clamps invalid `idx` to error but no UX explains why.
