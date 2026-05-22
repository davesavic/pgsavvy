# Cheatsheet

## Purpose
Auto-generated, scrollable popup listing every binding in the current TrieSet, partitioned into the focused scope plus the GLOBAL pseudo-scope.

## Visible content
- Header: `tr.CheatsheetTitle` + `tr.CheatsheetLegend`.
- If trie has no bindings: `tr.CheatsheetEmpty` sentinel.
- Two scope banners:
  - `== <CheatsheetCurrentScopeTab>: <scope label> ==` (scope `"all"` rewritten to `tr.CheatsheetScopeAllLabel`)
  - `== <CheatsheetGlobalTab> ==`
- Within each banner: per-mode subheading `-- Normal --`, `-- Insert --`, `-- Visual --`, `-- Visual-Line --`, `-- Visual-Block --`, `-- Operator --`, `-- Command --`, `-- Replace --`.
- Within each mode: section grouping by `Action.Tag` (empty Tag LAST), tag printed as `[tag]`; rows: `  <glyph> <key>  <description>`.
- Source glyphs: `·` ShippedDefault, `✱` UserOverride (U+2731), `★` CustomCmd.
- Key labels reverse-map leader / localleader runes back to `<leader>` / `<localleader>`.

## Filtering / grouping
- `cheatsheet.Generate` keeps ONLY leaves whose scope equals the focused scope OR `GLOBAL`.
- Empty modes filtered out (no banner painted for modes with zero leaves — D13 vacuous-truth avoidance).
- Within a mode, rows grouped by Tag → sorted by Tag (empty last), then by Key within section.

## Trigger / how to open
- `?` key bound to `commands.HelpCheatsheet`: captures the current focus tree top's `GetKey()` as scope, calls `Cheatsheet.SetScope(scope)`, pushes the context.

## Keybindings while focused
- `<esc>` — bound directly via `driver.SetKeybinding(string(types.CHEATSHEET), KeyEsc, ...)` to `tree.Pop()` (DISPLAY_CONTEXT bypasses Matcher)
- No other bindings; `AddKeybindingsFn` is intentionally a no-op

## Mouse interactions
- None.

## Status / dismissal
- `<esc>` pops; focus returns to whatever was beneath on the focus stack.

## Gaps / TODOs / dead-looking code
- `?` opens the cheatsheet popup: the `HelpCheatsheet` handler is registered in `QuitController.RegisterActions` (`pkg/gui/controllers/quit_controller.go:56`, dbsavvy-56u.2) and captures the focused scope before `Cheatsheet.SetScope` + push.
- No scroll keybindings — body is rendered in full with no pagination or keyboard scroll.
- No search / filter within the popup.
- Cosmetic: legend `✱` may render differently from row glyph due to font fallback.
