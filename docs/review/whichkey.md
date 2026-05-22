# Which-Key Popup

## Purpose
Mid-chord discovery popup showing the immediate children of the in-progress chord prefix.

## Visible content
- One row per child binding: `<key><pad>  <label>`, key column right-padded to widest key.
- Each row truncated to `whichKeyMaxRowWidth = 38` cols.
- Body padded with blank lines to span `whichKeyBodyRows = 10` rows (bleed-through fix).
- Hidden when prefix has no children, when notifier is `Hide`d, or before `WhichKeyDelay` elapses.

## Trigger / how to open
- Automatic via the Matcher: after a partial chord match the Matcher calls `whichkey.ShowAfter(WhichKeyDelay, scope, prefix)`.
- Default `WhichKeyDelay = 300 ms` (`config.GetDefaultConfig`); user-configurable in YAML (`whichkey_delay`).
- Resolver `whichKeyRows(scope, prefix)` queries `Trie.ChildrenAt(prefix)` against the current `(mode, scope)` trie each render.

## Keybindings while focused
- No input bindings — popup is `DISPLAY_CONTEXT` and `AddKeybindingsFn` is a no-op. The user types the next chord key against the underlying scope; the Matcher resolves it.

## Mouse interactions
- None.

## Status / dismissal
- Hidden by `matcher.whichkey.Hide()` on: full match, no-match, `<esc>` cancel of partial, focus swap (`tree.RegisterSwapHook(g.whichkey.Hide)`), or any chord completion path that exits the partial state.

## Gaps / TODOs / dead-looking code
- `whichKeyBodyRows` is hardcoded to `whichKeyMaxRows - 2`; comment warns to keep in lock-step with `layout.go`.
