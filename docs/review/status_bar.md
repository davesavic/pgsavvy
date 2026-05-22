# Status Bar (with Options Bar)

## Purpose
Always-on bottom status line: mode banner, active connection header, read-only tag, options-bar hints, and transient toasts.

## Visible content
- Layout: `<modeLabel> | <spinner> | <icon> <label> | [RO] <option1> <option2> ... | <OptionsBarMore>` (joined by ` | `). The `<spinner>` slot is omitted when `BusyCount <= 0` and otherwise renders a braille-dot frame indexed by `BusyCount` (dbsavvy-56u.4, `pkg/gui/status/status_bar.go:58`).
- Mode banner from `status.LabelForMode` (`-- NORMAL --`, `-- INSERT --`, `-- VISUAL --`, `-- V-LINE --`, `-- V-BLOCK --`, `-- O-PENDING --`, `-- COMMAND --`, `-- REPLACE --`); `ModeNormal` is shown explicitly (`forceShowNormal=true`).
- Connection header (`presentation.HeaderTextFor`) tinted via ANSI 8-color SGR from `conn.Color` (black/red/green/yellow/blue/magenta/cyan/white); hex/unknown collapses to bare text.
- `[RO]` segment when `activeConn.ReadOnly == true`.
- Up to 8 sorted option hints (`description: key`, suffixed with ` (disabled)` when statically disabled).
- Trailing `?: more` hint always appended.

## Toast multiplexing
- When `ToastSource.Current() != ""` the entire status line is replaced for the TTL window.
- Toast colors: ANSI green (`ToastInfo`) or red (`ToastError`).
- Default toast TTL: 3 s (command-line toaster); `defaultNoticeToastTTL = 4 s`.
- Toast level classification is heuristic on substrings (`fail`, `error`, `panic`, `unknown`).

## Trigger / how to open
- Automatic; rendered every frame against view `AppStatusViewName = "status"`.

## Keybindings while focused
- None — non-focusable chrome.

## Mouse interactions
- None on the status bar itself.

## Status / dismissal
- Toast auto-clears via `ToastHelper`'s `AfterFunc`; on expiry the next frame reverts to the default line.

---

## Options Bar (mid-section of status line)

### Purpose
Derives the per-frame list of action hints rendered into the status line's middle section from the focused context's chord trie.

### Visible content
- Each entry formatted as `"<description>: <keySequence>"`, e.g. `Confirm: <cr>`.
- `(disabled)` suffix when `leaf.Action.Disabled(commands.ExecCtx{Mode: mode, Scope: scope})` returns true at frame-build time. `ExecCtx` is populated with the frame's mode + focused scope (dbsavvy-56u.5, `pkg/gui/orchestrator/options_bar.go`); dynamic predicates that key off mode/scope now resolve correctly. `Count` / `Register` remain zero.
- Entries gathered from `(mode, focusedScope)` trie PLUS `(mode, GLOBAL)`, skipping double-collect when focused scope IS `GLOBAL`.
- Only leaves with `ShowInBar == true` AND non-nil `Action` included.
- Sort: by `Action.Tag` then by sequence-string label (stable).
- Cap: `optionsBarMax = 8`; overflow silently truncated (the trailing `?: more` hint signals the cheatsheet has the rest).

### Trigger
Automatic — `RenderStatusLine` calls `CollectOptionsForScope(trieSet, mode, scopeKey, tr)` each frame.

## Gaps / TODOs / dead-looking code
- `tr` parameter to `CollectOptionsForScope` is currently unused (reserved for future i18n separators).
- Disabled-probe `ExecCtx` still carries zero `Count` / `Register`; predicates that key off either remain enabled until real dispatch.
