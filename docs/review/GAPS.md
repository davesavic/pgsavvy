# Consolidated Gaps

Roll-up of half-wired / stubbed / planned-but-not-shipped functionality uncovered during the panel-by-panel audit. Each entry references the panel doc with details. Tiered by likely user-visible impact.

---

## ✅ Resolved (epic dbsavvy-56u, 2026-05-22)

The following Tier-1 items landed in epic `dbsavvy-56u` and are no longer gaps:

- **dbsavvy-56u.1** — INDEXES rail now populates on table activation (composite single-`OnWorker` alongside COLUMNS); `r` RailRefresh binding ships on all 5 rails; `connectInvoker.Connect` writes `LastConnectionID` + maintains `RecentConnectionIDs` (LIFO, dedupe, cap=10); CONNECTIONS rail cursor restored on boot via `restoreConnectionsCursor()`.
- **dbsavvy-56u.2** — `?` opens the cheatsheet popup (`HelpCheatsheet` handler registered in `QuitController.RegisterActions`); `ConfirmationController` ships hardcoded y/n/`<esc>`/`<cr>` defaults under `CONFIRMATION` scope; first-run welcome popup (`FIRST_RUN_TIP` PERSISTENT_POPUP) is pushed after CONNECTIONS on fresh-state launch; hide-overlay `<c-n>`/`<c-p>` bindings registered (godoc now matches code).
- **dbsavvy-56u.3** — `entry_point.Start` now calls `i18n.DetectLocale` → `i18n.LoadAndMerge` (replaces hard-coded `EnglishTranslationSet()`); `ValidateUserConfig` invoked AFTER `NewGui`, BEFORE `g.RunAndHandleError()`, emitting stderr + `g.Close()` + exit 1 on validation error.
- **dbsavvy-56u.4** — `schemas_context.renderRows` now consults `showHiddenMode`, so `<leader>H` has a visible effect; `theme.parseStyle` parses attributes (`bold`/`underline`/`italic`/`on <bg>`), populating `Style.Bg`/`Bold`/`Underline`/`Italic`; status bar `BuildStatusLine` renders `BusyCount` as a braille spinner segment.
- **dbsavvy-56u.5** — Options-bar populates `ExecCtx` (`Mode`/`Scope` non-zero — dynamic predicates now resolve correctly); prompt-chain `Busy` guard added (mutex-protected; overlapping `PromptString`/`PromptChoice` returns `ErrPromptBusy`).

Two pre-existing GAPS-doc items were documentation corrections only (code was already correct before this epic):

- **COLUMNS rail populate** — was already wired at `populateColumnsRail` (`pkg/gui/orchestrator/adapters.go:252`) + `OnTableActivate` before this epic; the GAPS entry was stale.
- **`logs.Init` never called** — superseded by the slog migration; `logs.Open` is invoked at `pkg/app/entry_point.go:179`. The GAPS entry was stale.

---

## 🚨 Tier 1 — User-facing dead ends (likely surprises)

These are bindings, popups, or rails that look implemented but are silently inert.

### Suggestions popup is an empty stub
File: [suggestions.md](suggestions.md)
The type exists, the layout slot is reserved, but no controller or helper drives it. Comment in `suggestions_context.go`: "Suggestion fetching and selection wiring land in later epics."

### TABLES `<cr>` is a permanent placeholder
File: [tables.md](tables.md)
Activation shows the toast "Table data editing is not yet available." (deferred to a future inline editor). No drill-through to a detail view, no DDL preview, no `\d`-style metadata.

### Query history captured but no UI to browse it
File: [result_tabs.md](result_tabs.md)
History written to `$XDG_STATE_HOME/dbsavvy/history.sqlite` (FTS5 indexed, cap=128) — no command, no popup, no keybinding to browse or replay it.

### Clipboard yank is no-op by default
File: [grid_and_expanded.md](grid_and_expanded.md), [cross_cutting.md](cross_cutting.md)
`grid.View.SetClipboard` accepts a writer (OSC-52 / xclip / pbcopy in production), but the default is no-op. Export menu's Clipboard destination buffers payloads to memory and discards on Close — grid clipboard adapter wiring is explicitly deferred. Tracked separately as epic `dbsavvy-8so`.

### Vim `+` / `*` registers don't reach the system clipboard
File: [query_editor.md](query_editor.md)
Falls back to in-memory register store with a one-shot "not yet wired to system clipboard" toast per session.

### Connections rail still missing CRUD (partial fix landed)
File: [connections.md](connections.md)
Cursor restore from `AppState.LastConnectionID` now lands at boot, and `RecentConnectionIDs` is maintained on Connect (both via `dbsavvy-56u.1`). Still missing: delete / edit / disconnect / duplicate-profile bindings — only `a` (add) is wired. To remove a profile after creating it, users must hand-edit the YAML config.

---

## ⚠️ Tier 2 — Missing vim functionality (known deferrals from QA suite)

Documented as deferred in `docs/QA_TEST_SUITE.md` but listed here as part of the inventory.

- `f` / `F` / `t` / `T` / `;` / `,` — char-search motions
- `/` / `?` / `n` / `N` / `*` / `#` — search and substitute (`n` / `N` are bound to result-grid filter navigation)
- `R` Replace mode
- `r{char}` single-char replace
- `x` / `X` delete-char shortcuts (use `dl` / `dh`)
- Macros `q{reg}` / `@{reg}`
- `<c-o>` / `<c-i>` jump-list nav (push-only)
- `'a..z` mark-recall (set works, recall not published)
- `gqq` SQL formatter
- Tree-sitter SQL highlighting
- SQL-string-literal-aware statement splitter (uses naive `;`-split)
- Ex command set limited to `:q`, `:quit`, `:reload` — no `:w`, `:set`, `:e`, `:wq`, `:help`

See [query_editor.md](query_editor.md) and [command_line.md](command_line.md).

---

## ⚙️ Tier 3 — Subsystem half-wiring / latent issues

### Export buffered-threshold ambiguity (AD-17 mismatch)
[export_menu.md](export_menu.md)
bd memory states "hard-block above threshold"; reality is the menu enforces the block only for buffered formats. The warn-threshold label can appear without an actual block on streaming formats.

### Result-tab features missing
[result_tabs.md](result_tabs.md)
- Tab title rename (auto-derived from SQL prefix only)
- Tab drag-reorder
- Search inside cell (only /regex filter on rows)
- Cell-edit / inline mutation (deferred E10)
- FK navigation

### Grid filter-projected cursor
[grid_and_expanded.md](grid_and_expanded.md)
Uses raw-buffer index; cursor may "vanish" outside projected rows until JumpNext lands it. Documented gap.

### Hide-overlay doc/code drift
[hide_overlay.md](hide_overlay.md)
`<c-n>` / `<c-p>` mentioned in controller godoc but not registered in `GetKeybindings`.

### Plan-view i18n not wired
[plan_view.md](plan_view.md)
Action descriptions are English literals; `Tr.Actions.Plan*` i18n fields not yet wired.

### Plan-view missing affordances
[plan_view.md](plan_view.md)
- No in-tree search.
- No node-detail popup — only inline cost / rows columns.

### Selection popup missing affordances
[selection.md](selection.md)
- No multi-select.
- No filter / search.
- No scroll / page navigation for long lists.
- No mouse click-to-select.

### Limit popup has no explicit-dismiss affordance
[limit_popup.md](limit_popup.md)
`<c-c>` from GLOBAL-scoped `QuitController` still quits, but no toast/hint communicates the dismissal path to the user.

### Status bar `tr` parameter unused
[status_bar.md](status_bar.md)
`CollectOptionsForScope(trieSet, mode, scopeKey, tr)` accepts a translator argument that's currently unused (reserved for future i18n separators).

### Messages panel has no controller / clear command
[messages.md](messages.md)
`MessagesContext` is a near-empty stub. Sink writes go directly to the view buffer bypassing the context. No clear / copy / scroll.

### Menu popup is lifecycle skeleton only
[menu_popup.md](menu_popup.md)
`MenuController.Select` is a no-op shim; rendering owned by `MenuPushHelper`. No j/k cursor at controller level.

### Limit popup misnamed
[limit_popup.md](limit_popup.md)
"Limit" implies row-limit but it's actually the terminal-too-small overlay. The actual page-size config (`UIConfig.ResultPageSize`, default 200) lives in YAML config, not in any popup.

### Side rails missing common affordances
[connections.md](connections.md), [schemas.md](schemas.md), [tables.md](tables.md), [columns.md](columns.md), [indexes.md](indexes.md)
- Wheel scroll handlers are stubs across all rails (cancel-arm only)
- No filter / search / sort on any rail

(Refresh `r` binding and CONNECTIONS cursor-restore from `LastConnectionID` landed in `dbsavvy-56u.1`.)

---

## 📋 Notes for QA test authoring

1. The existing `docs/QA_TEST_SUITE.md` covers closed-epic functionality. Use this directory for the **truth-on-master** state.
2. AppState file at `$XDG_STATE_HOME/dbsavvy/app_state.yml` should be `0o600` after first launch — test this explicitly.
3. Verify export buffered-threshold gate by attempting Markdown / JSON Array on a ≥ 100 000-row result; Confirm must be blocked.
4. Test the chained add-connection flow rejects DSNs with inline userinfo passwords (G3-G(ii)).
5. Test that `H` on the schemas rail persists, but does NOT re-populate (regression risk if a future fix changes this).
6. Verify `?` opens the cheatsheet popup from any focused scope and `<esc>` dismisses (dbsavvy-56u.2).
7. Verify Confirmation popup is dismissable with default `y`/`n`/`<esc>`/`<cr>` bindings on a clean profile dir (dbsavvy-56u.2).
8. Verify the first-run welcome popup appears on a clean state dir with zero profiles, dismisses on `<esc>`/`<cr>`, and stamps `StartupTipsSeenAt` (dbsavvy-56u.2).
9. Verify CONNECTIONS rail cursor lands on `LastConnectionID` after restart, and that `RecentConnectionIDs` records on Connect (dbsavvy-56u.1).
10. Verify INDEXES rail populates on table activation alongside COLUMNS (dbsavvy-56u.1).
11. Verify `<leader>H` on schemas rail toggles visible rendering of runtime-hidden schemas (dbsavvy-56u.4).
12. Verify the status bar renders a spinner while `BusyCount > 0` (dbsavvy-56u.4).
13. Verify a malformed user config triggers stderr error + exit 1 on startup (dbsavvy-56u.3).
