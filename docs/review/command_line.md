# Command Line (ex-line)

## Purpose
Vim-style `:` ex-command prompt; reads typed text from the underlying gocui `TextArea` and dispatches it through the `ExRegistry`.

## Visible content
- Single-row popup view named `COMMAND_LINE` at the bottom.
- The `:` prefix is pre-populated by the Layout Tier-3 popup pass on view creation.

## Trigger / how to open
- `:` in `ModeNormal` at scope `all` (any non-popup context + GLOBAL) — dispatches `commands.CommandOpen` which `Push`es `CommandLineContext`, enables the terminal caret, and sets `ModeStore[COMMAND_LINE] = ModeCommand`.

## Keybindings while focused
- printable runes / editing — routed through master gocui `Editor.Passthrough` → `gocui.DefaultEditor` → `v.TextArea`
- `<esc>` — `commands.CommandCancel`: pops context, disables caret
- `<cr>` — `commands.CommandSubmit`: reads `TextArea.GetContent()` (strips leading `:`), splits on whitespace, looks first token up in `ExRegistry`, dispatches handler with remaining tokens. Always pops afterward.

## Recognised `:` commands
Registered in `pkg/gui/orchestrator/gui.go`:
- `:q` — Quit (returns `gocui.ErrQuit`)
- `:quit` — Quit (alias)
- `:reload` — Reload user config; rebuilds the trie under panic-guard, swaps live `TrieSet`, toasts:
  - `config reloaded`
  - `config reloaded (N warning(s))`
  - `reload failed: ...`
  - `reload superseded` for queued duplicates
  - Extra args silently dropped

No `:w`, `:set`, `:e`, `:wq`, `:help`, etc.

## Mouse interactions
- None defined for the command line itself.

## Status / dismissal
- Unknown command → `Toaster("unknown ex-command: <name>")` then pop
- Handler error → toast `err.Error()` then pop (except `gocui.ErrQuit` which propagates)
- Empty/whitespace-only line → silent pop
- `HandleFocusLost` resets `ModeStore[COMMAND_LINE]`, clears buf, and drops the cached view pointer

## Gaps / TODOs / dead-looking code
- Very sparse ex-command set — only `:q`, `:quit`, `:reload`.
- No history, no completion, no tab-cycling.
- No `:w` to force-save the editor buffer (buffer auto-saves on focus loss but there's no manual flush).
