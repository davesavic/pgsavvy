# Global Context + First-Run Tip + Expanded-Mode Toggle

## Global Context

### Purpose
No-view host (`GetViewName()` returns `""`) for keybindings that must fire regardless of focus — quit, cheatsheet, leader-prefix chords, command-line entry, rail-jump digits.

### Trigger
Always active; orchestrator always includes GLOBAL in the keybinding-resolution chain.

### Visible content / inputs
None — `GlobalContext` has no view.

### Keybindings while focused (always-on)
- `<c-c>` — `app.quit` (GLOBAL, direct, returns `gocui.ErrQuit`)
- `<leader>q` — `app.quit` (shipped in `GetDefaultConfig().Keybindings`)
- `?` — `help.cheatsheet`; handler registered in `QuitController.RegisterActions` opens the cheatsheet popup scoped to the focused context (dbsavvy-56u.2)
- `:` — `command.open` (ModeNormal, scope `"all"` — fires on every non-popup context)
- `<esc>` / `<cr>` (in `COMMAND_LINE` scope, ModeCommand) — close / submit command-line

### Rail switching
Registered per-rail in `shared.go:railSwitchBindings`, attached to `SCHEMAS` / `TABLES` / `COLUMNS` / `INDEXES` / `QUERY_EDITOR` / `RESULT_GRID`:
- `1` → Schemas
- `2` → Tables
- `3` → Columns
- `4` → Indexes
- `5` → QueryEditor
- `6` → active result tab
- `<tab>` → cycle next: connections → schemas → tables → columns → indexes → query_editor → results (skips results if no tab open; never wraps through nil)

### Result-tab jumping (GLOBAL scope — fires from any view)
- `<leader>1`..`<leader>9` → `result.tab.jumpN`

### Leader-prefix chords
- Leader rune is configurable (`UserConfig.Leader`, default `" "` space)
- `<leader>` token in any binding shorthand is expanded at `Build()` time
- Digit leaders are rejected (`keys: leader %q is a digit; counts would be ambiguous`)

### Chord timing
- `Timeout` / `TimeoutLen` — 1 s (full chord resolve window)
- `TtimeoutLen` — 50 ms (key-code disambiguation)
- `WhichKeyDelay` — 300 ms (delay before which-key popup appears on partial chord)

### Multi-step / chaining
N/A.

### Persisted state
N/A (delegates to per-action persistence).

### Gaps / TODOs / dead-looking code
- No `<s-tab>` reverse-cycle binding.
- No explicit `<c-l>` redraw binding (gocui auto-redraws on `Update`; the `RefreshHelper` reloads side-rail data, not the screen).
- `app.quit` is registered twice (`<c-c>` controller-level, `<leader>q` default-config-level) — intentional but worth noting for QA matrix.

---

## First-Run Tip

### Purpose
One-shot welcome popup shown on first launch when no connection profiles exist; teaches `?` (help) and `a` (add connection).

### Trigger (predicate)
`data.ShouldShowFirstRunTip(store, profilesProvider)` returns true iff `store != nil` AND `!store.IsStartupTipsSeen()` AND `len(profiles) == 0`.

The orchestrator pushes the `FIRST_RUN_TIP` context after CONNECTIONS during initial focus-stack wiring when the predicate holds (`pkg/gui/orchestrator/gui.go:935-941`, dbsavvy-56u.2). The context is `PERSISTENT_POPUP` so unrelated popups don't pop it.

### Visible content / inputs
- `Tr.FirstRunTipTitle` = "Welcome to dbsavvy"
- `Tr.FirstRunTipBody` = "Press ? at any time to see available keys. Press a to add your first connection."

### Keybindings while focused
- `<esc>` / `<cr>` dispatch `TipDismiss` directly via the driver (`pkg/gui/orchestrator/gui.go:775-776`); the handler pops the tip context and stamps `StartupTipsSeenAt`. No controller publishes bindings — the FIRST_RUN_TIP context carries no controller surface.

### Multi-step / chaining
N/A.

### Persisted state
- `AppState.StartupTipsSeenAt` (YAML field `startup_tips_seen_at`) stamped via `AppStateStore.StampStartupTips()` → `MutateAndSave` → debounced atomic write at file mode **`0o600`**.
- `IsStartupTipsSeen()` uses `!t.IsZero()`.

### Gaps / TODOs / dead-looking code
- None known for the first-run flow; popup push + dismiss + persistence are wired end-to-end (dbsavvy-56u.2).

---

## Expanded View Mode

### Purpose
Toggle the active result-tab grid between standard row/column grid and psql `\x`-style one-record-per-screen expanded view. Persisted globally.

### Trigger
`<leader>gx` in `RESULT_GRID` scope (`commands.ResultViewToggle`).

### Visible content / inputs
- No popup — modifies the active result-tab grid render path in-place.
- Grid renders `ViewModeGrid` (default) or `ViewModeExpanded` per `grid.View.viewMode`.

### Keybindings
- `<leader>gx` — flip view mode (`ResultTabsHelper.ToggleViewMode()`).
- Motion keys are viewMode-aware inside `grid.View`: `G` in expanded mode jumps to last loaded record; in grid mode it triggers `ReadToEnd` drain (with the >1 M-row warn). Cursor motions, half-page, wrapped-line, select-row, select-block all dispatch through the same handlers and route internally per viewMode.

### Multi-step / chaining
N/A.

### Persisted state
- `AppState.LastResultViewMode` (YAML `last_result_view_mode`) via `AppStateStore.SetLastResultViewMode`; new tabs seed their grid viewMode from `LastResultViewModeSnapshot()` on creation.

### Gaps / TODOs / dead-looking code
- Persistence is global, not per-connection or per-tab — a user who toggles expanded on one tab gets expanded on every freshly opened tab thereafter. Likely intentional but worth confirming in QA.
- `SetViewMode` accepts any string but silently falls back to `ViewModeGrid` for unknowns — no error surface.
