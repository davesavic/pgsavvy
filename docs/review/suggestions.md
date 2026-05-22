# Suggestions Popup

## Purpose
Intended autocomplete / suggestion popup attached to a prompt.

## Visible content
- Nothing rendered today.

## Trigger / how to open
- Registered as a `TEMPORARY_POPUP` context (view `SUGGESTIONS`), included in the layout's popup case list and in the keybinding-service scope list — but **no controller pushes it and no helper writes to it.**

## Keybindings while focused
- None defined.

## Mouse interactions
- None.

## Status / dismissal
- N/A.

## Gaps / TODOs / dead-looking code
- **Entire feature is unimplemented.** `suggestions_context.go` explicitly states: "Suggestion fetching and selection wiring land in later epics." No data sources, no fetch logic, no controller — just the empty type and its registry slot.
