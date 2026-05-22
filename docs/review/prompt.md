# Prompt + Chained Prompt

## Purpose
Single-line text-input popup; underpins both standalone prompts and the multi-step add-connection flow (driver → name → DSN) via a chained-prompter adapter.

## Trigger
- Single prompt: `PromptHelper.Prompt(label, initial, onSubmit, onCancel)`
- Choice prompt: `ChoiceHelper.Choose(label, choices, onSubmit, onCancel)` (renders into the SELECTION popup)
- Chained: `chainedPrompterAdapter.PromptString` / `PromptChoice` driven by `ConnectionFormHelper.WalkAddConnection` (`a` on the connections rail)

## Visible content / inputs
- Word-wrapped label (multi-line, wraps at view InnerWidth).
- Blank separator line.
- `> ` prefix + typed buffer line.
- The buffer is owned by the `PROMPT` view's `gocui.TextArea`; printable runes / Backspace / Delete / arrow keys / Home / End / bracketed paste flow through `gocui.DefaultEditor`.

## Keybindings while focused
- `<cr>` — `prompt.submit` (reads-and-clears TextArea, hands value to `PromptHelper.Submit`)
- `<esc>` — `prompt.cancel` (clears TextArea, calls `PromptHelper.Cancel`)
- All printable runes / `<bs>` / `<del>` / `<left>` / `<right>` / `<home>` / `<end>` / bracketed paste — through `gocui.DefaultEditor` (NOT chord-trie bindings)

## Multi-step / chaining (add-connection flow `WalkAddConnection`)
1. **driver** — `PromptChoice` over `drivers.Names()`; re-prompts if pick is out-of-set
2. **name** — `PromptString` with validator: non-empty + not duplicate vs `LoadConnections`. Surfaces `Tr.DuplicateConnectionName`
3. **DSN** — `PromptString` with validator: non-empty + `url.Parse`-able + no inline userinfo password (G3-G(ii); surfaces `Tr.DSNInlinePassword`)
- Validation errors re-push the same popup with the raw (untrimmed) input preserved as `initial`; label embeds the validator error on its own line for clean wrapping.
- `<esc>` at ANY step discards collected values and returns `nil` (no error, no write).
- Adapter blocks the caller goroutine over a buffered chan; ctx cancellation schedules `helper.Cancel` guarded by `Active()`.

## Persisted state
- On success the new `models.Connection` is written via `config.AppendConnection` (YAML profiles file).
- No partial state persisted between steps — cancellation discards.

## Gaps / TODOs / dead-looking code
- Chained prompter is mutex-guarded (dbsavvy-56u.5): overlapping `PromptString` / `PromptChoice` calls return `("", ErrPromptBusy)` immediately and leave the in-flight prompt's callbacks intact.
- `PromptController.buf` is "test-only seam" — real path uses `v.TextArea`. Dead-ish in production but kept for test compatibility.
- `PromptController.Buffer()` peek path falls back to `p.buf` when reader lacks a `Buffer()` method — production reader does expose it, so the fallback is test-only.
