# dbsavvy — Design Document

A terminal database client modeled on **lazygit**'s architecture, with
**fully customizable vim-style keybindings** (modes, leader keys,
arbitrary chord sequences, per-context maps).

> **Status:** design-stage. No code in this repo yet. This document is the
> specification work-product; implementation is phased in §17.

> **Revision 2026-05-17 — gap-closure pass.** A pre-planning review
> closed 26 v1 behavioral gaps. Index of changes, each linked to the
> section it lands in:
>
> 1. Multi-statement run → **N result tabs in the secondary slot** (§7, §12.2)
> 2. Concurrent queries → **one in-flight per connection**, pane-switch preempts (§12.2)
> 3. Visual-select run → `<leader>r` overloaded; selection split on `;` → N tabs (§13.4)
> 4. Pagination → auto-pull near tail, `G` triggers `ReadToEnd` (§12.3)
> 5. Server NOTICE/WARNING → `command_log` + toast on first per run (§12.9)
> 6. Cancel when `HasLiveCancel: false` → `<leader>x` hard-disabled, `<esc>` detaches (§12.4)
> 7. Query history UX → `HISTORY` popup, `<leader>h`, FTS search (§8, §13.6)
> 8. Result export → `<leader>oe` menu, CSV/TSV/JSON/NDJSON/SQL INSERTs/Markdown (§12.7)
> 9. EXPLAIN rendering → indented tree, cost-percentile coloring, `<CR>` expand, `o` raw (§12.8)
> 10. First-run → empty CONNECTIONS + `a` add + one-time tip (§15.7)
> 11. Lost connection → lazy reconnect dialog: Retry / Pick other / Quit (§15.8)
> 12. Multi-connection → **single-active for v1**; `pair.SchemaDiff` deferred to Phase 8 (§7, §18)
> 13. `search_path` / `SET ROLE` → `:` ex-line accepts `SET …`; `<leader>p` shortcut (§15.9)
> 14. Transaction model → implicit autocommit; `<leader>tx` submenu; `[TX]` indicator (§15.10)
> 15. Quit with open tx → block with confirmation in `connection.color` (§15.10)
> 16. In-grid filter → `/regex` over loaded rows; resets on next query (§12.3)
> 17. In-grid sort → `<leader>s{col}` client-side over loaded rows (§12.3)
> 18. Column hide/show → `<leader>gH`; persisted per (conn, table); reorder deferred (§12.3)
> 19. Forward FK nav → `gd` opens new result tab; jumplist marks (§12.6)
> 20. Reverse FK nav → `gD` opens table-picker menu (§12.6)
> 21. Schema hide/show → `connection.hidden_schemas` + AppState toggle; `H`/`<leader>H`/`U` (§15.9)
> 22. Mouse → click-focus, wheel-scroll, click-row-select, double-click table (§6.1)
> 23. Statement timeout → `connection.statement_timeout: 0` default; `<leader>tt` override (§11.2)
> 24. Confirmation memory → **always re-prompt**; no session unlock (§12.5.5)
> 25. Macros → record resolved keystrokes, scope-locked to recording context (§10.13)
> 26. Cheatsheet completeness → unit-tested invariant; `· / ✱ / ★` source legend (§10.11)
>
> The phased plan in §18 is updated to slot these into Phases 1–6.

---

## Table of contents

1. [Vision & Design Principles](#1-vision--design-principles)
2. [Why lazygit's model (and not bubbletea/Elm)](#2-why-lazygits-model-and-not-bubbletea-elm)
3. [Architectural Overview](#3-architectural-overview)
4. [Package Layout](#4-package-layout)
5. [Core Concepts](#5-core-concepts)
6. [TUI Layer: gocui](#6-tui-layer-gocui)
7. [Layout & Windows](#7-layout--windows)
8. [Contexts & the Focus Stack](#8-contexts--the-focus-stack)
9. [Controllers, Helpers, Actions](#9-controllers-helpers-actions)
10. [The Keybinding System (vim-style, full spec)](#10-the-keybinding-system-vim-style-full-spec)
11. [Driver Layer](#11-driver-layer)
12. [Async Query Execution & Result Rendering](#12-async-query-execution--result-rendering)
13. [The SQL Editor](#13-the-sql-editor)
14. [Custom Commands (Extensibility)](#14-custom-commands-extensibility)
15. [Config System](#15-config-system)
16. [Themes, Styles, i18n, State](#16-themes-styles-i18n-state)
17. [Threading Model](#17-threading-model)
18. [Phased Implementation Plan](#18-phased-implementation-plan)
19. [Appendix A — Layered Dependency Rules](#appendix-a--layered-dependency-rules)
20. [Appendix B — Comparison Matrix with lazygit](#appendix-b--comparison-matrix-with-lazygit)
21. [Appendix C — Prior Art](#appendix-c--prior-art)

---

## 1. Vision & Design Principles

### Vision

> **dbsavvy is the most enjoyable TUI for working with relational databases.**
> It should make TablePlus users want to leave their GUI, and `psql` users
> feel they've kept everything they love.

### Seven Principles (adapted from lazygit's VISION.md)

| Principle | What it means for dbsavvy |
|---|---|
| **Discoverability** | Bottom options bar shows current-context keys. `?` opens the full cheatsheet. Disabled actions show *why* they are disabled ("no result set yet"). A which-key popup appears after a chord prefix. |
| **Simplicity** | Connect → browse → query → results in ≤ 3 keystrokes from cold start. No "wizards." Sensible defaults; very few config knobs that everyone has to set. |
| **Safety** | Destructive operations (DROP, TRUNCATE, UPDATE/DELETE without WHERE) prompt for confirmation. Auto-detected transactional commands ask before COMMIT. Undo via query history. |
| **Power** | Pro users should *rarely* drop down to `psql`. Stream multi-million-row results. Cancel queries instantly. Custom SQL snippets bound to keys. |
| **Speed** | Startup < 200 ms. Schema browsing cached with TTL. Result-set first-page render < 100 ms after `<CR>`. Throttle on stress (lazygit's `tasks.go:138` pattern). |
| **Conformity with the DBMS** | Postgres errors carry SQLSTATE + position + hint — show all three. Don't override server-side settings without permission. Be a *good citizen* on the wire (one connection per session, no probe-spam). |
| **Think of the codebase** | Layered dependencies (Appendix A). No new abstractions without two concrete uses. Migrations on config from day one (lazygit's `computeMigratedConfig` pattern). |

**Resolving conflicts:** err on the side of *safety + simplicity by default*,
with config knobs and explicit keybindings for users who want *power +
speed*. Force-quitting a query is one key. Force-dropping a table requires
confirmation **unless** the user has set `confirm.drop_table: false`.

---

## 2. Why lazygit's model (and not bubbletea/Elm)

A prior attempt at this tool used Bubble Tea's Elm architecture and felt
**clunky**. After analyzing both, here are the concrete reasons lazygit's
imperative gocui model is a better fit:

| Concern | Bubble Tea (Elm) | lazygit (gocui) |
|---|---|---|
| **Mental model** | Every keystroke produces an `(model, cmd)` tuple. Whole world re-renders. | Keystroke → handler → mutate state → next paint redraws affected views only. |
| **Multi-pane focus** | One global Update; pane focus encoded in model. Awkward for ≥ 6 panes. | First-class **Context** per pane + **ContextMgr** stack. Natural for the side-rail + main-pair layout. |
| **Large streaming output** | Tea Cmds and goroutines tend to coalesce in the model; backpressure is manual. | `ViewBufferManager.NewCmdTask` pattern: pull `ReadLines(n)` model, automatic preempt on context switch, throttle on stress. |
| **Custom keybindings at runtime** | Hard — Update is one switch statement. | Keybindings are *data* (`[]*Binding`), rebuilt on every context change and config reload. |
| **Sub-process / PTY** | Possible but cumbersome. | First-class: `CmdObj.UsePty()`. |
| **Modal editor in one pane while list in another** | Conflicting key handlers fight in `Update`. | Each Context owns its bindings; gocui dispatches to the focused view first, falls back to global. |
| **"Feel"** | Quirky scroll/cursor; redraw flicker on big diffs. | Tight; flicker minimized via `OnUIThreadContentOnly` (`gui.go:640`). |

For a tool whose **dominant interaction is multi-pane navigation with
streaming output and rich keybindings**, lazygit's model is the right fit.

---

## 3. Architectural Overview

### Layers (bottom-up)

```
┌──────────────────────────────────────────────────────────────────────┐
│  CLI entrypoint  (main.go)                                           │
│   parse flags → exit-subcommands → bootstrap                         │
└──────────────────────────────────────────────────────────────────────┘
┌──────────────────────────────────────────────────────────────────────┐
│  pkg/app         App lifecycle, dependency wiring                    │
│  pkg/common      Common{Log, Tr, UserConfig (atomic), AppState, Fs}  │
│  pkg/config      User config schema, defaults, migrations, hot reload│
└──────────────────────────────────────────────────────────────────────┘
┌──────────────────────────────────────────────────────────────────────┐
│  pkg/drivers     Driver interface + per-DB packages                  │
│   ├── pg/        Postgres v1 (pgx)                                   │
│   ├── mysql/     (v2)                                                │
│   └── sqlite/    (v2)                                                │
│  pkg/session     Connection pool, sessions, transactions             │
│  pkg/query       QueryObj builder, cancellation, parameter binding   │
│  pkg/models      Connection, Schema, Table, Column, Row, Result      │
└──────────────────────────────────────────────────────────────────────┘
┌──────────────────────────────────────────────────────────────────────┐
│  pkg/tasks       ResultBufferManager — streaming, preempt, throttle  │
│  pkg/gocui       Vendored fork (compatible with lazygit's)           │
│  pkg/gui         Gui struct, layout, view registry                   │
│   ├── context/   ContextTree, focus stack                            │
│   ├── controllers/  Per-context keybinding declarations & handlers   │
│   │     └── helpers/  Multi-step UI flows shared across controllers  │
│   ├── keys/      ★ Chord trie + modal dispatcher (★ = novel vs lazygit) │
│   ├── editor/    ★ Multi-line SQL buffer with syntax + completion    │
│   ├── grid/      ★ Result-grid view (custom gocui view)              │
│   ├── presentation/ Formatting (rows → cells → cell strings)         │
│   ├── popup/     Confirmation, prompt, suggestions                   │
│   ├── services/custom_commands/  User-defined snippets               │
│   ├── style/     Reusable text styles                                │
│   └── theme/     Color globals (reassigned on reload)                │
│  pkg/i18n        TranslationSet — flat struct of strings             │
└──────────────────────────────────────────────────────────────────────┘
```

The ★-marked packages are the only places we **deviate** from lazygit's
shape in non-trivial ways. The rest is "lazygit but with DB nouns."

### Lifecycle

```
main.go → app.Start
            ├─ parse CLI args                            (flaggy)
            ├─ handle --version, --logs, --config-dir    (exit early)
            ├─ NewAppConfig                              (loads YAML, merges defaults, migrations)
            ├─ NewCommon                                 (assembles Common bag)
            ├─ NewDriverRegistry                         (registers pg, mysql, sqlite, ...)
            ├─ NewConnectionStore                        (reads ~/.config/dbsavvy/connections.yml)
            ├─ NewGui(common, cfg, drivers, conns)       (no TUI yet — testable)
            └─ gui.RunAndHandleError
                  ├─ initGocui                           (TUI alive now)
                  ├─ g.SetManager(layout)
                  ├─ createAllViews
                  ├─ onNewConnection(...)               (analog of lazygit onNewRepo)
                  ├─ BackgroundRoutineMgr.start          (config-reload poller, refresh ticker)
                  └─ g.MainLoop                          (blocks until ErrQuit)
```

Each step is independently testable. `NewGui` without `initGocui` enables
headless integration tests (the lazygit pattern at `gui.go:805`).

---

## 4. Package Layout

```
dbsavvy/
├── docs/
│   ├── Config.md
│   ├── Custom_Commands.md
│   ├── Keybindings.md               # auto-generated cheatsheet
│   └── dev/
│       └── Codebase_Guide.md
├── schema/
│   └── config.json                  # generated jsonschema for editor LSPs
├── pkg/
│   ├── app/
│   │   ├── app.go                   # App struct, Run
│   │   └── entry_point.go           # Start: flags, exit-subcommands, wiring
│   ├── common/
│   │   ├── common.go                # Common struct (embedded everywhere)
│   │   └── dummies.go               # NewDummyCommon for tests
│   ├── config/
│   │   ├── user_config.go           # YAML schema (defaults inline)
│   │   ├── app_config.go            # load/reload/migrate/XDG
│   │   ├── connections.go           # connection profile loader
│   │   ├── keynames.go              # <c-a>, <leader>, <esc>, etc.
│   │   ├── migrations.go            # config-version upgrades
│   │   └── validation.go            # post-unmarshal checks
│   ├── jsonschema/
│   │   └── generate.go              # go:generate produces schema/config.json
│   ├── i18n/
│   │   ├── i18n.go
│   │   ├── english.go               # baseline TranslationSet
│   │   └── translations/*.json
│   ├── constants/
│   │   └── constants.go
│   ├── logs/
│   │   └── logs.go                  # logrus + xdg state file
│   ├── utils/
│   │   ├── strings.go
│   │   ├── slice.go
│   │   ├── template.go              # ResolveTemplate wrapper
│   │   └── scanner.go               # ScanLinesAndTruncateWhenLongerThanBuffer
│   ├── theme/
│   │   └── theme.go                 # global ActiveBorderColor, SelectedRowBg, etc.
│   ├── models/
│   │   ├── connection.go
│   │   ├── database.go
│   │   ├── schema.go
│   │   ├── table.go
│   │   ├── column.go
│   │   ├── index.go
│   │   ├── constraint.go
│   │   ├── row.go
│   │   ├── result.go
│   │   ├── query.go
│   │   └── plan.go
│   ├── drivers/
│   │   ├── driver.go                # the Driver interface (§11)
│   │   ├── registry.go              # name → factory
│   │   └── pg/
│   │       ├── driver.go            # implements Driver
│   │       ├── tables_loader.go
│   │       ├── columns_loader.go
│   │       ├── indexes_loader.go
│   │       ├── sequences_loader.go
│   │       ├── functions_loader.go
│   │       ├── extensions_loader.go
│   │       ├── plan.go              # EXPLAIN parsing
│   │       ├── cancel.go            # pg_cancel_backend via second conn
│   │       └── sql/*.sql            # embedded queries
│   ├── session/
│   │   ├── pool.go                  # connection pool per connection profile
│   │   ├── session.go               # Session = *sql.Conn + tx state
│   │   ├── transaction.go
│   │   └── credentials.go           # keychain integration
│   ├── query/
│   │   ├── query_obj.go             # fluent QueryObj (mirrors lazygit CmdObj)
│   │   ├── runner.go                # Run / Stream / RunWithRows
│   │   ├── cancel.go                # cancel registry, per-driver dispatch
│   │   └── history.go               # sqlite-backed history
│   ├── tasks/
│   │   └── tasks.go                 # ResultBufferManager (extends lazygit/tasks)
│   ├── gocui/                       # vendored fork OR direct dep
│   ├── gui/
│   │   ├── gui.go                   # Gui struct, NewGui, Run, RunAndHandleError
│   │   ├── layout.go                # the layout(g) callback
│   │   ├── views.go                 # types.Views; createAllViews; orderedViews
│   │   ├── main_panels.go           # MainContextPair, RefreshMainView
│   │   ├── keybindings.go           # legacy global bindings + reset loop
│   │   ├── options_map.go           # bottom options bar
│   │   ├── command_log_panel.go     # action log
│   │   ├── editors.go               # editor builders
│   │   ├── modes/                   # cherry-pick equivalents (e.g. "tx in progress")
│   │   ├── types/
│   │   │   ├── context.go           # Context, IBaseContext, ContextKind enum
│   │   │   ├── keybindings.go       # Binding, ChordBinding, Mode
│   │   │   ├── common.go            # IGuiCommon, HelperCommon
│   │   │   ├── views.go             # Views struct
│   │   │   ├── rendering.go         # UpdateTask interface + impls
│   │   │   └── refresh.go           # RefreshOptions
│   │   ├── context/
│   │   │   ├── context.go           # ContextTree, ContextKey
│   │   │   ├── base_context.go      # BaseContext (multi-controller composition)
│   │   │   ├── side_list_context.go # for Connections/Schemas/Tables/Columns
│   │   │   ├── query_editor_context.go
│   │   │   ├── result_grid_context.go
│   │   │   ├── plan_context.go
│   │   │   ├── menu_context.go
│   │   │   ├── confirmation_context.go
│   │   │   └── setup.go             # NewContextTree
│   │   ├── controllers/
│   │   │   ├── controllers.go       # AttachControllers
│   │   │   ├── base_controller.go
│   │   │   ├── list_controller_trait.go
│   │   │   ├── connections_controller.go
│   │   │   ├── schemas_controller.go
│   │   │   ├── tables_controller.go
│   │   │   ├── columns_controller.go
│   │   │   ├── indexes_controller.go
│   │   │   ├── query_editor_controller.go
│   │   │   ├── result_grid_controller.go
│   │   │   ├── plan_controller.go
│   │   │   ├── menu_controller.go
│   │   │   ├── quit_controller.go
│   │   │   └── helpers/
│   │   │       ├── helpers.go        # Helpers bag
│   │   │       ├── connect_helper.go
│   │   │       ├── tables_helper.go
│   │   │       ├── query_helper.go   # run query, stream rows
│   │   │       ├── tx_helper.go      # begin/commit/rollback
│   │   │       ├── confirm_helper.go
│   │   │       ├── prompt_helper.go
│   │   │       ├── refresh_helper.go
│   │   │       └── window_arrangement_helper.go
│   │   ├── keys/                    # ★ NEW vs lazygit
│   │   │   ├── chord_trie.go        # the trie data structure
│   │   │   ├── matcher.go           # state machine: idle → partial → leaf
│   │   │   ├── modes.go             # Normal | Insert | Visual | OperatorPending | Command
│   │   │   ├── command_registry.go  # named actions (action.id → handler)
│   │   │   ├── parser.go            # parse "<leader>fp", "dap", "<c-w>v"
│   │   │   └── whichkey.go          # transient floating hint popup
│   │   ├── editor/                  # ★ NEW vs lazygit
│   │   │   ├── buffer.go            # multi-line text buffer
│   │   │   ├── cursor.go            # cursor + marks + jumplist
│   │   │   ├── undo.go              # undo tree
│   │   │   ├── selection.go         # visual range
│   │   │   ├── motion.go            # word, line, block motions
│   │   │   ├── textobject.go        # i", a(, ip (inner paragraph), etc.
│   │   │   ├── operator.go          # d, y, c, gU, gu
│   │   │   ├── highlighter.go       # tree-sitter integration
│   │   │   └── completion.go        # column/keyword/snippet suggestions
│   │   ├── grid/                    # ★ NEW vs lazygit
│   │   │   ├── view.go              # custom gocui view subtype
│   │   │   ├── columns.go           # column auto-sizing, freeze
│   │   │   ├── cells.go             # NULL marker, JSON expand, BLOB hex
│   │   │   ├── selection.go         # cell / row / column / range
│   │   │   └── scroll.go            # h/j/k/l + page nav + jump
│   │   ├── presentation/
│   │   │   ├── tables_view_model.go
│   │   │   ├── tree_view_model.go   # connection → schema → table tree
│   │   │   ├── value_formatter.go
│   │   │   └── plan_view_model.go
│   │   ├── popup/
│   │   ├── style/
│   │   ├── services/
│   │   │   └── custom_commands/
│   │   │       ├── client.go
│   │   │       ├── handler_creator.go
│   │   │       ├── keybinding_creator.go
│   │   │       ├── session_state_loader.go
│   │   │       ├── menu_generator.go
│   │   │       └── resolver.go
│   │   └── status/
│   ├── cheatsheet/
│   │   └── generator.go             # produces docs/Keybindings*.md
│   └── env/
├── test/
│   ├── integration/
│   └── unit/
├── docker/
│   └── postgres/                    # docker-compose for integration tests
├── Taskfile.yml                     # build/test/lint/release commands
├── go.mod
├── main.go                          # entrypoint → app.Start
└── README.md
```

This is intentionally close to lazygit's tree, both because the patterns
transfer and because anyone who's read lazygit's code will navigate this
without a map.

---

## 5. Core Concepts

These are the **shared vocabulary** the rest of the document uses. Most are
imported wholesale from lazygit; the novel ones are marked **★**.

| Concept | Owner | What it is |
|---|---|---|
| **View** | gocui | A rectangular buffer with a frame, cursor, scroll origin, editor, optional autoscroll. Identified by a string name. |
| **Window** | layout helper | A logical *slot* on screen. Multiple Views can share one Window via tabs (e.g. the left-rail's `tree` window cycles between `Connections`/`Schemas`/`Tables`/...). |
| **Context** | `pkg/gui/context` | A focusable entity that wraps a View, owns its keybindings, its view-model state, and its lifecycle hooks. The unit of focus. |
| **ContextMgr** | `pkg/gui/context` | A stack of contexts. `Push(side)` resets the stack; `Push(popup)` overlays; `Pop()` returns. The top is the current focus. |
| **Controller** | `pkg/gui/controllers` | Declares `[]*Binding` (or in our case `[]*ChordBinding`) attached to one or more Contexts. Handler bodies are thin: validate → log → call Helper. |
| **Helper** | `pkg/gui/controllers/helpers` | Multi-step UI orchestration shared across Controllers (prompts, confirms, refresh-on-success). |
| **★ Action** | `pkg/gui/keys` | A named handler in the **CommandRegistry** (e.g. `"query.run"`, `"table.refresh"`). Keys map to action IDs, not to closures. This is the inversion that makes config user-friendly. |
| **★ ChordBinding** | `pkg/gui/keys` | `{Mode, Scope, Sequence []Key, ActionID, Description, GetDisabledReason}`. Replaces lazygit's `Binding` for everything except gocui-level mouse. |
| **★ ChordTrie / Matcher** | `pkg/gui/keys` | The dispatcher state machine: idle → partial-match (with timeout) → leaf. Per (mode, scope). |
| **Driver** | `pkg/drivers` | Implementation of the per-DB interface: list schemas/tables/columns, run query, stream rows, EXPLAIN, cancel. |
| **Connection / Session** | `pkg/session` | A `Connection` is a configured profile; a `Session` is an active `*sql.Conn` (so transaction state lives somewhere). |
| **QueryObj** | `pkg/query` | Fluent builder mirroring lazygit's `CmdObj`. Methods: `.Stream()`, `.WithTx(tx)`, `.WithTimeout(d)`, `.DontLog()`, `.Bind(args)`. Terminators: `Run / RunWithRows / RunAndStreamRows`. |
| **Task** | `pkg/tasks` | One of `RenderStringTask | RunQueryTask | RunPtyTask | RenderPlanTask`. The interface `UpdateTask` is dispatched by `RefreshMainView`. |
| **ResultBufferManager** | `pkg/tasks` | Per-result-view manager: lazy `ReadRows(n)` pulls, preempts on context switch, throttles on stress. Direct port of lazygit's `ViewBufferManager`. |
| **Model** | `pkg/models` | Plain data: `Table`, `Column`, `Row`. Mutated by loaders, read by presentation. |
| **ViewModel** | `pkg/gui/presentation` | Stateful wrapper around models for one view: tree expansion, filter, sort. |
| **Common** | `pkg/common` | The cross-cutting dependency bag — `Log`, `Tr`, `UserConfig (atomic.Pointer)`, `AppState`, `Fs`. Embedded into virtually every receiver. |

---

## 6. TUI Layer: gocui

We adopt **gocui** directly (lazygit's vendored fork, currently). gocui is
a tcell-based imperative TUI library.

### Key facts (verified from `pkg/gocui/`)

- `gocui.NewGui(opts)` constructs a `*Gui`.
- `g.SetManager(managers...)` — every Manager implements `Layout(*Gui) error`,
  called on every redraw.
- `g.SetView(name, x0, y0, x1, y1, overlaps)` creates or resizes a named view.
- `g.SetKeybinding(viewName, key, mod, handler)` registers one key binding.
  Dispatcher iterates registrations in order; per-view binding fires first,
  global (`viewName == ""`) is a fall-through. **No chord state**.
- Redraw is **manual but driven** by events. UI thread is the goroutine
  running `MainLoop`. Background goroutines must call `g.Update(func)` or
  `g.UpdateContentOnly(func)` to mutate state safely.
- `g.UpdateContentOnly` skips the layout pass — important perf optimization
  for "just append a row" streaming.

### What we don't do at the gocui level

- We **do not** register chord bindings with gocui directly. Instead we
  register only the *first key of every chord* and one fall-through "any
  key" binding when the matcher is in partial state. The Matcher (§10)
  intercepts.

### What we add on top of gocui

- A custom **GridView** subtype (`pkg/gui/grid/view.go`) that doesn't use
  gocui's line buffer. Cells are rendered directly from a backing
  `[][]Cell` against the view's frame. This is necessary because gocui's
  default view is `io.Writer`-shaped — fine for text streams, wrong for
  tabular data with column-aware formatting.

### 6.1 Mouse support (v1 scope)

Mouse is **on by default**; a top-level `mouse.enabled: false` config
flag disables it for users who proxy through tmux selection or want a
keyboard-only feel. Wheel events use tcell's mouse mode; selection-for-
clipboard remains accessible via Shift-drag (terminal convention).

| Interaction | Effect |
|---|---|
| Single-click on any view | Focus that view (push its context per §8 push semantics). |
| Wheel up/down on focused view | Scroll one line; with Shift, scroll one page. |
| Single-click on a row in the result grid or any side-rail | Select that row (same effect as moving the cursor there with `j`/`k`). |
| Single-click on a cell in the grid | Select that cell (same as `j`/`k` + `h`/`l`). |
| Double-click on a table in the `TABLES` context | Equivalent to `<CR>` — open for data edit (§9 example). |
| Double-click on a column header in the grid | Sort by that column ascending; second double-click toggles descending; third clears sort (§12.3). |

Deferred to v2: drag-to-resize panes, drag-select for visual mode in
the editor, drag-to-reorder columns, right-click context menus.

---

## 7. Layout & Windows

We reuse lazygit's `boxlayout` library (`lazycore/pkg/boxlayout`) — a
~200 LOC declarative flex layout: `Box{Direction, Weight, Size, Children}`
→ `map[string]Dimensions`.

The single `layout(g *gocui.Gui) error` callback in `pkg/gui/layout.go`:

1. Compute window dimensions via `WindowArrangementHelper.GetWindowDimensions`.
2. For each Context, `g.SetView(viewName, x0, y0, x1, y1, 0)` from the
   dimension map.
3. Run `NeedsRerenderOnHeightChange` / `NeedsRerenderOnWidthChange` checks.
4. Z-order via `g.SetViewOnTop` in `orderedViewNames`.
5. Drain `afterLayoutFuncs` channel.

### Screen layout (80×24 minimum, scales up)

```
┌─[Connection: prod-pg]────────────────────────────────────────────────┐
│ ┌──────────────────┐ ┌────────────────────────────────────────────┐ │
│ │ Schemas       (1)│ │ Query                                  (5) │ │
│ │  ▶ public      *│ │ SELECT u.id, u.email                       │ │
│ │    auth        │ │ FROM users u                                │ │
│ │    analytics   │ │ WHERE u.created_at > now() - interval '7d' │ │
│ ├──────────────────┤ │ ORDER BY u.created_at DESC                  │ │
│ │ Tables        (2)│ │ LIMIT 100                                   │ │
│ │  ▶ users     12k│ ├────────────────────────────────────────────┤ │
│ │    orders   3.4M│ │ Results  (rows 1–100 of 100)            (6) │ │
│ │    payments  87k│ │ ┌────┬────────────┬─────────────────────┐  │ │
│ │    events   23M│ │ │  id│ email      │ created_at          │  │ │
│ ├──────────────────┤ │ ├────┼────────────┼─────────────────────┤  │ │
│ │ Columns       (3)│ │ │1234│ a@b.co     │ 2026-05-17 10:14:02 │  │ │
│ │    id     int  pk│ │ │1235│ c@d.io     │ 2026-05-17 09:58:11 │  │ │
│ │    email  text  │ │ │... │ ...        │ ...                 │  │ │
│ │    name   text  │ │ └────┴────────────┴─────────────────────┘  │ │
│ │    created  ts │ │                                              │ │
│ ├──────────────────┤ ├────────────────────────────────────────────┤ │
│ │ Indexes       (4)│ │ Plan / Log                              (7) │ │
│ │    pk_users     │ │ Index Scan using idx_users_created on users │ │
│ │    idx_email   *│ │   (cost=0.42..4815.84 rows=100)             │ │
│ └──────────────────┘ └────────────────────────────────────────────┘ │
│ [n]ew query  [r]un  [c]ancel  [<leader>tx]act  ...  ?: help    [─]│
└──────────────────────────────────────────────────────────────────────┘
```

### Window slots

| Slot | Default view | Tabs |
|---|---|---|
| `tree` (left rail) | `schemas` | schemas / tables / columns / indexes — switch by digit or `<tab>` |
| `main` (right top) | `query_editor` | query_editor / table_data / view_definition |
| `secondary` (right bottom) | `result_grid_1` | **N result tabs** + plan / log / errors — see §12.2 |
| `status` (bottom line) | `options_bar` | options / progress / search |
| `extras` (overlay) | `command_log` | always-on-top toggle (`<leader>l`) |

**Multi-result-tab model (v1).** The `secondary` slot hosts an
ordered list of result tabs, one per `<leader>r` / `<leader>R` /
visual-select run, plus the persistent `plan`, `log`, and `errors`
tabs. New tabs open to the right of the last result tab and become
active. `<leader>1`…`<leader>9` jump by index; `gt` / `gT` cycle next
/ previous (vim convention); `<leader>x` cancels the active tab's
stream; `<leader>X` closes the active tab. The maximum live tab count
is `ui.result_tabs_max` (default 8); creating a 9th evicts the
oldest non-pinned tab (`<leader>=` pins a tab against eviction).

The slot/view distinction is critical: a `MainContextPair{Main, Secondary}`
groups two contexts that should be visible together (lazygit's
`main_panels.go:29` pattern). Mode-specific pairs for **v1**:

- `pair.Normal`         — `QueryEditor / Result(active tab)`
- `pair.TableData`      — `TableDataEditor / Result(active tab)`   (clicking a table jumps here)
- `pair.PlanFocus`      — `Plan / Query`                            (debugging a slow query)

Deferred to Phase 8 (multi-connection / schema-diff work):
`pair.SchemaDiff` (`SchemaDiffLeft / SchemaDiffRight`),
`pair.MigrationApply` (`Migration / Log`).

### Z-order (front → back)

1. `limit` overlay (when screen too small — `width<10 || height<10`)
2. Popups: confirmation, prompt, menu, suggestions, tooltip, which-key
3. Status line: options, app-status, search
4. Extras: command_log
5. Main pair (query_editor / result / plan / log)
6. Side rail tabs (schemas / tables / columns / indexes)

`orderedViewNames` enumerates this in `views.go`.

---

## 8. Contexts & the Focus Stack

### `ContextKind` enum

Reused verbatim from lazygit:

```go
type ContextKind int

const (
    SIDE_CONTEXT       ContextKind = iota // left-rail entries
    MAIN_CONTEXT                          // right-top + right-bottom pair
    PERSISTENT_POPUP                      // identity preserved across pushes
    TEMPORARY_POPUP                       // discarded on next push
    EXTRAS_CONTEXT                        // command_log
    GLOBAL_CONTEXT                        // no view; hosts global bindings only
    DISPLAY_CONTEXT                       // pure render target (which-key popup)
)
```

### Concrete contexts in dbsavvy v1

| Key | Kind | View | Role |
|---|---|---|---|
| `CONNECTIONS` | `SIDE_CONTEXT` | `connections` | Connection picker (visible only when no connection active) |
| `SCHEMAS` | `SIDE_CONTEXT` | `schemas` | List schemas |
| `TABLES` | `SIDE_CONTEXT` | `tables` | List tables in current schema |
| `COLUMNS` | `SIDE_CONTEXT` | `columns` | Columns of selected table |
| `INDEXES` | `SIDE_CONTEXT` | `indexes` | Indexes of selected table |
| `QUERY_EDITOR` | `MAIN_CONTEXT` | `main` | Multi-line SQL buffer |
| `TABLE_DATA_EDITOR` | `MAIN_CONTEXT` | `main` | Inline cell editing |
| `RESULT_GRID` | `MAIN_CONTEXT` | `secondary` | Result rows |
| `PLAN` | `MAIN_CONTEXT` | `secondary` | EXPLAIN ANALYZE output |
| `LOG` | `EXTRAS_CONTEXT` | `extras` | Action / SQL log |
| `MENU` | `TEMPORARY_POPUP` | `menu` | Generic menu (chord-resolved or `?` help) |
| `CONFIRMATION` | `TEMPORARY_POPUP` | `confirmation` | "Are you sure?" |
| `PROMPT` | `TEMPORARY_POPUP` | `prompt` | Single-line input |
| `SUGGESTIONS` | `TEMPORARY_POPUP` | `suggestions` | Autocomplete |
| `HISTORY` | `TEMPORARY_POPUP` | `history` | Query history browser (see §13.6) |
| `WHICH_KEY` | `DISPLAY_CONTEXT` | `whichkey` | Transient floating chord hint |
| `GLOBAL` | `GLOBAL_CONTEXT` | nil | Hosts global bindings (leader prefix, `:` command line) |

### Push/Pop semantics

Identical to lazygit (`pkg/gui/context.go:76`):

- **Push side context** → wipe stack, replace.
- **Push main context** → remove other main, keep popups.
- **Push popup** → if top is `TEMPORARY_POPUP`, pop it; else push.
- **Pop** → refuses when stack size == 1.
- **Replace** → swap top without popping (used for tabs within a slot).

### Lifecycle hooks per Context

```go
type IBaseContext interface {
    GetKey() ContextKey
    GetViewName() string
    GetWindowName() string
    GetKind() ContextKind

    // Lifecycle
    HandleFocus(opts OnFocusOpts) error
    HandleFocusLost(opts OnFocusLostOpts) error
    HandleRender() error
    HandleRenderToMain() error // updates the paired main view
    HandleQuit() error          // called on Esc

    // Layout hints
    NeedsRerenderOnHeightChange() bool
    NeedsRerenderOnWidthChange() bool

    // Keybindings (multi-controller composition)
    AddKeybindingsFn(fn KeybindingsFn)
    GetKeybindings(opts KeybindingsOpts) []*ChordBinding
    GetMouseKeybindings(opts KeybindingsOpts) []*gocui.ViewMouseBinding
}
```

`AddKeybindingsFn` lets multiple Controllers attach to the same Context;
`GetKeybindings` iterates the list in reverse so the last-attached wins.
This is exactly lazygit's `base_context.go:121`.

---

## 9. Controllers, Helpers, Actions

### Controller pattern (lifted from lazygit)

A Controller is a thin layer that:

1. Implements `GetKeybindings(opts) []*ChordBinding` returning declarative
   binding data.
2. Holds handlers that **validate preconditions, log, then delegate to a
   Helper**. Controllers do not call the driver directly.

Example sketch (`pkg/gui/controllers/tables_controller.go`):

```go
type TablesController struct {
    baseController
    *ListControllerTrait[*models.Table]
    c *ControllerCommon
}

func (self *TablesController) GetKeybindings(opts KeybindingsOpts) []*ChordBinding {
    return []*ChordBinding{
        {
            Sequence:    Seq("<CR>"),
            Mode:        ModeNormal,
            ActionID:    "table.open",
            Handler:     self.withItem(self.open),
            GetDisabled: self.require(self.singleItemSelected, self.tableNotLocked),
            Description: self.c.Tr.OpenTable,
            ShowInBar:   true,
        },
        {
            Sequence:    Seq("p"),
            Mode:        ModeNormal,
            ActionID:    "table.preview",
            Handler:     self.withItem(self.preview),
            Description: self.c.Tr.PreviewTable,
        },
        {
            Sequence:    Seq("<leader>", "t", "r"),    // chord!
            Mode:        ModeNormal,
            ActionID:    "table.truncate",
            Handler:     self.withItem(self.truncate),
            GetDisabled: self.require(self.canMutateSchema),
            Description: self.c.Tr.TruncateTable,
        },
        {
            Sequence:    Seq("<leader>", "t", "d"),
            Mode:        ModeNormal,
            ActionID:    "table.drop",
            Handler:     self.withItem(self.drop),
            GetDisabled: self.require(self.canMutateSchema),
            Description: self.c.Tr.DropTable,
        },
    }
}

func (self *TablesController) open(t *models.Table) error {
    self.c.LogAction(self.c.Tr.Actions.OpenTable)
    return self.c.Helpers().Tables.OpenForDataEdit(t)
}
```

### Helpers do the multi-step UI flows

```go
// pkg/gui/controllers/helpers/tables_helper.go
func (self *TablesHelper) OpenForDataEdit(t *models.Table) error {
    return self.c.Helpers().Confirm.IfChanged(func() error {
        if err := self.c.Sessions().Current().ResetFilter(); err != nil {
            return err
        }
        self.c.Contexts().TableDataEditor.SetTable(t)
        return self.c.Context().Push(self.c.Contexts().TableDataEditor, OnFocusOpts{})
    })
}
```

Helpers can call into other Helpers and the driver layer (`self.c.Driver()`).
Controllers **cannot** call other Controllers — anything shared moves into a
Helper. This is the layering rule that keeps lazygit's code from collapsing
in on itself and we adopt it verbatim.

### CommandRegistry — the inversion

Lazygit's config schema is action-indexed (`Keybinding.Universal.Quit: "q"`).
Vim users expect key-indexed:

```yaml
nnoremap <leader>q  :quit<CR>
```

dbsavvy adopts the **key-indexed** schema. To make this work, every action
needs a stable string ID, decoupled from its display name. The
`CommandRegistry` (`pkg/gui/keys/command_registry.go`) is the canonical list:

```go
type Command struct {
    ID           string              // "table.drop"
    Description  string              // localized via Tr
    Handler      func() error
    GetDisabled  func() *DisabledReason
    Scope        ContextKey          // where it's available, or 0 for global
    Mode         Mode                // which modes it applies in
    Hidden       bool                // hide from cheatsheet/whichkey
}

type CommandRegistry struct {
    cmds map[string]*Command
    mu   sync.RWMutex
}

func (r *CommandRegistry) Register(c *Command)
func (r *CommandRegistry) Get(id string) *Command
func (r *CommandRegistry) AllForScope(scope ContextKey) []*Command
```

Controllers register their handlers at startup (`Controller.RegisterCommands`)
**before** any keybinding is wired. The keybinding layer then maps keys to
action IDs and looks them up at dispatch time.

This decouples three concerns:

- **What can happen** (CommandRegistry)
- **How it's invoked** (ChordBinding)
- **What it's called** (TranslationSet)

Users can rebind any command without code changes; we can rename
descriptions without breaking configs; we can reuse a single command from
multiple keybindings (e.g. `j` and `<down>` both → `list.next`).

---

## 10. The Keybinding System (vim-style, full spec)

**This is the most significant departure from lazygit.** Lazygit has no
modes, no chord sequences, no leader keys, no timeout disambiguation. We
build all of these.

### 10.0 Rebinding guarantee

> **Every keybinding in dbsavvy is rebindable. No exceptions.**

This is a load-bearing UX promise of the product, not an
implementation detail. The keybinding subsystem is designed around it:

- **Every action** is registered in the `CommandRegistry` (§9) with a
  stable string `action.id` (e.g. `"query.run"`, `"edit.commit"`,
  `"app.quit"`, `"edit.discard_all"`, `"connection.switch"`).
- **No handler is wired by direct function reference.** The dispatcher
  only ever resolves keys through the chord trie (§10.3), which is
  built from the merged default + user config. Removing all bindings
  for an action ID is a supported state.
- **`<nop>` / `<disabled>`** explicitly unbinds a key, including
  defaults the user finds dangerous (e.g. `q` quitting from anywhere).
- **Destructive actions are not special-cased.** `app.quit`,
  `edit.apply`, `edit.discard_all`, `table.drop`, `table.truncate` —
  all are reachable from config and all can be unbound or rebound.
  Their *safety* lives in confirmation dialogs (§12.5, §14), not in
  hardcoded keys.
- **Custom commands** (§14) bind shell commands to keys, so the
  rebindability extends past built-in actions into user-defined ones.
- **No "fallback" hardcoded key path.** If a user unbinds `:` and
  doesn't rebind it, there is no `command.open` action available —
  the cheatsheet (`?`, itself rebindable) will surface the lack.

The one *encouraged* pattern (not a requirement): users are advised to
keep `?` bound to `help.cheatsheet` somewhere reachable. If they unbind
it everywhere, recovery is via editing the config file. This is
documented in the startup tips.

The keybinding linter (run at config load time) warns about — but does
not block — config states that look footgun-shaped:
- No binding for `help.cheatsheet` in any context
- No binding for `app.quit` in any context
- An action that has no binding in *any* context (likely unintentional)

### 10.1 Modes

```go
type Mode uint8

const (
    ModeNormal Mode = 1 << iota
    ModeInsert
    ModeVisual
    ModeVisualLine
    ModeVisualBlock
    ModeOperatorPending
    ModeCommand        // the `:` ex-line
    ModeReplace
)

func (m Mode) Has(other Mode) bool { return m&other != 0 }
```

Modes are a **bitmask** so bindings can be declared for `ModeNormal|ModeVisual`
in one line.

Mode is **per-context**, not global. The QueryEditor context owns a `Mode`
field; the Result grid is always effectively Normal. The current mode is
shown in the status bar (`-- NORMAL --`, `-- INSERT --`).

### 10.2 ChordBinding

```go
type ChordBinding struct {
    Sequence    []Key            // ["<leader>", "f", "p"]  — at least one key
    Mode        Mode             // bitmask
    Scope       ContextKey       // ContextKey("") for global
    ActionID    string           // looked up in CommandRegistry
    Handler     func() error     // pre-resolved closure (CommandRegistry.Get(ActionID).Handler)

    Description string
    Tooltip     string
    Tag         string           // for cheatsheet grouping
    ShowInBar   bool             // show in bottom options bar
    OpensMenu   bool             // hint: pressing this opens a submenu

    GetDisabled func() *DisabledReason
}

type Key struct {
    Code     rune              // for printables; r == 0 for special keys
    Special  SpecialKey        // 0 for printables
    Mod      Modifier          // Ctrl | Alt | Shift | Meta — bitmask
}
```

`Key` is parsed from a string label (`<c-a>`, `<leader>`, `<esc>`, `gg`).
The parser handles:

```
"q"            → {Code: 'q'}
"Q"            → {Code: 'Q'}                        // capital
"<c-a>"        → {Code: 'a', Mod: ModCtrl}
"<c-s-a>"      → {Code: 'A', Mod: ModCtrl|ModShift} // shift folded into rune
"<leader>"     → expanded at registration time to the configured leader
"<localleader>"→ ditto for context-specific leader
"<esc>"        → {Special: KeyEsc}
"<f1>".."<f12>"
"<up>", "<down>", "<left>", "<right>"
"<home>", "<end>", "<pgup>", "<pgdn>"
"<insert>", "<delete>"
"<bs>", "<enter>", "<tab>", "<s-tab>"
"<space>"      → {Code: ' '}
"<bar>"        → {Code: '|'}
"<lt>"         → {Code: '<'}
"<nop>"        → unbind sentinel
"<disabled>"   → alias for <nop>
```

### 10.3 The chord trie

Per `(Mode, Scope)` pair we maintain a trie:

```
                           root
                          / | \
                       g  d   <leader>
                      /  /     /  |  \
                     g  d     f   t   q
                              |   |   .
                              p   r → action: table.truncate
                              .       (described as "Truncate table")
                              .
                            action: schema.find_path
```

Build time: when `KeybindingService.Build()` runs (at startup and after
config reload), it:

1. Builds a fresh `ChordTrie` per `(Mode, Scope)`.
2. For each `ChordBinding`, inserts its `Sequence` into every applicable
   trie (one entry per bit set in `Mode`).
3. Detects collisions: two bindings with the same `Sequence` in the same
   `(Mode, Scope)` → user-facing warning logged, both kept (last-wins on
   dispatch, mirroring lazygit's behavior for compatibility).
4. Detects ambiguous prefixes (e.g. `gg` exists *and* `g` exists as a leaf)
   → also warned. Vim's rule: prefer the leaf with `timeoutlen`.

### 10.4 The matcher state machine

```
                              ┌─────────────────────┐
                              │      IDLE           │
                              │  pending = []       │
                              └─────┬───────────────┘
                                    │ keypress
                                    ▼
                        ┌─────────────────────────┐
                        │  trie.Lookup(pending+k) │
                        └──┬──────────┬───────────┘
            no match       │          │ match
           ┌────────────────┘          └────────────────┐
           ▼                                            ▼
   ┌───────────────┐                              isLeaf?
   │ fall through  │                       ┌──────────┴──────────┐
   │ - to insert?  │                       │ yes                 │ no
   │ - to global?  │                ┌──────▼──────┐      ┌──────▼─────────┐
   │ - swallow?    │                │ hasChildren?│      │   PARTIAL      │
   └───────────────┘                └─────┬───┬───┘      │  pending += k  │
                                          │   │          │  start timer   │
                                          │no │yes       └──────┬─────────┘
                                          ▼   ▼                 │
                                      EXECUTE  PARTIAL           │
                                      → IDLE   pending += k      │ timeout
                                               start timer       ▼
                                                              EXECUTE
                                                              last leaf
                                                              → IDLE
```

State:

```go
type Matcher struct {
    trie     *ChordTrie
    pending  []Key
    lastLeaf *trieNode    // most recent leaf seen while descending
    deadline time.Time
}

type DispatchResult int
const (
    Dispatched     DispatchResult = iota // action fired, state reset
    Pending                              // partial match, awaiting more keys
    FellThrough                          // no match — caller may try insert
    Cancelled                            // user pressed <esc> on partial
)
```

### 10.5 Timeout disambiguation

Two timeouts (vim conventions):

| Setting | Default | Meaning |
|---|---|---|
| `keys.timeoutlen` | 1000 ms | After a partial match with a leaf-ancestor, wait this long for the next key. If none arrives, fire the leaf-ancestor. |
| `keys.ttimeoutlen` | 50 ms | Same but for terminal escape sequences (Esc + ... is sent as `\e...` by terminals). Shorter so `<Esc>` feels instant. |

We model "is this an escape sequence?" by checking if the first key in
`pending` is `<esc>`. If so, use `ttimeoutlen`; else `timeoutlen`.

Implementation: when entering PARTIAL state with `lastLeaf != nil`,
schedule a timer via `time.AfterFunc(timeoutlen, gui.Update(executeTimeout))`.
On any subsequent keypress, cancel the timer.

### 10.6 Dispatch order

For a keypress `k` arriving in current context `C`, current mode `m`:

```
1. matcher[(m, C.Key)].Dispatch(k)             → if Dispatched, done
2. matcher[(m, GLOBAL_CONTEXT)].Dispatch(k)    → if Dispatched, done
3. if m == ModeInsert and C is editable:
       C.View.Editor.Edit(k)                    → done
4. swallow                                       → done
```

Fall-through from context to global is the lazygit pattern (`gocui/gui.go:1546`),
adapted for chords.

### 10.7 Counts and registers

In Normal/Visual modes, a leading digit (1–9) followed by zero or more
digits collects a `count`:

```
5j         → count=5, action=cursor.down
3dw        → count=3, operator=delete, motion=word
"ayy       → register='a', action=line.yank → register
"ap        → register='a', action=paste
```

The matcher's `pending` includes these prefix tokens; on action execution,
the handler receives an `ExecCtx`:

```go
type ExecCtx struct {
    Count    int    // 0 if not specified (handlers default to 1)
    Register rune   // 0 if not specified (handlers default to '"')
    Mode     Mode
    Scope    ContextKey
}
```

### 10.8 Operator-pending mode

After an operator key (`d`, `y`, `c`, `gU`, `gu`), we enter
`ModeOperatorPending` and wait for either a **motion** or a **text object**:

| Sequence | Effect |
|---|---|
| `daw` | delete a word |
| `di"` | delete inside double-quotes |
| `dap` | delete a paragraph (SQL statement, in our case) |
| `yiB` | yank inside `{}` block |

In our world `dap` selects the SQL statement under the cursor (delimited
by `;` or blank line) — extremely useful for the QueryEditor.

Motions and text objects are themselves Commands in the registry (scoped
to `ModeOperatorPending|ModeVisual`):

```go
{ID: "motion.word_next",         Handler: motion.WordNext}
{ID: "motion.line_end",          Handler: motion.LineEnd}
{ID: "textobject.inner_paragraph", Handler: textobject.InnerParagraph}
{ID: "textobject.statement",       Handler: textobject.SqlStatement}
```

The operator command's handler invokes the motion and applies itself to
the resulting range. This is exactly how Helix and Kakoune work.

### 10.9 The which-key popup

When the matcher enters PARTIAL state for ≥ `keys.whichkey_delay` (default
300 ms), a floating popup appears showing the children of the current
trie node:

```
┌─── <leader>t ─────────────────┐
│ r    Truncate table           │
│ d    Drop table               │
│ c    Copy table DDL           │
│ e    Edit table data          │
│ f    Find usages              │
│ x    Cancel                   │
└────────────────────────────────┘
```

Implemented as a `WHICH_KEY` context of kind `DISPLAY_CONTEXT` (the
DISPLAY kind exists exactly so the popup doesn't push/pop focus —
seamless feel). On the next keypress, the popup is dismissed (either
matched, partial-deeper, or fell-through).

### 10.10 Config schema

```yaml
keys:
  leader: "<space>"
  localleader: ","
  timeoutlen: 1000
  ttimeoutlen: 50
  whichkey_delay: 300

  bindings:
    # mode: n|i|v|V|<c-v>|o|x|c — comma-separated, default "n"
    # scope: a context key, or "global", or "all" — default "global"
    # key: a sequence string, like "<leader>tr" or "dap"
    # action: an action ID from the CommandRegistry
    # OR command: a shell command string (custom command shorthand)

    - { mode: n,   scope: tables,         key: "<leader>tr", action: "table.truncate" }
    - { mode: n,   scope: tables,         key: "<leader>td", action: "table.drop" }
    - { mode: n,   scope: query_editor,   key: "<leader>r",  action: "query.run" }
    - { mode: n,   scope: query_editor,   key: "<leader>R",  action: "query.run_to_grid" }
    - { mode: n,   scope: query_editor,   key: "<leader>e",  action: "query.explain" }
    - { mode: n,   scope: query_editor,   key: "<leader>E",  action: "query.explain_analyze" }
    - { mode: n,   scope: result_grid,    key: "yy",         action: "row.yank" }
    - { mode: n,   scope: result_grid,    key: "yc",         action: "cell.yank" }
    - { mode: n,   scope: result_grid,    key: "yC",         action: "column.yank" }
    - { mode: n,   scope: all,            key: "<c-h>",      action: "pane.left" }
    - { mode: n,   scope: all,            key: "<c-j>",      action: "pane.down" }
    - { mode: n,   scope: all,            key: "<c-k>",      action: "pane.up" }
    - { mode: n,   scope: all,            key: "<c-l>",      action: "pane.right" }
    - { mode: "n,v", scope: global,       key: ":",          action: "command.open" }
    - { mode: n,   scope: global,         key: "?",          action: "help.cheatsheet" }
    - { mode: n,   scope: global,         key: "<leader>q",  action: "app.quit" }

    # multi-tab result navigation (§7, decision 1)
    - { mode: n,   scope: result_grid,    key: "<leader>1",  action: "result.jump.1" }
    - { mode: n,   scope: result_grid,    key: "<leader>2",  action: "result.jump.2" }
    # … <leader>3..9 elided
    - { mode: n,   scope: result_grid,    key: "gt",         action: "result.tab.next" }
    - { mode: n,   scope: result_grid,    key: "gT",         action: "result.tab.prev" }
    - { mode: n,   scope: result_grid,    key: "<leader>X",  action: "result.tab.close" }
    - { mode: n,   scope: result_grid,    key: "<leader>=",  action: "result.tab.pin" }

    # FK navigation (§12.6, decisions 19/20)
    - { mode: n,   scope: result_grid,    key: "gd",         action: "row.fk_forward" }
    - { mode: n,   scope: result_grid,    key: "gD",         action: "row.fk_reverse_menu" }

    # in-grid sort/filter/column-hide (§12.3, decisions 16/17/18)
    - { mode: n,   scope: result_grid,    key: "/",          action: "result.filter" }
    - { mode: n,   scope: result_grid,    key: "<leader>s",  action: "result.sort_picker" }
    - { mode: n,   scope: result_grid,    key: "<leader>gH", action: "result.toggle_hidden_columns" }

    # history (§13.6, decision 7)
    - { mode: n,   scope: query_editor,   key: "<leader>h",  action: "history.open" }

    # transaction submenu (§15.10, decision 14)
    - { mode: n,   scope: global,         key: "<leader>t",  action: "tx.menu", opens_menu: true }

    # statement-timeout override (§11.2, decision 23)
    - { mode: n,   scope: global,         key: "<leader>tt", action: "session.statement_timeout" }

    # search_path quick set (§15.9, decision 13)
    - { mode: n,   scope: global,         key: "<leader>p",  action: "session.search_path" }

    # schema hide/show (§15.9, decision 21)
    - { mode: n,   scope: schemas,        key: "H",          action: "schema.hide" }
    - { mode: n,   scope: schemas,        key: "<leader>H",  action: "schema.toggle_show_hidden" }
    - { mode: n,   scope: schemas,        key: "U",          action: "schema.unhide" }

    # result export (§12.7, decision 8)
    - { mode: n,   scope: result_grid,    key: "<leader>oe", action: "result.export_menu" }

    # unbind a default
    - { mode: n,   scope: global,         key: "q",          action: "<nop>" }

    # bind to a shell command directly (shorthand for a Custom Command)
    - { mode: n,   scope: tables,         key: "<leader>tp",
        command: "pg_dump -s -t {{.SelectedTable.QualifiedName}} > /tmp/{{.SelectedTable.Name}}.sql",
        output: popup,
        description: "Dump table schema to /tmp" }
```

Notes:

- The schema is **key-indexed**, not action-indexed. A user can rebind
  literally anything.
- `<nop>` and `<disabled>` are first-class — explicit unbind.
- The `scope: all` shorthand expands at registration time into one binding
  per non-popup context, mirroring vim's `*map`.
- `command:` is sugar for inline custom commands (§14). When present,
  `action:` must be absent.

### 10.11 Discoverability

Three mechanisms:

1. **Options bar** at the bottom of the screen. Shows the 4–8 most-relevant
   bindings for the current context, filtered to those with `ShowInBar:
   true` and `!IsDisabled`. Format: `description: key | description: key`.
   Same as lazygit's `options_map.go`. The bar **always** terminates with
   a `?: more` hint pointing at the cheatsheet, even when all 8 slots are
   in use — so the cheatsheet is one keypress away from anywhere.

2. **Cheatsheet** (`?`). Auto-generated from the chord trie at runtime.
   Grouped by `Tag`, mode-tabbed, with two scope tabs: **current context**
   (default landing) and **global**. Same source of truth as runtime
   dispatch — no duplicate documentation.

   **v1 completeness invariant:** every binding present in the active
   chord trie MUST appear in the cheatsheet for its scope. There is
   **no `Hidden: true` opt-out in v1** — the `ChordBinding.Hidden` field
   is removed (or treated as unused). Custom commands and rebound
   actions are included. A package-level test (`pkg/cheatsheet/...`)
   asserts the invariant by walking every (mode, scope) trie and the
   `CommandRegistry` and verifying every bound entry materializes in
   the rendered cheatsheet.

   **Source markers.** Each cheatsheet row carries a single-glyph
   source tag, with a legend at the top of the popup:

   | Glyph | Meaning |
   |---|---|
   | `·` (dim) | Shipped default binding (unchanged by user) |
   | `✱` (bright) | User-overridden / user-added binding from `config.yml` |
   | `★` (accent) | Custom command (see §14) |

   The trie node tracks the source at build time (see §15.2's overlay
   loading); rendering reads the tag without re-walking config files.

3. **Which-key** (§10.9). The killer feature for chord ergonomics.

### 10.12 Reload-on-save

Identical to lazygit (`gui.go:351-375`): the focus handler stats the
config file; if mtime changed, full reload (`ReloadChangedUserConfigFiles`),
`KeybindingService.Build()` runs again, all tries replaced. Atomic, since
the matcher reads its trie via a pointer swap.

For files edited *outside* the focus boundary (rare in a TUI), the
background refresh ticker checks every 2 s.

### 10.13 What we explicitly drop from vim

To keep the surface honest:

| vim feature | Status |
|---|---|
| `:command` user-defined ex commands | ✅ via CommandRegistry |
| Recording macros (`q{reg}`/`@{reg}`) | ✅ — event-tap on the dispatcher. **Records resolved keystrokes** (post-trie, post-leader-expansion). **Scope-locked**: the recording context is captured at `q{reg}` time and replay errors with `"no binding for X in <scope>"` if invoked from a different context. Config edits after a recording do not retro-break the macro, because the resolved sequence is what's stored. Registers carry the recording's scope tag for the cheatsheet (`@a — recorded in QueryEditor`). |
| Registers | ✅ — `RegisterStore map[rune]string`, 26 named + `"`, `*`, `+` |
| Marks (`m{a-z}`, `'{a-z}`) | ✅ — per-buffer for QueryEditor; per-row for ResultGrid |
| Search (`/`, `?`) | ✅ — in QueryEditor and ResultGrid (the latter searches all loaded rows) |
| `*` / `#` search-word-under-cursor | ✅ in QueryEditor |
| `:s/foo/bar/g` substitute | ✅ in QueryEditor |
| Folds | ❌ v1 — adds complexity to the buffer |
| Visual block (`<c-v>`) | ✅ v1 |
| Buffers and windows (`:buffers`, `:split`) | ❌ v1 — multi-buffer is via the Tabs concept (§7) |
| `.` repeat | ✅ v1 — store last operator+motion |
| Quickfix list | ❌ — `command_log` is the analog |

---

## 11. Driver Layer

### 11.1 The interface

```go
// pkg/drivers/driver.go

type Driver interface {
    // Identity
    Name() string                              // "postgres", "mysql", ...
    Capabilities() Capabilities

    // Lifecycle
    Open(ctx context.Context, profile ConnectionProfile) (Connection, error)
}

type Connection interface {
    Close() error
    Ping(ctx context.Context) error
    ServerVersion() string

    // Sessions (statefully holding tx, search_path, etc.)
    AcquireSession(ctx context.Context) (Session, error)

    // Cancellation must work from a different connection (pg_cancel_backend etc.)
    Cancel(ctx context.Context, queryID QueryID) error
}

type Session interface {
    Close() error
    ID() SessionID

    // Schema introspection
    ListDatabases(ctx context.Context) ([]Database, error)
    ListSchemas(ctx context.Context, db string) ([]Schema, error)
    ListTables(ctx context.Context, schema string) ([]*Table, error)
    ListColumns(ctx context.Context, schema, table string) ([]Column, error)
    ListIndexes(ctx context.Context, schema, table string) ([]Index, error)
    ListConstraints(ctx context.Context, schema, table string) ([]Constraint, error)
    DescribeFunction(ctx context.Context, schema, name string) (FunctionDetail, error)

    // Query execution
    Execute(ctx context.Context, q Query) (Result, error)              // small results
    Stream(ctx context.Context, q Query) (RowStream, error)            // big results
    Explain(ctx context.Context, q Query, analyze bool) (Plan, error)

    // Transactions
    Begin(ctx context.Context, opts TxOptions) (Transaction, error)
    InTransaction() bool
    CurrentTransaction() Transaction
}

type Transaction interface {
    Commit(ctx context.Context) error
    Rollback(ctx context.Context) error
    Savepoint(ctx context.Context, name string) error
    Status() TxStatus
}

type RowStream interface {
    Columns() []ColumnMeta
    Next(ctx context.Context) (Row, bool, error) // (row, hasMore, err)
    Close() error
    QueryID() QueryID                            // for cancel
}

type Capabilities struct {
    HasSchemas         bool
    HasMaterializedViews bool
    HasArrayTypes      bool
    HasJSONTypes       bool
    HasLiveCancel      bool // can cancel a running query via separate connection
    HasExplainAnalyze  bool
    HasNotice          bool // server-side NOTICE/WARNING messages
    HasListenNotify    bool
    SupportsCursor     bool // server-side cursors for huge results
    MaxIdentifierLen   int
}
```

Types with `sync/atomic` fields (`Table.EstimatedRows`, `Table.SizeBytes`)
are passed by pointer everywhere; copying a `Table` would trigger
`go vet copylocks`. Drivers return `[]*Table`.

### 11.2 ConnectionProfile

```yaml
# ~/.config/dbsavvy/connections.yml
connections:
  - name: prod-pg
    driver: postgres
    dsn: "postgres://app@db.prod.internal:5432/app?sslmode=require"
    password_command: "op read 'op://Prod/db/password'"  # 1Password CLI
    role: "app_readonly"                                  # default search role
    ssh_tunnel:
      host: bastion.prod
      user: deploy
      identity_file: ~/.ssh/id_ed25519
    tags: [production, postgres]

    # Visual identity (§11.6)
    color: "#ff4d4d"        # hex, named ("red"), or theme token ("danger")
    label: "PROD"           # short label (≤8 chars) shown in title and dialogs
    icon: "⚠"               # optional single glyph

    # Safety
    read_only: true         # hard-disables all writes at driver level
    confirm_writes: true    # typed connection-name confirmation on edits
    confirm_ddl: true       # ditto for DDL (DROP, ALTER, TRUNCATE)

    # Session defaults
    statement_timeout: "30s"   # SET statement_timeout at session open; 0 = unlimited
    hidden_schemas:            # globs ok; merged with built-in defaults
      - "audit_*"
      - "_partitions_*"

  - name: staging-pg
    driver: postgres
    dsn: "postgres://app@db.staging.internal:5432/app?sslmode=require"
    color: "orange"
    label: "STAGE"
    confirm_writes: true

  - name: local
    driver: postgres
    dsn: "postgres://sav@localhost:5432/dbsavvy_dev?sslmode=disable"
    color: "green"
    label: "LOCAL"
    # read_only / confirm_writes default to false
```

Field semantics:

| Field | Default | Effect |
|---|---|---|
| `color` | `""` (theme accent) | Drives every visual touch-point in §11.6. Any tcell-resolvable color string. |
| `label` | `name[:8]` | Short tag shown alongside the connection in titles and dialogs. |
| `icon` | `""` | Optional single glyph rendered before the label. |
| `read_only` | `false` | If true, sessions open with `default_transaction_read_only = on`; all edit / DDL keybindings are hard-disabled with a `DisabledReason`. |
| `confirm_writes` | `false` | DML commit dialogs (§12.5) require typing the connection's `name` to enable `[a]pply`. |
| `confirm_ddl` | `false` | DDL custom commands and explicit `DROP` / `TRUNCATE` actions require typed confirmation. |
| `statement_timeout` | `"0"` | Postgres-style duration (`"30s"`, `"5min"`, `"0"`). Applied via `SET statement_timeout` at session open. `0` = unlimited. Runtime override per session via `<leader>tt` (persisted to `AppState`, see §16.4). |
| `hidden_schemas` | `[]` | Schema name globs to omit from the `SCHEMAS` rail. Unioned with the built-in defaults (`pg_catalog`, `information_schema`, `pg_toast` for Postgres). Runtime hide/show via `H`, `<leader>H`, `U` (see §15.9). |

`read_only: true` is enforced at the **driver layer**, not the UI
layer. Even a user who manually types `INSERT ...` into the editor
and hits run gets a server-side error (`cannot execute INSERT in a
read-only transaction`). The UI is a polite hint; the driver is the
guarantee.

`password_command` lets the secret live anywhere (keychain, op, bw,
Vault) without dbsavvy carrying credential libraries.

The connection store has full credential helpers (`pkg/session/credentials.go`):
1. Inline `password` (only for dev).
2. `password_command` shell-out.
3. Native keychain via `99designs/keyring` (Linux secret-service, macOS
   keychain, Windows credential manager).
4. `pgpass` file (Postgres-specific).
5. Interactive prompt — last resort.

### 11.3 Postgres driver (v1)

`pkg/drivers/pg/driver.go` implements `Driver` using **pgx v5**
(`jackc/pgx/v5`):

- `pgxpool.Pool` per Connection.
- `pgxpool.Acquire()` per Session — gives us a dedicated `*pgxpool.Conn`
  that owns transaction state.
- Cancellation: `pg_cancel_backend(pid)` from a *separate* connection
  acquired specifically for cancel. We track `(QueryID, BackendPID)` at
  query start.
- Streaming: `Conn.Query(...)`, then `Rows.Next()`. For *huge* results,
  open a server-side cursor (`DECLARE c CURSOR FOR ...; FETCH 1000 FROM c`).

Loader files mirror lazygit's `branch_loader.go`:

```go
// pkg/drivers/pg/tables_loader.go

func (l *TableLoader) Load(
    ctx context.Context,
    schema string,
    oldTables []*models.Table,
    onWorker func(func() error),
    renderFunc func(),
) ([]*models.Table, error) {
    // Cheap query: just name + kind
    rows, err := l.session.Conn().Query(ctx, sqlListTables, schema)
    if err != nil { return nil, err }
    defer rows.Close()

    tables := []*models.Table{}
    for rows.Next() {
        t := &models.Table{}
        if err := rows.Scan(&t.Schema, &t.Name, &t.Kind, &t.Owner); err != nil {
            return nil, err
        }
        // Preserve previously-loaded row counts to avoid flicker
        if old := findTable(oldTables, t.Schema, t.Name); old != nil {
            t.EstimatedRows.Store(old.EstimatedRows.Load())
            t.SizeBytes.Store(old.SizeBytes.Load())
        }
        tables = append(tables, t)
    }

    // Expensive enrichment in background
    onWorker(func() error {
        return l.enrichWithStats(ctx, tables, renderFunc)
    })

    return tables, nil
}

func (l *TableLoader) enrichWithStats(
    ctx context.Context, tables []*models.Table, renderFunc func(),
) error {
    rows, err := l.session.Conn().Query(ctx, sqlTableStats, schemaList(tables))
    if err != nil { return err }
    defer rows.Close()
    for rows.Next() {
        var schema, name string
        var estRows, sizeBytes int64
        if err := rows.Scan(&schema, &name, &estRows, &sizeBytes); err != nil {
            return err
        }
        if t := findTable(tables, schema, name); t != nil {
            t.EstimatedRows.Store(estRows)
            t.SizeBytes.Store(sizeBytes)
        }
    }
    renderFunc()
    return nil
}
```

Embedded SQL files (`//go:embed sql/*.sql`) keep big queries out of Go
strings — easier to copy into psql to debug.

### 11.4 Extensibility (when adding MySQL, SQLite, ...)

Steps:

1. New package `pkg/drivers/mysql/`.
2. Implement `Driver`, `Connection`, `Session`, `RowStream`.
3. Implement loaders for the schema kinds.
4. Register in `pkg/drivers/registry.go`:
   ```go
   drivers.Register("mysql", mysql.New)
   ```
5. Add `Capabilities()` — `HasSchemas: false` flips UI affordances (the
   left-rail collapses two levels into one).

UI code touches `Capabilities()` *only*, never imports a specific driver
package. This is the layering rule that makes the driver plug-in story
real, not aspirational.

### 11.5 Query errors

```go
type QueryError struct {
    Raw      error
    Code     string  // SQLSTATE
    Position int     // 1-based offset into the SQL, or -1
    Severity string  // ERROR, WARNING, NOTICE
    Hint     string
    Detail   string
    Where    string
    Schema   string
    Table    string
    Column   string
    Constraint string
}
```

Postgres errors via `pgconn.PgError` map directly. The QueryEditor
underlines the bad token at `Position` and shows `Hint` in a tooltip.

### 11.6 Connection visual identity

The `color`, `label`, and `icon` fields (§11.2) drive **five visual
touch-points**, all designed to make "which database am I editing?"
impossible to misread.

1. **Title bar** (top frame of the screen).
   ```
   ┌─⚠ PROD · prod-pg ─────────────────────────────────────────────┐
   ```
   The whole top frame is rendered in `color`. The label and icon
   appear immediately after the corner glyph.

2. **Left-rail header band** (above the schemas list).
   ```
   ┌──────────────────┐
   │ ⚠ PROD          │   ← background tinted in `color`
   ├──────────────────┤
   │ Schemas       (1)│
   ...
   ```

3. **Status bar** anchors the label at the right edge.
   ```
   [n]ew  [r]un  [c]ancel  ...        [3 pending] ⚠ PROD
   ```
   In `read_only` connections the suffix becomes `[RO] ⚠ PROD`.

4. **Every confirmation dialog** (commit, DROP, TRUNCATE, disconnect)
   is bordered in the connection's `color` and prefixed with the
   `icon label`. This is the most important touchpoint — the user
   physically cannot dismiss a destructive prompt without seeing it.

5. **Connection picker** (the `CONNECTIONS` side context) renders
   each entry with its color swatch and label, sorted so non-prod
   appears first by default (`tags: [production]` sinks).

The colors are tcell `tcell.Color`s, resolved through:

```
color: "#ff4d4d"   → tcell.NewHexColor(0xff4d4d)
color: "red"       → tcell.ColorRed
color: "danger"    → theme.Palette["danger"]   // theme token, §16.1
```

This last form (theme tokens) is the recommended one for shareable
configs: a team can ship a `connections.yml` referencing `danger`,
`warning`, `safe` and the actual colors track the user's theme.

#### Color & accessibility

- All colors must pass a contrast check against the default
  background for the label text to remain legible. If contrast fails,
  the label is rendered with an inverted background (the color
  becomes the background, text becomes light or dark to maintain
  contrast). This is checked at config-load time and once per theme
  change.
- A `colorblind_safe: true` top-level config flag remaps the connection
  color palette to an Okabe–Ito-friendly set; labels and icons remain,
  so identity stays distinguishable even in monochrome terminals.
- If the terminal doesn't support 24-bit color, hex colors are
  quantized to the nearest 256-color palette entry. The label and
  icon carry the identity load when color fails entirely.

---

## 12. Async Query Execution & Result Rendering

### 12.1 ResultBufferManager — extending lazygit's tasks

lazygit's `tasks.go` is the closest analog to what we need. We port the
file verbatim, with these changes:

1. The reader is no longer `bufio.Scanner` over a process stdout — it's
   the driver's `RowStream.Next()`.
2. `LinesToRead{Total, InitialRefreshAfter, Then}` → `RowsToRead{Total,
   InitialRefreshAfter, Then}`. Same shape.
3. Throttle / preempt / `taskKey` / `stopCurrentTask` — all kept.
4. Output target is no longer `io.Writer` to a gocui View. It's
   `GridView.AppendRows([]Row)` — the GridView owns its own backing
   buffer.

Public surface:

```go
type ResultBufferManager struct {
    // ... lazygit-pattern fields ...
    rowsToRead chan RowsToRead
    target     *grid.GridView
}

func (m *ResultBufferManager) NewQueryTask(
    streamFn func(ctx context.Context) (drivers.RowStream, error),
    initialRows int,
    onDone func(),
) error

func (m *ResultBufferManager) ReadRows(n int)        // pull n more
func (m *ResultBufferManager) ReadToEnd(then func()) // pull all remaining
```

### 12.2 The query lifecycle

```
User in QueryEditor presses <leader>r (Normal) or selects a range and <leader>r (Visual)
  → QueryEditorController.run()
     → QueryHelper.RunCurrentStatementsOrSelection()
        → split source range on `;`:
            Normal mode  → range = statement-under-cursor (text-object §10.8)
            Visual mode  → range = selection; split into N statements
            <leader>R    → range = whole buffer; split into N statements
        → for each statement, allocate a new result tab in the secondary slot
          (see §7's multi-result-tab model):
            - tab #i becomes active immediately so the user watches rows stream in
            - tab title shows "result <i>: <first-40-chars-of-stmt>…"
            - each tab owns its own ResultBufferManager + GridView instance
        → run statements sequentially on the same Session:
            - if user is in a manual tx (§15.10), runs inside that tx;
              a mid-batch error blocks subsequent statements and the tx remains
              open for explicit rollback
            - otherwise each statement is autocommitted on its own
            - resultBufMgr.NewQueryTask(streamFn, initialRows=200, onDone)
              for the active tab; later tabs queue until prior completes
           - reads up to InitialRefreshAfter rows synchronously
           - first paint → user sees rows
           - listens on rowsToRead chan for further pulls
```

**Concurrency invariant (v1).** At most **one in-flight query per
connection**, period. Multi-statement runs serialize on the same
session. Switching panes or contexts away from a running result
*preempts* it (see below); the result tab stays open, marked
`(cancelled, N rows)` and may be re-run with `<leader>r` from inside
the tab. Cross-connection concurrent queries are deferred along with
multi-connection support (Phase 8).

On context switch (user moves to another table), the manager's stop
channel closes:
- The active driver session calls `Cancel(queryID)` (server-side cancel).
- The `RowStream.Close()` releases any cursor.
- The `gocui.Task` is `Done()`d so the spinner stops.

This preempt-on-key pattern is exactly lazygit's "scroll commits → preempt
git show" and exactly what every shipped DB GUI gets wrong.

### 12.3 The GridView

A custom gocui-compatible view (`pkg/gui/grid/view.go`). Implements
`Layout(g *gocui.Gui)` via composition — gocui still owns the rectangle,
but we don't write to the line buffer. We render cells directly via
tcell-level escape sequences inside the view's draw method.

The GridView owns one logical row buffer (filled by `ResultBufferManager`)
and renders it through one of two view modes. The mode is a render-time
concern only — it does not touch the buffer, the cursor model, or
cancellation.

#### View modes (grid / expanded)

| Mode | Description | Best for |
|---|---|---|
| `grid` (default) | 2D table — one row per terminal line, columns side-by-side | Narrow tables, scanning many rows |
| `expanded` | One record per "page", `column: value` pairs stacked vertically (psql `\x`) | Wide tables (≥ ~6 columns), JSON-heavy rows, single-record inspection |

Toggle with `<leader>gx` (mnemonic: grid eXpanded). The mode is a
**global session preference**: once toggled it persists across queries
and is round-tripped through `AppState.LastResultViewMode` (§16.4), the
same way psql treats `\x`. No per-query override in v1.

Expanded mode layout (psql `\x` style):

```
-[ RECORD 3 of ~12,540 ]------------------------------------------------
id          | 1235
email       | charlotte.delacroix@example.io
name        | Charlotte Delacroix
created_at  | 2026-05-17 09:58:11.224+00
metadata    | {"plan":"pro","trial_ends":"2026-06-01","flags":["beta",
            |  "early_access"],"referrer":"hn"}
deleted_at  | NULL
-[ RECORD 4 of ~12,540 ]------------------------------------------------
...
```

Layout rules:

- The column-name gutter is sized to `max(len(col_name))` across the
  result set, clamped to `[12, 32]`.
- Long values wrap at the available width and re-indent to the value
  column (left-padded to the `|` separator) — never to the gutter.
- The `-[ RECORD n of ~total ]-` separator uses the view width and the
  same `~` semantics as the grid title (`EstimatedRows.Load()`).
- The renderer is streaming-safe: it only formats records visible in
  the viewport plus a one-record overscan, so a 10M-row result is cheap.

#### Navigation

| Key | `grid` mode | `expanded` mode |
|---|---|---|
| `j` / `k` | Down/up one row | Next/previous **record** |
| `J` / `K` | (unbound) | Next/previous wrapped line (linear, for selection) |
| `h` / `l` | Left/right one cell | Scroll viewport left/right (long values) |
| `<C-d>` / `<C-u>` | Half-page rows | Half-page within current record (or onto next if at end) |
| `gg` / `G` | First / last row | First / last record |
| `<CR>` on JSON cell | Open JSON popup | Same |
| `<leader>gf` | Toggle frozen first column | (no-op — column is always frozen in expanded) |

#### Selection & yank

The selection model is cell-coordinate-based `(row, col)` in both modes
— expanded is just a different layout of the same cells.

- `v` — visual cell (start a selection inside a single value)
- `V` — visual record (whole row's worth of cells)
- `<C-v>` — rectangular block (grid mode only; falls back to `V` in
  expanded mode, since there is no column axis to span)
- `y` — yank. TSV in grid mode; `col\tvalue\n` lines in expanded mode
  (handy for pasting a single record into chat / a ticket).

#### Other features (mode-independent)

- **Column auto-sizing** (grid mode) with min/max bounds. Initial widths
  from the first 100 rows; later rows may exceed and get truncated with `…`.
- **Frozen first column** (grid mode, toggle with `<leader>gf`).
- **Column hide/show** (decision 18). `<leader>gH` toggles a hidden
  overlay UI: hidden columns appear in a dim "hidden:" footer; `j`/`k`
  in the overlay navigates, `<space>` toggles inclusion, `<esc>` closes.
  Hidden-column sets are persisted per `(connection_id, base_table_id_or_query_fingerprint)`
  in `AppState.HiddenColumns` (§16.4). Reorder is deferred to v2.
- **NULL rendering** — italic dim `NULL`.
- **JSON expansion** — `<CR>` on a JSON/JSONB cell opens a popup with
  formatted JSON.
- **BLOB rendering** — hex preview with size annotation.
- **Type-aware coloring** — keywords (theme.SqlKeywordFg), numbers
  (theme.NumericFg), nulls (theme.NullFg), etc.
- **Pagination indicator** in the title: `(rows 1–200 of ~12,540, +N loading…)`
  in grid mode, `(record 3 of ~12,540)` in expanded mode. The `~` indicates
  we asked for `EstimatedRows.Load()`, not exact count.

#### Pagination (decision 4)

The `ResultBufferManager` (§12.1) drives row pulls. The grid pulls
**automatically** when the cursor moves within `ui.result_prefetch_rows`
(default 50) of the loaded tail, calling `ReadRows(ui.result_page_size)`
(default 200). This gives the feel of a tail-streaming editor without
the user having to think about paging.

| Key | Effect |
|---|---|
| `j` / `k`, `<C-d>` / `<C-u>`, `<C-f>` / `<C-b>` | Move cursor; auto-pulls if within `prefetch_rows` of tail |
| `G` | `ReadToEnd` then jump to last row (status bar warns on >1M-row result and offers `<esc>` to cancel) |
| `gg` | First row (no pull) |
| `]p` / `[p` | Manual next-page / re-anchor at current top |

Once `ReadToEnd` completes, the `~` prefix in the row count is
dropped (we now know the exact total). The result tab title gains a
`(complete)` suffix.

#### In-grid sort and filter (decisions 16 + 17)

Both operate over **loaded rows only**. The status bar surfaces the
partial-load caveat the first time either is invoked on an incomplete
result: `"Sorting/filtering loaded rows only — press G to load all
then re-sort."`

| Key | Effect |
|---|---|
| `/regex<CR>` | Filter loaded rows by regex against the cursor's column (with `<C-a>` to toggle "all columns" mode); `n` / `N` jump between matches; `<esc>` clears the filter. |
| `<leader>s` then `j` / `k` to pick a column and `<CR>` | Sort loaded rows by the chosen column ascending; second invocation on the same column toggles descending; third clears the sort. |
| Double-click column header (mouse, §6.1) | Same as the sort key above. |

Both states **reset on next query**; they are intentionally ephemeral
to avoid implying server-side semantics.

### 12.4 Result identity & cancellation

```go
type QueryID struct {
    SessionID SessionID
    BackendPID int32   // for pg
    Started   time.Time
    Nonce     uint64
}
```

The query helper keeps a `CancelRegistry`:

```go
type CancelRegistry struct {
    inflight map[SessionID]QueryID
    mu       sync.Mutex
}
```

On preempt, `CancelRegistry.Cancel(sessionID)` looks up the in-flight
query and dispatches `Driver.Cancel(ctx, queryID)` — which uses a
separate connection to issue `pg_cancel_backend(pid)`.

Why a separate connection: the running session is busy and won't honor
queries until it returns. pgx supports this; we just need a one-shot
pool that mints connections on demand.

**Drivers without `HasLiveCancel`** (e.g. SQLite — there is no
analog to `pg_cancel_backend`). The `<leader>x` binding is
**hard-disabled** with `DisabledReason: "driver does not support live
cancel; press <esc> to detach from the result"`. The options bar
surfaces this state. Pressing `<esc>` calls `RowStream.Close()`
client-side: any rows already buffered remain visible, the result
tab is marked `(detached, server still running)`, and the underlying
server query is allowed to complete on its own time. This is the
"Conformity with the DBMS" principle (§1) — we do not lie about what
the driver can do.

### 12.5 Inline editing

The result grid is editable when the result is **derivable from a single
base table with row identity**. When editable, users can mutate cells
in place using a modal vim-style flow; pending edits accumulate across
multiple rows and commit atomically in one transaction after explicit
confirmation. When not editable, edit keybindings are hard-disabled
with a `DisabledReason` exposed in the options bar.

#### 12.5.1 Editability rules (Postgres v1)

A result is editable iff ALL hold:

- Every column in the SELECT list traces back to **the same base relation**
  — no joins, aggregates, window functions, or computed expressions
  (bare column references and simple casts are fine).
- The base relation is a **regular table** (not a view, materialized
  view, or foreign table without UPDATE support).
- **All columns of one of the table's UNIQUE / PRIMARY KEY constraints**
  appear in the SELECT, so each row has a recoverable identity.
- The connection's `read_only` flag (§11.2) is false.
- The driver's `Capabilities().SupportsInlineEdit` is true.

Detection runs once at first paint, using the prepared statement's
row description: `pg_attribute.attrelid` exposes the base relation per
column, and `pg_index` exposes the candidate keys. The GridView sets
`Editable bool` and `RowIdentity []ColumnIndex` accordingly. The
options bar reflects the result: `[i] edit cell` or
`[i] edit cell — disabled: result spans multiple tables`.

#### 12.5.2 The edit lifecycle

```
[cursor on row 3, column "email"]

i            → enter cell-edit (modal). The cell becomes a single-line
               mini-buffer pre-filled with the current value, cursor at
               end. Status bar:
                 -- INSERT (cell) -- email: text NOT NULL
[typing]     → mutates only the local mini-buffer
<Esc>        → exit cell-edit, record pending edit in PendingEditSet.
               Cell is now "dirty": tinted with theme.DirtyCellBg and
               trailing `●`. Original value retained for optimistic
               check at commit.
<C-c>        → exit without recording; if cell was already dirty,
               discard the pending edit (returns to clean).

[move to row 7, column "deleted_at", edit it, <Esc>]
[move back to row 3, edit "metadata", <Esc>]
   → PendingEditSet now holds 3 edits across 2 rows.

:w           OR   <leader>cw      → open the commit dialog (§12.5.4)
<leader>cu                        → discard the pending edit at cursor
<leader>cU                        → discard all pending edits
                                    (confirmation popup if > 5)
:q with unsaved edits             → blocked. Message:
                                    "3 pending edits. :w to commit,
                                    :q! to discard, <leader>cU to
                                    discard interactively."
```

The cell-edit mini-buffer is a tiny instance of the same buffer machinery
as the SQL editor (§13.1), running in `ModeInsert`. It inherits motion
and editing keybindings (the rebindable ones — §10.0), and `<C-f>`
opens the full multi-line popup for long values, JSON, etc. (same popup
as `<CR>`-on-JSON for viewing).

#### 12.5.3 Per-type entry helpers

| Type | Entry behavior |
|---|---|
| text / varchar | Plain text input. |
| numeric / int / float | Plain input; rejected at commit time with type error if not parseable. |
| boolean | `i` opens a 3-option chooser (`true`, `false`, `NULL`); typing also works. |
| timestamp / timestamptz / date | Pre-filled in ISO 8601; `<C-d>` inserts `now()`, `<C-t>` inserts today. |
| json / jsonb | Opens the full multi-line popup with JSON syntax highlighting and live-parse validation. |
| enum | `i` opens a chooser populated from `pg_enum` (cached per-type). |
| array | Full popup; comma- or bracket-delimited entry; pgx round-trips. |
| bytea | Hex entry (`\x...`); paste-from-file via `<C-r>f`. Not allowed for binaries >1 MiB. |
| (any) | `<leader>cn` sets the pending value to `NULL` from any clean or dirty cell. |

Type validation happens at two points: **on `<Esc>` exit** (best-effort
parse — if it clearly fails, we keep the cell in edit mode and surface
an inline error) and **at commit** (authoritative — server-side type
errors abort the transaction with a clear error).

##### Server-side expressions

Some entries are not literal values but **SQL expressions** that must
be evaluated by the server at commit time (`now()`,
`current_timestamp`, `gen_random_uuid()`, `nextval('seq')`, …).
Storing `now()` as a stale client-side literal would defeat the point.

A pending edit therefore carries a `Kind` discriminator:

```go
type PendingEdit struct {
    // ...
    Kind     EditKind     // Literal | Expression
    NewValue driver.Value // for Literal
    NewExpr  string       // for Expression — raw SQL, never user-facing
}

type EditKind uint8
const (
    Literal EditKind = iota
    Expression
)
```

Rules for an edit being treated as an Expression:

- Inserted via a known keybinding (`<C-d>` → `now()`, `<C-t>` → `current_date`).
- The user explicitly opted in with `<leader>ce` from a clean or dirty
  cell — opens a small "SQL expression" prompt (highlighted in the
  warning theme), where typed input is taken verbatim as SQL.

The commit dialog renders expressions inline-unquoted so they're
distinguishable from literals:

```
row id=1147
  last_seen_at  2026-05-12 09:00:00+00
             →  now()          (SQL expression)
```

The generated SQL injects them directly:

```sql
UPDATE public.users
   SET last_seen_at = now()         -- NOT $1
 WHERE id = $1
   AND last_seen_at IS NOT DISTINCT FROM $2;
```

Because expressions are not parameterized, the **expression input path
is the one place inline editing can issue free-form SQL fragments**.
This is gated by:

- Dedicated keybindings (`<C-d>`, `<C-t>`) inject from a hard-coded
  allowlist — no user SQL involved.
- `<leader>ce` requires a confirmation step (the warning-themed prompt
  is the confirmation) and is disabled entirely on `read_only`
  connections (where expressions cannot mutate anyway) and visually
  marked on `confirm_writes` connections.
- Expression edits show their `Kind` in every visual indicator and
  in the commit dialog so a reviewer can spot them in the diff.

#### 12.5.4 PendingEditSet

```go
type PendingEdit struct {
    PrimaryKey []driver.Value   // tuple matching the table's PK columns
    Column     string
    OldValue   driver.Value     // value at row-load time (optimistic check)
    NewValue   driver.Value
    LoadedAt   time.Time
}

type PendingEditSet struct {
    Table TableRef
    Edits []PendingEdit         // append-only;
                                // later edit on same (pk,col) replaces in place
}
```

A GridView holds **at most one** `PendingEditSet`. Switching the result
to a different table (running a new query, jumping to a different table
via the schema rail) prompts:

```
Discard 3 pending edits to public.users? [y/N]
```

— pending edits never silently survive a context change.

#### 12.5.5 Commit confirmation

`:w` / `<leader>cw` opens the **commit dialog**. The dialog is bordered
in the connection's `color` and prefixed with `icon label` (§11.6) — so
you cannot apply changes without seeing which database you're hitting.

```
┌─⚠ PROD · Commit 3 changes to public.users ─────────────────────┐
│                                                                │
│ 2 rows affected. Will execute atomically.                      │
│                                                                │
│  row id=1235                                                   │
│    email     'charlotte.delacroix@example.io'                  │
│           →  'charlotte.d@example.io'                          │
│    metadata  {"plan":"pro", ...}                               │
│           →  {"plan":"enterprise", ...}                        │
│                                                                │
│  row id=1234                                                   │
│    deleted_at  NULL                                            │
│             →  '2026-05-17 12:00:00+00'                        │
│                                                                │
│ [s] show generated SQL    [d] dry-run (rollback after)         │
│                                                                │
│ This connection requires typed confirmation.                   │
│ Type the connection name to apply:  _______                    │
│                                                                │
│   [a] apply    [Esc / c] cancel                                │
└────────────────────────────────────────────────────────────────┘
```

Confirmation tiers, derived from `ConnectionProfile`:

| Connection flag | Apply gate |
|---|---|
| `read_only: true` | Dialog never opens; edit keybindings hard-disabled. |
| `confirm_writes: true` | User must type the connection's `name` to enable `[a]`. The expected text is shown to remove guesswork. |
| (default) | `[a]` is enabled immediately; single keystroke applies. |

**Confirmation memory: none** (decision 24). The typed-name
confirmation on `confirm_writes` connections is **per-dialog**, never
session-cached. There is no "remember for N minutes" or "unlock until
disconnect" flag. The friction is the point on flagged connections;
attempting to add a memory escape hatch creates a footgun where users
flip it once "to get unblocked" and lose protection. The same rule
applies to `confirm_ddl` dialogs.

`[s] show generated SQL` flips the dialog into SQL preview mode:

```sql
BEGIN;

UPDATE public.users
   SET email = $1
 WHERE id = $2
   AND email IS NOT DISTINCT FROM $3;
-- $1='charlotte.d@example.io', $2=1235,
-- $3='charlotte.delacroix@example.io'

UPDATE public.users
   SET metadata = $1
 WHERE id = $2
   AND metadata IS NOT DISTINCT FROM $3;
-- ...

UPDATE public.users
   SET deleted_at = $1
 WHERE id = $2
   AND deleted_at IS NOT DISTINCT FROM $3;
-- ...

COMMIT;
```

Each cell change is its own `UPDATE` statement (one column per
`UPDATE`) so the optimistic predicate is **per-column** — a concurrent
edit to a *different* column of the same row will not falsely conflict
with this change. All statements run inside a single transaction, so
the batch is still all-or-nothing.

`IS NOT DISTINCT FROM` (rather than `=`) handles `NULL` transitions
correctly: `NULL → x` and `x → NULL` updates will still match their
load-time values.

`[d] dry-run` wraps the batch in `BEGIN; …; ROLLBACK;` and reports
rows-affected per statement without committing. Useful for verifying
parse, constraints, and conflict counts before applying for real.

#### 12.5.6 Apply path

```
[a] / typed confirmation passes
  → driver.Session.Begin()
  → for each PendingEdit:
       res ← UPDATE table SET col = $new
             WHERE pk_tuple = ($pk...)
               AND col IS NOT DISTINCT FROM $old
       if res.RowsAffected() == 0:
         collect as ConflictedEdit
  → if any conflicts:
       Session.Rollback()
       open conflict dialog (§12.5.7)
  → else:
       Session.Commit()
       PendingEditSet.Clear()
       re-fetch affected rows by PK to refresh display
       options bar: "✓ 3 changes applied"
       command_log: SQL + duration + rows-affected per statement
```

Everything happens on a dedicated session (`AcquireSession`) so the
user's main editor session (which may hold a manual `BEGIN`) is
unaffected.

#### 12.5.7 Conflict dialog

If any `UPDATE` matched zero rows, **none** of the changes commit. The
dialog surfaces what's stale and what to do:

```
┌─Conflicts detected ─ no changes applied ──────────────────────┐
│                                                               │
│ 1 of 3 changes conflict with newer values on the server:      │
│                                                               │
│  row id=1235  email                                           │
│    your edit:  charlotte.d@example.io                         │
│    server now: c.delacroix@example.io  (loaded 10:14:02,      │
│                                          changed since)       │
│                                                               │
│ Other 2 changes are unaffected (also rolled back).            │
│                                                               │
│  [r] refresh stale cells, keep non-conflicting edits          │
│  [o] overwrite — re-apply ignoring conflict on this cell      │
│  [Esc] cancel — all pending edits retained                    │
└───────────────────────────────────────────────────────────────┘
```

- `[r]` — re-fetch the conflicted rows by PK, drop the pending edits
  that were stale, retain the rest. User can re-run `:w` to apply
  what's left.
- `[o]` — re-apply each conflicted UPDATE with a PK-only predicate
  (last-write-wins). **Explicit, deliberate, only reachable here.**
  Hidden entirely on `confirm_writes: true` connections — overwriting
  a conflict on a flagged connection requires going through `[r]`,
  examining the values, and re-editing.
- `[Esc]` — bail; nothing changed on the server; nothing changed in
  the PendingEditSet either.

#### 12.5.8 Visual indicators

- **Clean cell** — theme default.
- **Dirty cell** — `theme.DirtyCellBg` tint, trailing `●`.
- **Dirty row** — row gutter shows `M` (modified), like vim's
  `:set list` markers.
- **Pending count** in the status bar: `[3 pending]`. Tinted in
  `connection.color` if the connection is `confirm_writes` or has any
  destructive flag set.
- **Commit dialog** — bordered in `connection.color`, prefixed with
  `icon label`.
- **Read-only connection** — the `i` keybinding's options-bar entry
  reads `[i] edit cell — disabled: read-only connection`. Cells are
  still selectable / yankable.

#### 12.5.9 Mode interaction

Inline editing works identically in **grid** and **expanded** view modes
(§12.3). The PendingEditSet is layout-agnostic; the commit dialog renders
the same row diffs regardless of which mode produced them. This means
the natural workflow is:

1. Run a wide-table query, end up in expanded mode automatically (or
   by toggle).
2. Step through records with `j` / `k`, edit individual fields with
   `i` / `<Esc>` as you go.
3. `:w` to commit the batch.

— which is the psql `\x` + `\e` workflow that doesn't exist in any
existing terminal client.

#### 12.5.10 Out of scope for v1

- **INSERT** new rows from the grid — needs an "empty row" affordance
  and a separate dialog. Deferred to v2.
- **DELETE** rows from the grid — better as an explicit `dd` keybinding
  with its own confirmation dialog; deferred to v2.
- **JSON sub-path edits** — edits always replace the whole JSON value
  in v1. Future: `<leader>cj` to edit at a JSONPath.
- **Optimistic predicate skipping** — no flag to disable the
  per-column check. If you want last-write-wins, you go through the
  conflict dialog's `[o]` path.
- **Cross-table batches** — a single PendingEditSet maps to one table.
  Mixing tables means commit-or-discard between them.

### 12.6 Foreign-key navigation (decisions 19 + 20)

Foreign keys are surfaced by the driver via `ListConstraints` (§11.1),
cached per `(connection, schema, table)` and refreshed when the
schema-rail refreshes. The grid annotates FK columns with a small
arrow glyph in the header (`→`) so the user knows `gd` will work
there.

**Forward `gd`** — cursor on a value in a column that is part of a
foreign-key constraint:

- Resolves the parent table from the constraint metadata.
- Builds `SELECT * FROM <parent> WHERE <pk_cols> = <fk_values>` using
  parameterized values (no string concatenation).
- Opens a new result tab in the secondary slot (§7 multi-tab model)
  with title `→ <parent>(<pk_value>)`.
- Pushes a jumplist entry so `<C-o>` returns to the originating cell
  in the originating tab. `<C-i>` re-forwards.

Composite-FK requirement: **all** FK columns must be present in the
current SELECT result. If any are missing, `gd` is disabled with
`DisabledReason: "FK '<name>' requires columns <missing> in the
result"`. The user can re-run with the missing columns added; the
jumplist entry is preserved across the re-run.

**Reverse `gD`** — cursor on a PK (or unique-key) value, find all
inbound foreign keys:

- Driver query enumerates `pg_constraint` (Postgres) for `FOREIGN KEY`
  constraints whose `confrelid` is the current table.
- Result is rendered as a **picker menu** (TEMPORARY_POPUP): one
  entry per referencing table, with an estimated count from
  `pg_class.reltuples` filtered by an `EXISTS`-pattern (cheap upper
  bound — exact count is computed only if the user opens the tab).
- Selecting an entry runs
  `SELECT * FROM <ref_table> WHERE <ref_cols> = <pk_values>` and
  opens a new result tab, same flow as forward `gd`.

This avoids the "tab explosion" anti-pattern where a popular PK
spawns 20 result tabs.

### 12.7 Result export (decision 8)

`<leader>oe` opens an export menu from any result tab. The menu has
three orthogonal selectors:

| Selector | Options |
|---|---|
| **Format** | CSV / TSV / JSON (array) / NDJSON / SQL INSERTs / Markdown table |
| **Destination** | Clipboard / File path (prompt) / `stdout` (piped via the same shell-out pipe custom commands use, §14) |
| **Scope** | Visible (current viewport) / Loaded (everything in buffer) / Full (calls `ReadToEnd` first; shows progress and is cancellable) |

Implementation notes:

- The export writer is streaming for CSV / TSV / NDJSON / SQL INSERTs
  (incremental write while reading rows). JSON-array and Markdown
  buffer the whole set in memory; for `Full` scope on these formats,
  the menu warns when the estimated count exceeds
  `export.buffered_row_warn_threshold` (default 100k).
- SQL INSERTs require the editability metadata from §12.5.1 (the
  result must trace to a single base table with row identity). If
  that condition isn't met, the "SQL INSERTs" format option is
  disabled with the same `DisabledReason` strings used for inline
  edit.
- Default file naming: `<connection>_<table-or-fingerprint>_<utc-ts>.<ext>`
  in `~/Downloads` (or `$XDG_DOWNLOAD_DIR`).
- Quoting: CSV/TSV use RFC 4180; SQL INSERTs use the driver's
  literal-encoding helper (pgx `Encoding`); NDJSON uses `json.Encoder`
  with `SetEscapeHTML(false)`.

### 12.8 EXPLAIN plan rendering (decision 9)

The `PLAN` context renders the parsed plan from
`Driver.Explain(ctx, q, analyze)` as an **indented tree**, not raw
text. Each row is one plan node.

Layout sketch (ANALYZE mode):

```
┌─Plan · EXPLAIN ANALYZE · 1.214 s ─────────────────────────────────┐
│  ▼ Index Scan  using idx_users_created  on public.users          │
│       cost: 0.42..4815.84   rows: 100      time: 0.012..0.084    │
│       loops: 1   actual_rows: 100                                │
│    ├─ ▼ Hash Right Join                                          │
│    │       cost: 12.14..480.91   rows: 87   time: 0.412..3.221   │
│    │       loops: 1   actual_rows: 87                            │
│    │       hash_cond: (u.org_id = o.id)                          │
│    │    ├── Seq Scan  on public.orgs o                           │
│    │    │       cost: 0..18.20   rows: 820   time: 0.004..0.190  │
│    │    └── Hash                                                 │
│    │           ▼ Seq Scan  on public.users u                     │
│    │               cost: 0..341.04   rows: 12340  time: 0.001..1.8│
│    └── (more)                                                     │
└──────────────────────────────────────────────────────────────────┘
```

Render rules:

- Tree glyphs from the existing `tree_view_model` (§4 `presentation/`).
- `<CR>` collapses / expands the cursor's subtree (`▼` / `▶`).
- `<C-a>` expands all; `<C-x>` collapses all but the root.
- `o` toggles raw `EXPLAIN (FORMAT TEXT)` output in the same view
  (round-tripped from a `RawText` field on the parsed plan).
- Columns shown depend on `analyze`: estimate columns always; actual
  columns only when analyzed.
- **Cost coloring.** Each node's `total_cost` is bucketed into
  per-plan percentiles (P50/P75/P90/P95) and colored from neutral to
  the theme's warning fg. Color is on the cost column only; the rest
  of the row stays default. Disabled when terminal is monochrome.
- `H` jumps to the heaviest child of the cursor's subtree (lazygit's
  "go to next big thing" pattern).
- Plan model: `pkg/models/plan.go` already exists (§4). Postgres
  parsing uses `EXPLAIN (FORMAT JSON)` and reshapes into the model
  inside `pkg/drivers/pg/plan.go`.

### 12.9 Server NOTICE / WARNING streaming (decision 5)

Postgres can emit `NOTICE`, `WARNING`, and `INFO` messages
(`RAISE NOTICE` inside a function, `SET ... already set`, etc.).
The `HasNotice` capability is declared in §11.1; v1 surfaces them
as follows:

- Every notice is appended to the `command_log` (§4 `command_log_panel.go`,
  the `extras` context) with severity prefix and the active
  connection's `icon label`:
  `[NOTICE] · ⚠ PROD · function "f"(int) is replaced ...`.
- The **first** WARNING or NOTICE during a single `<leader>r` /
  `<leader>R` run raises a toast (`"server emitted N notice(s) —
  <leader>l to view"`) so the user doesn't miss it. Subsequent
  notices in the same run only update the in-line counter on the
  toast (no toast-spam).
- The **commit dialog** (§12.5.5) gains a "notices" footer that
  echoes any NOTICEs accumulated during the dry-run or apply. A
  `WARNING` in the apply path is treated as a soft failure: the
  transaction is committed (Postgres semantics) but the dialog
  closes into a "review notices before continuing" state.
- DDL run via custom command (`output: log`, §14) routes notices
  the same way: `command_log` is the canonical sink.

---

## 13. The SQL Editor

A multi-line buffer with vim-style editing, syntax highlighting, and
context-aware completion. The most ambitious piece outside the keybinding
system.

### 13.1 Buffer model

```go
// pkg/gui/editor/buffer.go

type Buffer struct {
    Lines    []Line
    Cursor   Position
    Marks    map[rune]Position
    Jumps    *JumpList
    History  *UndoTree
    Selection *Range
    Mode     Mode

    // Metadata
    ConnectionID string
    Path         string  // persisted scratch file path, if any
    Dirty        bool
}

type Line struct {
    Runes []rune
    // Computed lazily, invalidated on edit
    Highlights []Span
}

type Position struct { Line, Col int }
type Range    struct { Start, End Position; LineWise bool }
```

Edits go through `Apply(edit Edit) error` which records to the UndoTree
(vim-style branching undo).

### 13.2 Syntax highlighting

Tree-sitter via `smacker/go-tree-sitter` with the `sql` grammar. Spans
attach to `Line.Highlights`. Theme controls the actual colors:

```yaml
theme:
  sql:
    keyword:    [magenta, bold]
    function:   yellow
    string:     green
    number:     cyan
    comment:    [grey, italic]
    operator:   white
    type:       blue
    identifier: white
    placeholder: [yellow, bold]   # $1, :foo
    error:      [red, underline]
```

Re-parse is incremental per line edit (tree-sitter supports this).

### 13.3 Completion

Triggered manually by `<c-x><c-o>` or automatically by `keys.editor.autocomplete: true`.

Sources (in priority order):

1. **Schema-aware**: after a `FROM <tab>`, suggest tables from current
   schema; after `<table>.<tab>`, suggest columns of that table.
2. **Function names**: from `information_schema.routines`.
3. **Keywords**: a static list.
4. **Snippets**: user-defined (§14).
5. **History**: tokens from past queries on this connection.

The completion popup is the existing `SUGGESTIONS` context.

### 13.4 Execution modes

| Key (default) | Behavior |
|---|---|
| `<leader>r` | Run current statement (delimited by `;` or visual selection) |
| `<leader>R` | Run all statements in buffer |
| `<leader>e` | EXPLAIN current statement |
| `<leader>E` | EXPLAIN ANALYZE current statement |
| `<leader>x` | Cancel running query |
| `<leader>!` | Run current statement in a *new* transaction (rolled back on quit) |

"Current statement" uses the `textobject.statement` text object — found
by walking the parse tree to the enclosing `statement` node.

### 13.5 Persistence

Buffers are persisted to `~/.local/state/dbsavvy/buffers/<conn>/<uuid>.sql`.
On startup, scratch buffers are restored per connection. This is how
vim users expect editors to behave (think `:Telescope find_files` in
neovim restoring across sessions). Use the `Fs` from `Common` so tests
swap `afero.NewMemMapFs`.

### 13.6 Query history browser (decision 7)

`<leader>h` from the `QUERY_EDITOR` scope opens the `HISTORY` context
(TEMPORARY_POPUP from §8). The history backing store is the SQLite
file from §15.1 (`history.sqlite`); reads use the file's FTS5 index
on the `sql` column plus columns for `executed_at`, `connection_id`,
`duration_ms`, `rows_affected`, `succeeded`.

Layout (split popup, ~70% screen width):

```
┌─Query history · prod-pg · 12,418 entries ────────────────────────┐
│                                                                  │
│  /select * from users where org_id          [filter]            │
│ ┌──────────────────────────────────────────────────────────────┐ │
│ │ ⌚ 2026-05-17 10:14 ✓ 3.4ms 100r SELECT u.id, u.email FROM…│ │
│ │ ⌚ 2026-05-17 10:12 ✓ 1.1ms  87r SELECT * FROM users WHER…│ │
│ │ ⌚ 2026-05-17 10:09 ✗  err — INSERT INTO users (email) VAL…│ │ ◀─ failures dimmed
│ │ ⌚ 2026-05-17 10:01 ✓ 0.8ms   0r EXPLAIN ANALYZE SELECT *…  │ │
│ │ …                                                            │ │
│ └──────────────────────────────────────────────────────────────┘ │
│ ┌──────────────────────────────────────────────────────────────┐ │
│ │ SELECT u.id, u.email                                         │ │
│ │ FROM users u                                                  │ │ ◀─ preview pane,
│ │ WHERE u.created_at > now() - interval '7d'                    │ │    tree-sitter
│ │ ORDER BY u.created_at DESC                                    │ │    highlighted
│ │ LIMIT 100                                                     │ │
│ └──────────────────────────────────────────────────────────────┘ │
│ <CR>: insert at cursor   <C-r>: open in new scratch              │
│ <C-y>: yank to register  d: delete entry   <Esc>: close          │
└──────────────────────────────────────────────────────────────────┘
```

Behaviors:

- The filter field at the top is FTS-backed (`MATCH` against the SQL
  column). It updates the list live (debounced 80 ms). Empty filter
  shows entries reverse-chronologically.
- `j` / `k` move the selection; the preview pane re-renders.
- `<CR>` inserts the selected SQL at the cursor in the originating
  buffer and closes the popup.
- `<C-r>` opens a new scratch buffer pre-filled with the selection
  (mode = Normal at file start).
- `<C-y>` prompts for a register name (single key, default `"`) and
  yanks the SQL to that register.
- `d` removes the entry from history (with single-keypress
  confirmation `dd`, vim convention).
- Entries scope to the current connection by default; `<C-a>` toggles
  "all connections" (the list gains a `[conn]` column).

History recording happens on every executed statement via the
`pkg/query/history.go` writer (lazygit's `tasks.go`-style background
goroutine — never blocks the UI thread). The schema is migrated by
`pkg/query/history_migrations.go` (separate from the YAML config
migrations in §15.4).

---

## 14. Custom Commands (Extensibility)

The shape is **lazygit's custom commands**, adapted for SQL. The fewer
changes, the better — the lazygit pattern is well-tested and users have
internalized it.

### 14.1 Schema

```yaml
custom_commands:
  - key: "<leader>kr"
    scope: tables
    description: "Reindex selected table"
    sql: "REINDEX TABLE {{.SelectedTable.QualifiedName}}"
    output: log
    loading_text: "Reindexing {{.SelectedTable.Name}}…"
    confirm: "Reindex {{.SelectedTable.Name}}? This locks the table."
    after:
      refresh: [indexes, tables]

  - key: "<leader>kc"
    scope: tables
    description: "Copy table DDL to clipboard"
    sql: "{{ddl .SelectedTable}}"
    output: clipboard

  - key: "<leader>kf"
    scope: query_editor
    description: "Format SQL with pg_format"
    command: "pg_format -"
    stdin: "{{.CurrentBuffer.Content}}"
    output: replace_buffer

  - key: "<leader>km"
    scope: global
    description: "Schema migration menu"
    command_menu:
      - key: u
        sql: "{{readFile .MigrationFile}}"
        output: log
        description: "Run migration up"
        prompts:
          - type: input
            title: "Migration file:"
            key: MigrationFile
            suggestions:
              command: "ls db/migrations/*.up.sql"
      - key: d
        sql: "{{readFile .MigrationFile}}"
        output: log
        description: "Run migration down"
```

### 14.2 What we keep from lazygit

- **Prompt chaining** (recursive lambda wrap, `handler_creator.go:47`).
- **Suggestions** (`preset:` or `command:`).
- **Conditional prompts** (`condition: '{{ eq .Form.X "y" }}'`).
- **Output modes**: `none`, `terminal`, `log`, `popup`, plus new ones:
  `grid` (render rows in the result grid), `replace_buffer` (substitute
  the QueryEditor contents), `clipboard` (copy raw output).
- **Command menus** (`command_menu: [...]`) — nested submenu under one key.
- **SessionState** template context — `{{.SelectedTable}}`,
  `{{.SelectedColumn}}`, `{{.SelectedSchema}}`, `{{.CurrentConnection}}`,
  `{{.CurrentBuffer}}`.
- **Templating functions**: `quote`, `runCommand`, plus new ones:
  `ddl` (produce DDL for a table/index), `readFile`, `now`,
  `qualified` (`{{qualified .SelectedTable}}` → `"public"."users"`).

### 14.3 What's new

- `sql:` field — runs against current session instead of shelling out.
  When `sql:` is present, `command:` must be absent and vice versa.
- `confirm:` field — required confirmation popup before running.
- `after.refresh:` — list of contexts to re-load on success.
- `after.commit:` / `after.rollback:` — wrap in a tx automatically.
- `output: grid` — useful for "list all foreign keys referencing this
  column" power queries.
- `output: replace_buffer` — formatters and prettifiers.

### 14.4 Custom commands appear in cheatsheet

When `?` is pressed, custom commands are interleaved with built-ins under
their declared scope, distinguished by a `★` glyph in the cheatsheet.
Same source-of-truth principle.

---

## 15. Config System

### 15.1 Files

```
~/.config/dbsavvy/
├── config.yml              # main config
├── connections.yml         # connection profiles (often kept separate for sharing)
├── themes/
│   ├── solarized-dark.yml
│   └── gruvbox.yml
└── snippets/
    ├── postgres.yml
    └── analytics.yml

~/.local/state/dbsavvy/
├── state.yml              # mutable: recent connections, last-buffer, popup-seen flags
├── history.sqlite         # query history (SQLite, not YAML — supports search)
└── buffers/
    └── <conn-id>/
        └── <uuid>.sql
```

`adrg/xdg` picks the right paths on each OS.

### 15.2 Schema and defaults

`pkg/config/user_config.go` defines a single `UserConfig` struct with
`yaml:` tags. Defaults are inline in `GetDefaultConfig()`. Loading:

```go
base := GetDefaultConfigForPlatform(runtime.GOOS)
for _, file := range configFiles {
    if err := yaml.Unmarshal(content, base); err != nil { return err }
}
```

Multi-file via `LG_CONFIG_FILE=path1,path2` — direct port of lazygit's
mechanism. Per-connection overlays via a `.dbsavvy/config.yml` in the
directory the user invoked dbsavvy from (or pinned in the connection
profile).

### 15.3 Hot reload

Polling, not fsnotify (lazygit's `ReloadChangedUserConfigFiles`). Every
2 s the background tick stats config files; if mtime changed, full
reload:

1. Parse new YAML.
2. Run validations.
3. Swap `common.UserConfig` (atomic pointer).
4. `KeybindingService.Build()` — new chord tries.
5. `theme.Apply(newConfig)` — color globals reassigned.
6. Toast: "Config reloaded."

**Caller contract:** all consumers MUST `Load()` the atomic pointer at point
of use; the `*UserConfig` value MUST NOT be cached across reload boundaries.
`pkg/common.Common.Cfg()` is the canonical accessor.

### 15.4 Migrations

`pkg/config/migrations.go` — versioned YAML transforms applied before
unmarshal, with the migrated YAML written back to disk. v1 ships with
a `config_version: 1` field; v2 migrations add `config_version: 2` etc.

Plan to need this from day one (lazygit learned this lesson late and
paid for it).

### 15.5 JSON schema generation

`pkg/jsonschema/generate.go` — runs at `go generate ./...`, walks
`UserConfig`, emits `schema/config.json`. Editors (VS Code via the YAML
extension, neovim via `yaml-language-server`) pick it up automatically
when the file is named `config.yml` under `.config/dbsavvy/`.

We also publish the schema to the `dbsavvy.io` site for self-hosted
LSP setups.

### 15.6 Validation

`pkg/config/validation.go` — programmatic, runs post-unmarshal:

- Every key label parses.
- Every action ID in keybindings exists in the CommandRegistry.
- Every `scope` is a valid `ContextKey`.
- No two non-`<nop>` bindings share `(mode, scope, sequence)`.
- Every theme color parses.
- Every connection has a `driver` registered.
- Every `command_menu` is non-empty.
- Custom commands declare exactly one of `sql` / `command` / `command_menu`.

Failures are aggregated and shown in a startup toast plus the log; the
app still starts with as much of the config as is valid.

### 15.7 First-run experience (decision 10)

When dbsavvy starts and no `~/.config/dbsavvy/` exists (or
`connections.yml` is empty), the app:

1. Creates `~/.config/dbsavvy/` with a default `config.yml` containing
   only the leader/timeout settings and a commented-out keybinding
   block pointing at the docs.
2. Opens the TUI on the `CONNECTIONS` context with an empty list.
   The empty state renders:
   ```
   ┌─Connections ─────────────────────────────────────────────────┐
   │                                                              │
   │   No connections yet.                                        │
   │                                                              │
   │     a   add a connection                                     │
   │     ?   keybindings                                          │
   │     :q  quit                                                 │
   │                                                              │
   └──────────────────────────────────────────────────────────────┘
   ```
3. **One-time tip popup** overlays it: a brief "welcome — your config
   lives at `<path>`; bindings are at `<docs-url>`; press `<esc>` to
   dismiss" message. Dismissed with `<esc>` or any key; stamps
   `AppState.StartupTipsSeenAt` (§16.4) and never reappears for that
   user state file.

`a` opens an inline prompt sequence (driver → name → DSN → optional
label / color) that appends a connection to `connections.yml` and
reloads. The sequence uses the existing `Prompt` helper from
`pkg/gui/controllers/helpers/prompt_helper.go` — no separate wizard
machinery (the §1 "no wizards" principle stands; this is a chained
prompt, not a multi-step modal).

### 15.8 Lost-connection recovery (decision 11)

Connection loss covers: server restart, TCP reset, idle timeout,
revoked credentials. Detection points:

- The active session's `Conn.Query` / `Conn.Exec` returns a fatal
  error (`pgconn.SafeToRetry == false` for pgx).
- An explicit `Ping(ctx)` triggered by the next user action returns
  an error.

Flow:

1. The error reaches `QueryHelper`. The session is marked
   `Disconnected` and a non-modal toast appears:
   `"connection to prod-pg lost — <leader>R to reconnect"`. The
   active result tab (if any) becomes
   `(error: connection terminated, N rows received)`.
2. The schema rail dims; selecting any item in it triggers a Ping.
   On Ping failure, the **reconnect dialog** opens (TEMPORARY_POPUP,
   bordered in `connection.color`):
   ```
   ┌─⚠ PROD · connection lost ─────────────────────────────────┐
   │                                                           │
   │ The server closed the connection.                         │
   │                                                           │
   │ Server-side state is gone:                                │
   │   • any open transaction has been rolled back              │
   │   • temp tables and session settings were discarded        │
   │                                                           │
   │ Client-side state preserved:                              │
   │   • all scratch buffers, pending edits, history            │
   │                                                           │
   │   [r] retry  [c] pick another connection  [q] quit         │
   └───────────────────────────────────────────────────────────┘
   ```
3. `r` retries (`Driver.Open` again with the same profile). On
   success, the schema rail re-loads, the user is returned to their
   previous context, and a toast confirms `"reconnected to prod-pg"`.
4. `c` pushes the `CONNECTIONS` context.
5. `q` quits via the normal quit-with-pending-edits check (§12.5.2).

Reconnects do **not** auto-replay the failed statement; the user
chooses whether to re-run. This is the safety principle (§1) —
reconnect + replay risks double-executing a non-idempotent statement.

### 15.9 Search-path, `SET ROLE`, and hidden schemas (decisions 13 + 21)

#### Per-session SQL settings

The `:` ex-line accepts arbitrary statements; the helper recognizes a
short list of `SET` patterns (`SET search_path`, `SET ROLE`, `RESET
ROLE`, `SET TIME ZONE`, `SET application_name`) and:

- Runs the statement on the current session.
- Updates a per-session settings map (`pkg/session/session.go`'s
  `SettingsSnapshot`) so the status bar can show, e.g.,
  `[search_path=app,public] [role=app_readonly]`.
- Persists the most-recent values per connection to
  `AppState.LastSessionSettings[<connection_id>]` so a future session
  on the same connection can restore them (with a toast hint).
- Refreshes the schema rail loaders (visible-schemas / tables /
  columns are search-path-sensitive in Postgres).

`<leader>p` opens a single-line prompt pre-filled with
`SET search_path TO `; pressing `<CR>` runs it. The prompt's
suggestion helper queries `pg_namespace` for available schema names
(scoped through `hidden_schemas` — see below).

Other SQL not matching the recognized SET patterns runs normally on
the session, no special handling. Cross-database attach (`\c db2`) is
explicitly out of scope for v1; the user adds a separate
`ConnectionProfile`.

#### Schema visibility

Hidden schemas merge from three sources at runtime:

```
hidden = built_in_defaults(driver)
       ∪ connection.hidden_schemas (config)
       ∪ AppState.HiddenSchemas[connection_id] (runtime toggles)
```

`built_in_defaults(driver)` ships per-driver constants:

- Postgres: `pg_catalog`, `information_schema`, `pg_toast`,
  `pg_temp_*`, `pg_toast_temp_*`.
- SQLite: `sqlite_temp`, `sqlite_sequence`, `sqlite_stat*`.

Globs use `path.Match` semantics. The `SCHEMAS` context filters its
display list against `hidden` unless **show-hidden mode** is active.

Keybindings on the `SCHEMAS` context:

| Key | Effect |
|---|---|
| `H` | Hide the schema under the cursor (writes to `AppState.HiddenSchemas`); the entry vanishes (or dims, if show-hidden mode is on). |
| `<leader>H` | Toggle show-hidden mode for the rail. Title shows `Schemas [+hidden]` when on; hidden entries render dimmed with a `(hidden)` suffix. |
| `U` | (Only in show-hidden mode.) Un-hide the schema under the cursor. Removes from `AppState.HiddenSchemas`. If the schema is hidden by config (`connection.hidden_schemas`) or by built-in default, un-hide opens a confirmation noting that the override is per-session and won't survive a config rewrite. |

Show-hidden mode is a per-`SCHEMAS`-context flag (not persisted) —
restarting the app leaves it off.

The same hide/show conceptual model applies to other rail entries in
later phases (e.g. hiding individual tables); v1 ships schema-only.

### 15.10 Transaction model (decisions 14 + 15)

v1 ships with **implicit autocommit** by default: every statement
executed outside an explicit transaction commits immediately. There
is no global "autocommit off" mode; users who want batched semantics
open an explicit transaction.

The `<leader>tx` chord opens a transaction-mode submenu (which-key
popup, §10.9):

```
┌── <leader>t ──────────────────────────┐
│ b   Begin transaction                 │
│ c   Commit current transaction        │
│ r   Rollback current transaction      │
│ s   Savepoint…                        │
│ R   Release savepoint…                │
│ o   Rollback to savepoint…            │
└───────────────────────────────────────┘
```

The currently-open transaction shows in the status bar:

- No tx: nothing.
- Active tx: `[TX]` in `theme.WarningFg`.
- Active tx with savepoints: `[TX:sp1,sp2]`.
- Failed tx (statement errored, awaiting rollback): `[TX*]` in
  `theme.ErrorFg` — Postgres won't accept further statements until
  `ROLLBACK` (or `ROLLBACK TO SAVEPOINT ...`); the helper surfaces a
  toast nudging this.

Inline-edit commits (§12.5.6) continue to use a **dedicated session**
acquired via `AcquireSession`. They do not run inside or affect the
user's manual transaction state — this is already documented at
§12.5.6 and remains the invariant.

#### Quit with open transaction (decision 15)

`:q` / `app.quit` with an active server-side transaction is blocked
by a confirmation dialog (bordered in `connection.color` like the
commit dialog, §12.5.5):

```
┌─⚠ PROD · open transaction ──────────────────────────────────────┐
│                                                                 │
│ You have an open transaction on prod-pg.                        │
│   • 3 statements executed                                       │
│   • 2 savepoints active: sp_pre_import, sp_after_load           │
│                                                                 │
│   [c] commit and quit                                           │
│   [r] rollback and quit                                         │
│   [a] abort quit (stay in app)                                  │
└─────────────────────────────────────────────────────────────────┘
```

If pending inline edits (§12.5) also exist, that block precedes this
one — the user resolves edits first, transactions second.

---

## 16. Themes, Styles, i18n, State

### 16.1 Theme

```go
// pkg/theme/theme.go — single atomic snapshot reassigned on reload
type themeState struct {
    ActiveBorder, InactiveBorder, SelectedRowBg *Style
    NullValueFg, NumericFg, StringFg, KeywordFg *Style
    // ... ~40 named styles
}

var current atomic.Pointer[themeState]

func Current() *themeState        { return current.Load() }
func Apply(cfg *config.ThemeConfig) error {
    current.Store(buildState(cfg))
    return nil
}
```

Single-pointer swap is race-safe on all architectures. Readers `Load()` once
per frame and read all fields from the same snapshot.

Built-in themes ship in `pkg/theme/builtin/`: `default-dark`,
`default-light`, `solarized-dark`, `gruvbox`, `nord`.

### 16.2 Style builder

`pkg/gui/style/basic_styles.go`:

```go
type TextStyle struct { /* fg, bg, attrs */ }

func (s TextStyle) SetFg(c Color) TextStyle
func (s TextStyle) SetBg(c Color) TextStyle
func (s TextStyle) SetBold() TextStyle
func (s TextStyle) SetUnderline() TextStyle
func (s TextStyle) SetItalic() TextStyle
func (s TextStyle) MergeStyle(other TextStyle) TextStyle
func (s TextStyle) Sprint(a ...interface{}) string
```

Backed by `gookit/color`. Direct port.

### 16.3 i18n

`pkg/i18n/english.go`:

```go
type TranslationSet struct {
    OpenTable           string
    TruncateTable       string
    DropTable           string
    DropTableTooltip    string
    AreYouSure          string
    ConnectionLost      string
    QueryCancelled      string
    Rows                string
    NullValue           string
    // ... eventually ~500–2000 strings
    Actions             ActionTranslations
}

type ActionTranslations struct {
    OpenTable     string
    RunQuery      string
    CancelQuery   string
    // ...
}

func EnglishTranslationSet() *TranslationSet {
    return &TranslationSet{
        OpenTable:        "Open table",
        TruncateTable:    "Truncate table",
        Rows:             "rows",
        NullValue:        "NULL",
        // ...
    }
}
```

Translations are JSON files embedded via `go:embed`, merged on top of
English by cloning `EnglishTranslationSet()` then calling `json.Unmarshal`
on the overlay bytes into that clone. The standard library's "only set
fields present in the JSON token stream" behavior gives the desired
override-where-set, preserve-otherwise semantics with no third-party dep.

Detection via `github.com/Xuanwo/go-locale.Detect()` returning
`golang.org/x/text/language.Tag`. On any error OR if the parsed Tag is
`language.Und`, the app falls back silently to `language.English`.

### 16.4 AppState (persisted runtime state)

```go
type AppState struct {
    LastConnectionID    string
    RecentConnectionIDs []string
    LastBufferUUIDs     map[string]string         // per-connection
    LastTheme           string
    LastResultViewMode  string                    // "grid" | "expanded" — see §12.3
    StartupTipsSeenAt   time.Time
    Version             string                    // last-run dbsavvy version

    // Runtime, per-connection overrides (decisions 13, 18, 21, 23)
    StatementTimeoutOverride map[string]string         // connection_id → duration
    HiddenSchemas            map[string][]string       // connection_id → schema names (runtime adds)
    HiddenColumns            map[string]map[string][]string
                                                       // connection_id → table-or-fingerprint → column names
    LastSessionSettings      map[string]map[string]string
                                                       // connection_id → setting → value (e.g. search_path)
}
```

The new maps are append-only at the per-connection level; entries
removed via UI (e.g. `U` un-hiding a schema) prune the slice. Atomic
write via tmp file + rename, same as today.

Persisted to `~/.local/state/dbsavvy/state.yml`. Atomic write (tmp file
+ rename). Loaded once at startup and held in `Common.AppState`.
**v1 OS support: Linux and macOS only.** POSIX rename semantics required
for atomic replace; Windows support is deferred to a later epic. The temp
file is written with mode 0600; the parent directory is created with mode
0700.

---

## 17. Threading Model

Direct port of lazygit's discipline:

1. **UI thread** — the goroutine running `gocui.MainLoop`. All Context /
   View / GridView state mutations happen here.
2. **Background goroutines** — every long-running operation (query
   streaming, loader enrichment, password-command shell-out) runs on a
   goroutine started via `gui.OnWorker(func(gocui.Task) error)`.
   `OnWorker` increments a "busy" counter; the bottom spinner displays
   while > 0.
3. **Coming back to UI** — goroutines call `gui.OnUIThread(func() error)`
   to mutate UI state, or `gui.OnUIThreadContentOnly(func)` to skip
   layout for "just rerender this view's content" (perf-critical for
   row streaming).
4. **`afterLayoutFuncs`** — a buffered channel; functions enqueued here
   run *after the next paint*. Useful for "scroll to selected row after
   layout settled."

Mutexes are typed and named (`pkg/gui/types/common.go`'s `Mutexes`
struct) so the right mutex is locked for the right concern — never use
an anonymous `sync.Mutex` for cross-cutting state.

---

## 18. Phased Implementation Plan

Each phase is independently shippable. The principle is **ship the
keybinding system early**, because it's the differentiator and the
hardest thing to retrofit.

### Phase 1 — Skeleton + connect (2–3 weeks)

- Repo scaffold (main.go, pkg/, Taskfile.yml, golangci-lint, gofumpt).
- `pkg/common`, `pkg/config` (basic — no migrations yet), `pkg/i18n`
  (English only).
- `pkg/drivers/driver.go` + `pkg/drivers/pg/driver.go` (just `Open`,
  `Ping`, `ListSchemas`, `ListTables`, `ListColumns`).
- `pkg/gui/gui.go` with one View showing connection list, one with
  the schemas tree. No editor, no result grid yet.
- `pkg/gui/context/` with `CONNECTIONS`, `SCHEMAS`, `TABLES`,
  `COLUMNS`. Stack-based `ContextMgr`.
- Single-key bindings via lazygit's direct gocui registration — **no
  chord system yet**.
- `:q` quits. `?` opens menu of bindings.
- **First-run flow** (§15.7, decision 10) — empty CONNECTIONS context,
  `a` to add, one-time tip popup writing `StartupTipsSeenAt`.
- **Schema hide/show** (§15.9, decision 21) shipped at the rail level
  even though tables/queries come in later phases — affects the
  loader filters from day one.
- **Basic mouse** (§6.1, decision 22) wired since gocui mouse is one
  config flag.

**Definition of done:** open a connection, navigate the tree (with
hidden-schema toggle working), see columns. No SQL execution.

### Phase 2 — The keybinding system (3–4 weeks)

- `pkg/gui/keys/` — `CommandRegistry`, `Key` parser, `ChordTrie`,
  `Matcher`, modal dispatcher.
- Refactor Phase 1 bindings to ChordBindings + CommandRegistry. (Most
  of Phase 1's wiring throws away.)
- Implement `<leader>`, `<localleader>`, `<nop>`.
- Implement timeout disambiguation (`timeoutlen` / `ttimeoutlen`).
- Implement which-key popup (DISPLAY_CONTEXT).
- Implement counts (`5j`).
- Implement modes (Normal/Insert/Command).
- Wire `:` command line.
- Cheatsheet generator (`pkg/cheatsheet/`).
- Config validation for key labels and action IDs.
- Hot reload on config change.

**Definition of done:** all Phase 1 bindings work through the new
system; `<leader>2` (digit-jump) switches focus to the tables rail;
user can rebind anything in `config.yml` and have it live-reload.
**Cheatsheet completeness invariant** (§10.11, decision 26) including
the package-level test and the `· / ✱ / ★` source legend is delivered
this phase, since the surface only grows from here.

### Phase 3 — Query execution + result grid (3–4 weeks)

- `pkg/query/`, `pkg/session/`.
- `pkg/tasks/` — port lazygit's `tasks.go` to row streams.
- `pkg/gui/grid/` — the GridView, including pagination (§12.3
  decision 4), in-grid filter `/` (decision 16), in-grid sort
  `<leader>s` (decision 17), and column hide `<leader>gH`
  (decision 18).
- `pkg/gui/controllers/query_editor_controller.go` + a *naive*
  single-line input first (no full vim editor yet).
- `pkg/drivers/pg/` — `Execute`, `Stream`, `Cancel`, `Explain`,
  plus parsing for EXPLAIN tree rendering (§12.8, decision 9).
- `Main + Secondary` pair: QueryEditor + Result tabs (multi-tab
  model from §7 / §12.2 / decisions 1+2 shipped from day one).
- Run / cancel (with no-live-cancel fallback, decision 6).
- Server NOTICE / WARNING streaming into `command_log` + toast
  (§12.9, decision 5).
- **Transaction submenu** `<leader>tx` and `[TX]` status indicator
  (§15.10, decision 14). Quit-with-open-tx confirmation (decision 15).
- **Lost-connection recovery** dialog (§15.8, decision 11) including
  the explicit "server state is gone" copy.
- **`<leader>p` search_path** and `:` ex-line SET recognition
  (§15.9, decision 13).
- **Statement-timeout** application at session open (§11.2) and the
  `<leader>tt` runtime override prompt (decision 23).
- **Result export** `<leader>oe` menu (§12.7, decision 8).

**Definition of done:** type a query, run it with `<leader>r`, see
results stream into a tab; multi-statement run opens N tabs; preempt
on context switch cancels server-side; `<leader>oe` exports a 100k-row
result to a CSV file without OOM; quit with `BEGIN` outstanding
blocks with the confirmation dialog.

### Phase 4 — The SQL editor (4–6 weeks)

- `pkg/gui/editor/` — buffer, cursor, marks, jumplist, undo tree,
  motions, text objects, operators.
- Visual / VisualLine / VisualBlock modes.
- **Visual-select run** (`<leader>r` in Visual mode → selection split
  on `;` → N tabs, §13.4 decision 3).
- Operator-pending mode (`d{motion}`, `y{motion}`, `c{motion}`).
- Search (`/`, `?`, `n`, `N`).
- Substitute (`:s/foo/bar/g`).
- Tree-sitter SQL syntax highlighting.
- Buffer persistence per connection.
- **Macros** (`q{reg}` / `@{reg}`) recording resolved keystrokes,
  scope-locked to the recording context (§10.13 decision 25).

**Definition of done:** the QueryEditor is a credible vim experience —
not a complete clone, but enough that `dap` deletes the SQL statement
under the cursor, `gqq` reformats, `>>` indents, `u` undoes, etc.

### Phase 5 — Completion + table data editing (2–3 weeks)

- Schema-aware completion (FROM ↦ tables, table.x ↦ columns).
- Inline cell editing in the GridView (§12.5 full spec): pending
  set, commit dialog with `connection.color`, conflict dialog,
  expression edits.
- **Forward FK navigation** `gd` opening new result tabs with
  jumplist marks (§12.6, decision 19).
- **Reverse FK navigation** `gD` opening the referencing-table picker
  (§12.6, decision 20).
- Type-aware value editors (date picker, JSON popup, boolean toggle).

### Phase 6 — Custom commands + snippets + history UX (2–3 weeks)

- `pkg/gui/services/custom_commands/` — port lazygit's package,
  add `sql:` field, `output: grid` and `output: replace_buffer`.
- `pkg/query/history.go` — SQLite-backed history with FTS5 (the
  write path can ship earlier as a silent recorder in Phase 3).
- **Query history browser** `<leader>h` opening the `HISTORY` popup
  with FTS filter + preview pane (§13.6, decision 7).
- Snippets via custom commands (essentially a custom command with
  `output: replace_buffer`).

### Phase 7 — Polish + second driver (3–4 weeks)

- Migrations framework actually wired (config_version: 1 → 2).
- Theme variants shipped (gruvbox, solarized, nord, light/dark).
- Translations for 3+ languages (zh-CN, ja, de, pt-BR).
- `pkg/drivers/sqlite/` — proves the driver abstraction is real.
- Performance pass: profile, fix hot spots. Startup goal < 200 ms.
- Documentation site (mdbook or Astro Starlight).

### Phase 8 — Future (out of scope for v1)

- MySQL driver.
- Schema diff / migration generation.
- ER-diagram view (ASCII art).
- Multi-server compare.
- Replication-lag dashboards.
- LISTEN/NOTIFY tail view.

---

## Appendix A — Layered Dependency Rules

To prevent the codebase from collapsing into a god-struct (as lazygit
honestly admits theirs has), enforce these rules via `go vet`-style
custom linting:

```
Controllers      may depend on  Helpers, Contexts, Views, Common, Models
Helpers          may depend on  Contexts, Views, Common, Drivers, Session, Query, Models
Contexts         may depend on  Views, Common, Models
Views (gocui)    may depend on  nothing else dbsavvy

Drivers          may depend on  Models, Common
Session, Query   may depend on  Drivers, Models, Common

Editor, Grid     may depend on  Common, Models, gocui

Config           may depend on  Models only
i18n             may depend on  nothing else dbsavvy
```

Violations get caught at PR time. Add a `tools/lint-deps/` script that
walks the AST.

---

## Appendix B — Comparison Matrix with lazygit

| Subsystem | lazygit | dbsavvy | Verdict |
|---|---|---|---|
| Bootstrap (`pkg/app`) | flaggy + xdg + Common assembly | identical | **copy** |
| `Common` struct | Log + Tr + atomic UserConfig + AppState + Fs | identical | **copy** |
| gocui usage | manual Layout, named views, line-buffer | same, plus custom GridView subtype | **copy + extend** |
| boxlayout | yes, used for window arrangement | same | **copy** |
| Context system | stack-based, kind enum, lifecycle hooks | same | **copy** |
| Controller pattern | declarative `GetKeybindings` + thin handlers → Helpers | same, but with ChordBinding | **copy + extend** |
| Helpers | shared multi-step UI flows | same | **copy** |
| Keybinding registry | per-context single-key, action-indexed YAML | **chord trie, modal, key-indexed, CommandRegistry** | **rewrite** |
| Custom commands | YAML, prompts, suggestions, template, output modes | same + `sql:` + `output: grid` + `confirm:` | **copy + extend** |
| Tasks (streaming) | `ViewBufferManager` + `NewCmdTask` for stdout | `ResultBufferManager` + `NewQueryTask` for RowStream | **port** |
| Config (YAML, defaults, hot reload, jsonschema) | full system in `pkg/config` + `pkg/jsonschema` | identical | **copy** |
| Migrations | YAML node rewrites | same (from day 1, not day 100) | **copy + earlier** |
| i18n | English baseline, JSON overlays, mergo | identical | **copy** |
| Theme | mutable globals, reassigned on reload | identical | **copy** |
| Discoverability (options bar + cheatsheet + menu) | yes | yes + **which-key popup** | **copy + extend** |
| Threading (UI thread + OnWorker + OnUIThread) | yes | identical | **copy** |
| Multi-pane focus | `MainContextPair` | identical | **copy** |
| Text editor | only a single-line prompt and the commit-message buffer | **full vim-style buffer in QueryEditor** | **build new** |
| Result rendering | `io.Writer` line buffer | **GridView with column-aware cells** | **build new** |
| Connections vs. repos | repo paths, RepoStateMap | connection profiles, ConnectionStateMap | **rename + extend** |

---

## Appendix C — Prior Art

### lazysql (jorgerojas26/lazysql)

A bubbletea-based DB TUI that's the closest existing thing to this
project. Why it's not enough:

- Bubble Tea / Elm — the very thing that felt clunky in the prior
  attempt.
- Hard-coded keybindings, not user-customizable.
- No modal editing.
- No custom commands.
- Single-pane focus model.

What it does well that we should match: connection management UX,
multi-driver from day 1 (Postgres / MySQL / SQLite). We learn from this
that **driver abstraction is non-negotiable**.

### dbcli family (pgcli, mycli, litecli)

CLI (not TUI), but the **completion** UX is world-class — schema-aware,
sub-second. We crib their `prompt_toolkit`-inspired completion strategy
(suggesting context-appropriate tokens based on a SQL parser's current
state).

### usql

The "psql-but-universal" multi-driver client. Driver list is
encyclopedic; the way they version-detect and dispatch per-driver is a
template for our `Capabilities()` story.

### TablePlus

The aesthetic and UX target — fast, focused, no clutter. The biggest
features to recreate in a TUI:

- One-keystroke connection switcher
- Server-side cancellation
- Multi-statement execution with per-statement result blocks
- Schema browsing that doesn't lag
- Edit-mode for cells with a clear pending-changes indicator
- Foreign key navigation (`gd` here, double-click there)

### vim / neovim

The keybinding north star. We do not try to be 100% vim-compatible
inside the QueryEditor — that way lies madness. We hit the 80% that
matters: modes, motions, operators, text objects, counts, registers,
marks, search, substitute, undo.

### Helix

The cleaner take on modal editing — selection-first, then action.
Worth considering as an *alternative* mode for the editor in a future
phase (configurable, `editor.mode_style: vim | helix`).

---

## End of design

This document is the spec. Implementation tracks against the phases in
§18. Any change to the keybinding system (§10), driver interface (§11),
or context model (§8) requires a doc update *first*, then code.
