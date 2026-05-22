# Confirmation Popup

## Purpose
Yes / No confirmation overlay (TEMPORARY_POPUP) used for destructive actions; styled with the active connection's accent color via `PresentationHook`.

## Trigger
`ConfirmHelper.Confirm(title, body, onYes, onNo)` from any controller; orchestrator pushes the `CONFIRMATION` context.

## Visible content / inputs
- Title + body strings supplied by caller.
- Border style + header come from `deps.PresentationHook(activeConnection)` (nil hook → default style).
- No text input field — pure dismissal popup.

## Keybindings while focused
- `ConfirmationController` (`pkg/gui/controllers/confirmation_controller.go`) publishes hardcoded defaults under `CONFIRMATION` scope (dbsavvy-56u.2):
  - `y` / `<cr>` → `ConfirmYes` (dispatches `ConfirmHelper.Yes()`)
  - `n` / `<esc>` → `ConfirmNo` (dispatches `ConfirmHelper.No()`)
- Bindings are fixed defaults, not user-overridable via `config.yml`.

## Multi-step / chaining
N/A.

## Persisted state
None.

## Gaps / TODOs / dead-looking code
- `Presentation()` returns zero `TextStyle` when `PresentationHook` is absent — silent fallback to default, no warning.
