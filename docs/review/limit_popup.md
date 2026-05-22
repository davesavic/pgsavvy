# Limit Popup (terminal-too-small overlay)

## Purpose
**Not** a row-limit setter — this is the terminal-too-small overlay (DISPLAY_CONTEXT kind) shown when the window can't fit the layout.

## Trigger
Pushed by the layout pass when window dimensions fall below the minimum render size.

## Visible content / inputs
- Single-line message from `deps.LimitText()` (typically `Tr.TerminalTooSmall` → "Terminal too small. Please resize the window to continue.").

## Keybindings while focused
- None published. Auto-dismissed when the terminal is resized back above threshold.

## Multi-step / chaining
N/A.

## Persisted state
None.

## Gaps / TODOs / dead-looking code
- No explicit-dismiss affordance for the user. `<c-c>` is GLOBAL-scoped on `QuitController` so quit still works, but no toast/hint communicates that.
- **Misleading name** ("Limit") vs purpose ("terminal too small") — easy to confuse with a row-limit popup that doesn't exist. The actual page-size config lives in `UIConfig.ResultPageSize` (default 200) in YAML, not in any popup.
