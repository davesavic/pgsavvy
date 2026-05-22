# Messages Panel

## Purpose
EXTRAS-slot panel for streamed PG NOTICE / WARNING / INFO lines from running queries (and per DESIGN.md §12.5.7 future commit-edits audit / DDL `output:log` routing).

## Visible content
- Plain newline-terminated lines appended via `DefaultMessagesSink.Append(line)`, written to view `MESSAGES` through `driver.Write` scheduled on `OnUIThreadContentOnly`.
- Source: `NoticeHelper` routes server notices from `RunHandle` streams; severity prefixes come from `tr`.

## Trigger / how to open
- Always present as an EXTRAS-slot tiled view (`Kind: EXTRAS_CONTEXT`, `Title: "Messages"`). Sink writes happen as queries emit notices.

## Keybindings while focused
- Listed in the keybinding-service scope list but `MessagesContext` itself does NOT install controller bindings — the type has no `AddKeybindingsFn` overrides and no `HandleRender` beyond `BaseContext`.

## Mouse interactions
- None defined.

## Status / dismissal
- No dismissal — tiled chrome.

## Gaps / TODOs / dead-looking code
- `MessagesContext` is a near-empty stub: embeds `BaseContext`, stores `deps`. No `HandleRender`, no per-line formatting, no clear/scroll commands. Writes go directly to the view buffer via the sink, bypassing the context.
- No keybindings for clearing, copying, or scrolling messages.
