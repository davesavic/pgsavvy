# dbsavvy — Functionality Inventory (docs/review)

Per-panel audit of the TUI's current implementation as of 2026-05-20. Each file lists user-observable behavior, keybindings, persisted state, and **flagged gaps** (half-wired, stubbed, or planned-but-not-shipped features).

Use this directory to:
1. Drive manual QA — every feature listed below should have at least one QA test.
2. Identify holes from prior epic deliveries before scoping new work.

## Side rails (left column)

- [connections.md](connections.md) — connection profile picker
- [schemas.md](schemas.md) — schema list, hide/unhide
- [tables.md](tables.md) — table list (activation is a stub today)
- [columns.md](columns.md) — column list (populated on table activation)
- [indexes.md](indexes.md) — index list (populated on table activation)

## Main work area

- [query_editor.md](query_editor.md) — vim-style SQL editor; motions, operators, registers, run/EXPLAIN
- [result_tabs.md](result_tabs.md) — multi-tab streaming results, pin/close/cancel, sort/filter, export
- [grid_and_expanded.md](grid_and_expanded.md) — cell render, scroll, yank, filter, hidden cols, expanded view
- [plan_view.md](plan_view.md) — EXPLAIN/ANALYZE tree renderer with cost-percentile gradient
- [export_menu.md](export_menu.md) — format × destination × scope picker; Run orchestrator

## Chrome / feedback

- [status_bar.md](status_bar.md) — mode banner, conn header, options-bar hints, toasts
- [command_line.md](command_line.md) — `:` ex prompt (only `:q`, `:quit`, `:reload` shipped)
- [cheatsheet.md](cheatsheet.md) — auto-generated keybinding reference
- [whichkey.md](whichkey.md) — mid-chord discovery popup
- [messages.md](messages.md) — server NOTICE/WARNING panel
- [suggestions.md](suggestions.md) — **unimplemented stub**
- [selection.md](selection.md) — generic list picker (driver picker etc.)

## Popups / overlays

- [menu_popup.md](menu_popup.md) — generic action menu (lifecycle skeleton only)
- [confirmation_popup.md](confirmation_popup.md) — yes/no overlay
- [prompt.md](prompt.md) — single-line + chained prompts (add-connection flow)
- [limit_popup.md](limit_popup.md) — terminal-too-small overlay (NOT a row-limit setter)
- [hide_overlay.md](hide_overlay.md) — column-visibility toggle for result grid

## Cross-cutting / always-on

- [global_and_first_run.md](global_and_first_run.md) — global keybinds, focus cycling, first-run tip, expanded-mode toggle
- [cross_cutting.md](cross_cutting.md) — keybinding system, modes, session/credentials, AppState, exporters, theme, i18n, logging, config, tasks

## Cross-document gap roll-up

- [GAPS.md](GAPS.md) — consolidated list of half-wired / stubbed / missing features across all panels

---

## Conventions in these docs

- "Shipped" = wired and reachable from a default-config build.
- "Stubbed" = type/struct exists but no controller publishes bindings / no helper drives it.
- "Half-wired" = some part works (state, persistence, validators) but the user can't trigger or observe it without code changes.
- Keybind notation matches the live trie (e.g. `<leader>`, `<cr>`, `<esc>`, `<c-d>`). Default leader is `<space>`; default localleader is `,`.
