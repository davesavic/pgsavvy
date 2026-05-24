# dbsavvy — QA Test Suite

Manual QA test suite covering functionality shipped by the closed epics. Every test below maps to behavior that is implemented on `master` as of 2026-05-19. Features that were explicitly deferred to future epics (highlighter, macros, search/substitute, inline cell editing, FK navigation, transaction submenu UI, custom commands, etc.) are intentionally **not** listed.

Closed epics covered:
- `dbsavvy-2zl` — Project bootstrap & tooling
- `dbsavvy-8pa` — Foundation packages (common/config/i18n/logs/models/theme)
- `dbsavvy-921` — Driver layer + Postgres baseline + connection profiles
- `dbsavvy-enn` — TUI skeleton + side contexts + first-run + mouse + schema hide/show
- `dbsavvy-dlp` — Keybinding system (chord trie, modes, which-key, cheatsheet)
- `dbsavvy-66p` — Query execution core + multi-tab streaming + naive editor + NOTICE/WARNING
- `dbsavvy-wwd` — SQL editor (vim-style buffer, motions, operators, persistence) — MVP
- `dbsavvy-m47` — ChainedPrompter adapter for the add-connection flow
- `dbsavvy-tro` — UI walkthrough bug bundle (focus, cursor, toasts, which-key, dispatch, polish)

Leader keys (defaults): `<leader>` = `<space>`, `<localleader>` = `,`.

---

## 0. Prerequisites

### 0.1 Build the binary

**Steps:**
1. `task build`

**Expected:**
- `bin/dbsavvy` exists. Running `bin/dbsavvy --help` (or with no flags from a non-TTY) prints version info without panicking.

### 0.2 Run the unit suite

**Steps:**
1. `task test ./...`

**Expected:**
- Exit code `0`. No `FAIL` lines.

### 0.3 Lint

**Steps:**
1. `task lint`

**Expected:**
- Zero issues reported.

### 0.4 Bring up the Postgres fixture

**Steps:**
1. `docker compose -f docker/postgres/docker-compose.yml down -v`
2. `docker compose -f docker/postgres/docker-compose.yml up -d`
3. Wait for `pg_isready` against `localhost:5432`.

**Expected:**
- Postgres 17 container healthy, listening on `localhost:5432` with user/password/db = `dbsavvy / dbsavvy / dbsavvy_test`.
- The fixture (`docker/postgres/init/01_fixture.sql`) loads `app` and `reporting` schemas, with tables `users`, `roles`, `user_roles`, `posts`, a materialized view, a view, a mix of B-tree / partial / GIN indexes, PK / UNIQUE / FK / NOT NULL constraints, and `text[]` + `jsonb` columns with comments.

### 0.5 Run the integration suite (driver layer)

**Steps:**
1. `DBSAVVY_TEST_PG=postgres://dbsavvy:dbsavvy@localhost:5432/dbsavvy_test task test:integration`

**Expected:**
- All `//go:build integration` tests pass, including credential resolution, `read_only` persistence across pool-conn recycle, `statement_timeout` pass-through, and the per-loader (ListDatabases/Schemas/Tables/Columns/Indexes/Constraints) golden assertions.

---

## 1. First-run flow

### 1.1 Empty connections, fresh state dir

**Pre:** No `connections.yml` and no prior state dir.

**Steps:**
1. `bin/dbsavvy`.

**Expected:**
- TUI launches. The CONNECTIONS rail is focused (visible focus border in `ActiveBorder` color).
- A one-time tip popup is shown explaining `a` to add a connection.
- Status bar at the bottom shows a mode label (default `N`) plus a small options-bar hint.
- After dismissing the tip, `AppState.StartupTipsSeenAt` is stamped (file under XDG state dir, mode `0600`); next launch does **not** re-show the tip.

### 1.2 Quitting

**Steps:**
1. With no popup open, press `:q<cr>` *or* `:quit<cr>` *or* `<ctrl-c>` *or* `<space>q`.

**Expected:**
- App exits cleanly (no goroutine leak, no terminal artifacts).

---

## 2. Add-connection chained prompt (`a` on CONNECTIONS)

### 2.1 Happy path

**Steps:**
1. From the CONNECTIONS rail, press `a`.
2. In the **Driver** selection popup, `j`/`k` (or arrows) to highlight `postgres`, then `<cr>`.
3. In the **Name** prompt, type `local-pg` and press `<cr>`.
4. In the **DSN** prompt, type `postgres://dbsavvy:dbsavvy@localhost:5432/dbsavvy_test` and press `<cr>`.

**Expected:**
- Each prompt opens as a `TEMPORARY_POPUP` with a visible caret after the prompt label.
- On submit, `connections.yml` is created (or appended) with a `connections:` wrapper key containing the new profile.
- The new profile appears in the CONNECTIONS rail and is selectable.

### 2.2 ESC cancels at any step

**Steps:**
1. Press `a`, then `<esc>` at the Driver popup.
2. Re-open with `a`, advance to the Name step, then `<esc>`.
3. Re-open, advance to the DSN step, then `<esc>`.

**Expected:**
- Each `<esc>` closes the popup, returns focus to the CONNECTIONS rail, and writes nothing to `connections.yml`.

### 2.3 Validation re-prompts inline

**Steps:**
1. Press `a`, pick `postgres`.
2. Submit an empty name.
3. Submit a duplicate name (matching an existing profile).
4. Submit a DSN with an inline password (e.g. `postgres://u:p@h/d`).

**Expected:**
- The Name and DSN prompts stay open after each invalid submission. The label is appended with the validator error message. The previously typed value is preserved as the new initial value. Submission only proceeds when validation returns nil.

### 2.4 Empty driver list guard

**Pre:** Build/run a variant with no registered drivers (regression test for `promptDriver`).

**Steps:**
1. Press `a` on the CONNECTIONS rail.

**Expected:**
- A clear error toast is shown and the flow aborts. No infinite popup loop.

---

## 3. Connecting and navigating the schema rails

### 3.1 Open a connection

**Steps:**
1. Highlight `local-pg` on CONNECTIONS rail, press `<cr>`.

**Expected:**
- Connection opens against the docker fixture. The title bar, left-rail header, and status bar render in the connection's color with the configured icon/label.
- The SCHEMAS rail becomes visible and lists `app`, `reporting`, plus built-in schemas (except those in the built-in hidden default like `pg_catalog`, `information_schema`).

### 3.2 Drill Schemas → Tables → Columns → Indexes

**Steps:**
1. On SCHEMAS, navigate with `j`/`k` and press `<tab>` (or digits `1..4`) to cycle to TABLES.
2. Highlight `users` (or any fixture table); `<tab>` to COLUMNS.
3. `<tab>` to INDEXES.

**Expected:**
- Each rail populates from the driver loaders: tables of the selected schema, columns of the selected table (with type, nullable, default), indexes with method/unique/partial flags.
- Only the active rail wears the `ActiveBorder` color; the rest wear `InactiveBorder`.

### 3.3 Hide / show schemas

**Steps:**
1. On SCHEMAS, highlight `reporting` and press `H`.
2. Press `<leader>H` (toggle show-hidden mode).
3. Press `U` on `reporting`.

**Expected:**
- `H` removes the schema from the rail and persists into `AppState.HiddenSchemas` (debounced atomic write, mode `0600`).
- `<leader>H` toggles a mode where hidden schemas are listed again (visually marked).
- `U` un-hides; `AppState.HiddenSchemas` no longer contains it after the debounce window.

### 3.4 Mouse interaction (basic)

**Steps:**
1. Click on a different rail's view.
2. Wheel-scroll within a long list.
3. Double-click a table row.

**Expected:**
- Click focuses the clicked view (focus border moves there).
- Wheel scrolls the list.
- Double-click on a table currently surfaces a "deferred — coming in E10" toast (no crash). This is intentional — the data editor lands in a future epic.

### 3.5 Lost-connection placeholder

**Steps:**
1. With a connection open, stop the docker fixture (`docker compose ... stop`).
2. Trigger any driver call (e.g. switch schemas).

**Expected:**
- A toast surfaces describing the lost connection. (Full recovery dialog is a future epic — only the toast is expected here.)

---

## 4. Keybinding system & feedback

### 4.1 Which-key popup

**Steps:**
1. From a side rail, press `<space>` (leader) and pause for ~300 ms.

**Expected:**
- The WHICH_KEY popup appears listing every continuation under `<leader>`, sourced live from the trie (`ChildrenAt(prefix)`).
- Each row shows the next key + the action description (preserving the chord prefix, e.g. `<leader>q` not just `q`).
- Pressing `<esc>` dismisses the popup. Pressing an unmatched key also dismisses cleanly (no stuck popup).

### 4.2 Cheatsheet

**Steps:**
1. Press `?`.

**Expected:**
- The cheatsheet popup opens, sectioned by mode and by scope, populated from the live `CommandRegistry` + trie.
- Every published action appears with its chord; the override glyph (`✱`) legend matches the in-row glyph (`*`/`✱` consistency).
- Bindings overridden by the user are tagged as such.

### 4.3 `:reload` hot-swap

**Pre:** Edit your `~/.config/dbsavvy/dbsavvy.yml` to add a `keybindings:` entry remapping (e.g.) `app.quit` to `<leader>Q`.

**Steps:**
1. Press `:reload<cr>`.

**Expected:**
- Toast confirms reload. `<leader>q` no longer quits; `<leader>Q` does. No restart required.
- Pending chord state is canceled atomically with the swap; subsequent keys see only the new trie.

### 4.4 Counts and registers

**Steps:**
1. Focus the QUERY_EDITOR. Type `5j` to move down 5 lines (with content in the buffer).
2. Type `"ayy` to yank a line into register `a`. Then `"ap` to paste from `a`.
3. Type `"+y` to yank into the system clipboard register.

**Expected:**
- The motion repeats with the count.
- Register `a` holds the yanked line; paste lands the same content.
- First system-clipboard touch raises an info toast (and falls back to in-memory if clipboard access isn't available).

### 4.5 COMMAND_LINE editing primitives

**Steps:**
1. Press `:` to open the command line.
2. Type characters, press `<backspace>` to delete, press `<esc>` to cancel.

**Expected:**
- A caret renders at the typed position. Backspace, Delete, and arrow keys all edit the buffer (no silent drops). `<esc>` closes the popup and returns focus.

### 4.6 `<leader>q` quit path

**Steps:**
1. From any view, press `<space>q`.

**Expected:**
- The shim covers trailing chord keys, the matcher dispatches `app.quit`, and the app exits cleanly (regression from `dbsavvy-tro.7`).

---

## 5. Query editor — naive entry + execution

### 5.1 Focus the editor

**Steps:**
1. From a side rail, focus the QUERY_EDITOR (the main area).

**Expected:**
- `QueryEditorContext` is now active. `ModeStore[QUERY_EDITOR]` defaults to `Normal`. The mode label in the status bar reflects this (e.g. `N`).

### 5.2 Run a single statement

**Steps:**
1. Enter Insert mode (`i`), type `SELECT * FROM app.users;`, press `<esc>`.
2. Press `<space>r`.

**Expected:**
- Rows stream into a new result tab in the secondary slot.
- Tab title is `result 1: SELECT * FROM app.users…` (truncated to 40 chars).
- The grid renders headers from the column metadata. NULLs render dim/italic, JSON cells expand with `<cr>`, BLOBs render as hex.
- `Capabilities.HasLiveCancel` is `true` for postgres, so `<space>x` is not disabled.

### 5.3 Run all statements — multi-tab

**Steps:**
1. In the editor, type `SELECT 1; SELECT 2; SELECT 3;`.
2. Press `<space>R`.

**Expected:**
- Three result tabs open in order, executing serially on the same session.
- Each tab gets its own `ResultBufferManager` and grid.

### 5.4 Tab management

**Steps:**
1. With multiple result tabs open: press `<space>1`, `<space>2`, `<space>3` (global scope).
2. From an active tab, press `gt` and `gT` to cycle.
3. Press `<space>X` to close, `<space>=` to pin one, then exceed the `ui.result_tabs_max` (default 8) cap by running more queries.

**Expected:**
- Jump bindings work from any view (global scope).
- Cycle/close/pin operate on the active tab.
- When the cap is exceeded, the oldest non-pinned tab is evicted (stream canceled, RowStream closed, view deleted). Pinned tabs survive.

### 5.5 Preempt on context switch

**Steps:**
1. Start a long-running statement (e.g. `SELECT pg_sleep(30);`) with `<space>r`.
2. Switch focus to the SCHEMAS rail.

**Expected:**
- The in-flight query is canceled server-side via `pg_cancel_backend` (`PgConn.CancelRequest` on a fresh pool conn). The tab is marked `(cancelled, N rows)`.

### 5.6 Queued run

**Steps:**
1. Start a long query with `<space>r`.
2. Without canceling, press `<space>r` again on a different statement.

**Expected:**
- The second query opens a new tab marked `(queued)`. It transitions to `(running …)` once the first tab completes.

### 5.7 Explicit cancel

**Steps:**
1. Start a long query.
2. Press `<space>x` (with a result tab focused).

**Expected:**
- The active tab's stream is canceled. Tab is marked cancelled with row count.
- On drivers where `Capabilities.HasLiveCancel = false`, `<space>x` is rendered disabled in the options bar with the `DisabledReason` tooltip.

### 5.8 EXPLAIN / EXPLAIN ANALYZE

**Steps:**
1. With `SELECT * FROM app.users WHERE id = 1;` in the editor, press `<space>e`.
2. Press `<space>E`.
3. Type a mutating statement (e.g. `INSERT INTO app.users(name) VALUES ('x')`) and press `<space>E`.
4. Re-run the same mutating statement with `<space>!`.

**Expected:**
- `<space>e` opens a result tab with the raw text plan, plus the parsed `models.Plan` available internally.
- `<space>E` runs EXPLAIN ANALYZE wrapped in `BEGIN; … ; ROLLBACK;` by default — for the mutating statement, the row is **not** persisted.
- `<space>!` runs in a fresh tx with no auto-rollback, so the mutating EXPLAIN ANALYZE actually commits.

### 5.9 NOTICE / WARNING surfacing

**Pre:** Create a fixture function `RAISE NOTICE 'hello'` or use one of the fixture procedures.

**Steps:**
1. Run a statement that emits `NOTICE`/`WARNING`.

**Expected:**
- The notice text is appended to the command-log panel (lines via `OnUIThreadContentOnly`).
- The first notice/warning per `<space>r` / `<space>R` invocation raises a toast; subsequent notices in the same run update the toast's counter rather than spawn new ones.

### 5.10 Query history

**Steps:**
1. Run several different statements.
2. Inspect the history sqlite file at `$XDG_STATE_HOME/dbsavvy/history.sqlite`.

**Expected:**
- Each executed statement is recorded by the silent recorder (bounded channel cap=128, drop-oldest-and-warn on overflow). FTS5 index on the `sql` column is present (driver: pure-Go `modernc.org/sqlite`). UI for history is not yet implemented (future epic).

### 5.11 read_only enforcement

**Pre:** A profile with `read_only: true`.

**Steps:**
1. Open the profile, run `INSERT INTO app.users(name) VALUES ('x');`.

**Expected:**
- Postgres returns the read-only transaction error. The behavior persists across pool-conn recycle (covered by the integration suite).

### 5.12 statement_timeout

**Pre:** A profile with `statement_timeout: "500ms"`.

**Steps:**
1. Open the profile, run `SELECT pg_sleep(2);`.

**Expected:**
- Server-side timeout aborts the query; the result tab is marked errored with the Postgres timeout message.

### 5.13 Grid scrolling and selection

**Steps:**
1. With a multi-row result, use `h`/`j`/`k`/`l` to move the cursor.
2. `<c-d>`/`<c-u>` for half-page scroll; `gg`/`G` to jump to top/bottom.
3. `v` to enter cell visual select; `V` for line visual; `<c-v>` for block visual.
4. Press `<space>gf` to toggle the frozen first column.
5. Press `y` to yank the selection.

**Expected:**
- Cursor and selection update as in vim.
- Yank places TSV-formatted data into the default register (and the system clipboard register when `+`/`*` are targeted).

---

## 6. Vim-style SQL editor (Normal/Insert/Visual modes)

> Note: scope `QUERY_EDITOR`. Mode is tracked in `ModeStore[QUERY_EDITOR]`.

### 6.1 Insert mode entries

**Steps:**
1. From Normal, press `i` (cursor stays), then `<esc>`.
2. Press `a` (cursor moves right one column), then `<esc>`.
3. Press `o` (opens a new line below, enters Insert), then `<esc>`.
4. Press `O` (opens a new line above), then `<esc>`.
5. Press `I` (jumps to first non-blank, enters Insert), then `<esc>`.
6. Press `A` (jumps to line end, enters Insert), then `<esc>`.

**Expected:**
- Each entry positions the cursor correctly and flips `ModeStore[QUERY_EDITOR]` to `ModeInsert`. `<esc>` flips back to `ModeNormal`. Typed characters appear in the buffer through the master `gocui.Editor` (`VimEditor`), which calls `Buffer.Apply(Edit)` and syncs `View.SetContent(buf.String())` + `View.SetCursor`.

### 6.2 Motions (Normal mode)

**Steps:**
1. Use `h`/`j`/`k`/`l`, `w`/`b`/`e`, `W`/`B`/`E`, `0`/`^`/`$`, `gg`/`G`, `{`/`}`, `(`/`)`, `H`/`M`/`L` on a multi-line buffer.
2. Use `5j`, `3w` with counts.

**Expected:**
- Each motion updates the cursor following vim semantics. Counts repeat motions.

### 6.3 Text objects

**Steps:**
1. Place cursor inside `"foo"` and run `vi"` then `va"`.
2. Inside `(a,b,c)` run `vi(` and `va(`.
3. Inside a blank-line-delimited paragraph, run `vip` and `vap`.
4. Inside `{ ... }` run `viB` and `vaB`.
5. Inside a `;`-delimited SQL statement, run `vis` / `vas` (naive splitter — documented limitation: doesn't handle `;` inside string literals).

**Expected:**
- Visual selection covers the inner / around region per vim semantics.

### 6.4 Operators

**Steps:**
1. On a line, run `dd` (delete line), `yy` (yank line), `cc` (change line — enters Insert).
2. With cursor inside `"foo bar"`, run `di"` (delete inside quotes).
3. Run `yi(` then `p` (paste yank in same register).
4. Run `gUiw` (uppercase word), `guiw` (lowercase word).
5. Run `>>` and `<<` (indent right/left, `ShiftWidth = 2`).

**Expected:**
- Operator stashes itself in `RepeatStore.PendingOpID` + flips to `ModeOperatorPending`. Next motion/text-object completes via `applyPending`.
- `cc` flips to Insert after the delete.
- Linewise variants `dd`, `yy`, `cc`, `>>`, `<<` fire when the same operator key is pressed twice.
- `p` pastes from the effective register (`"` by default), respecting linewise/charwise.

### 6.5 Visual + operator composition

**Steps:**
1. From Normal, `v$d` to char-visual-select to end-of-line and delete.
2. From Normal, `Vd` to line-visual the current line and delete.
3. From Normal, `<c-v>` to block-visual a rectangle, then `d`.

**Expected:**
- Operators consume `Buffer.Selection` directly (bypassing op-pending). Visual mode exits before the next save.

### 6.6 Undo / Redo

**Steps:**
1. Make several edits.
2. Press `u` repeatedly to undo.
3. Press `<c-r>` to redo.

**Expected:**
- Each `u` replays the inverse of the most recent `Edit`; each `<c-r>` re-applies along `children[0]` of the UndoTree. Tree is capped at 1000 nodes. No-ops at tree boundaries.

### 6.7 `.` repeat

**Steps:**
1. Run `daw` to delete a word.
2. Move the cursor to a different word.
3. Press `.`.

**Expected:**
- The most recent operator+motion pair is re-resolved from the **current** cursor position (vim semantics — not a replay of the original range). Count and register are reused from the stashed `RepeatStore`.

### 6.8 Marks and jump list

**Steps:**
1. Press `ma` to set mark `a`. Move elsewhere. Press `'a` to jump back.
2. Make multiple long jumps; observe the bounded jump list (cap 100, push-only in MVP — no `<c-o>`/`<c-i>` navigation yet).

**Expected:**
- Mark recall jumps to the recorded position.
- Jump list accumulates entries; bidirectional navigation is **not** implemented (future epic).

### 6.9 Buffer persistence

**Pre:** Connect to a profile (gives a connection id).

**Steps:**
1. Type a multi-line query and make it dirty.
2. Switch focus away from the QUERY_EDITOR (or quit).
3. Re-focus / restart and re-open the same connection.

**Expected:**
- `HandleFocusLost` dispatches a worker job that writes the buffer to `<stateDir>/buffers/<hex(sha256(connID)[:8])>/<uuid>.sql` (file mode `0o600`, dir `0o700`). On-disk format is **raw `.sql`** (no struct serialization).
- `AppState.LastBufferUUIDs[connID]` remembers a single buffer per connection.
- On re-open, the buffer is hydrated from disk and the cursor lands at the start.

### 6.10 Visual `<leader>r` fan-out

**Steps:**
1. With multiple `;`-separated statements in the editor, enter Visual line mode (`V`) and select two-to-five of them.
2. Press `<space>r`.

**Expected:**
- The selection is split by `editor.SplitStatements` (naive `;`-split) and each statement runs in its own result tab, capped at `N=32`. Above the cap a toast aborts the run **before** any statement starts.
- After the run, Visual mode exits before any persistence.

---

## 7. Foundation & cross-cutting checks

### 7.1 i18n fallback

**Pre:** Unset all locale env vars (`LANGUAGE`, `LC_ALL`, `LC_MESSAGES`, `LC_CTYPE`, `LANG`). On darwin this test is skipped because `AppleLocale` overrides env.

**Steps:**
1. Launch `bin/dbsavvy`.

**Expected:**
- A log warning indicates locale fall-back; UI strings render in English.
- Adding a bad JSON overlay under translations falls back to English silently with a log warning. An oversize (>1 MiB) overlay also falls back.

### 7.2 Theme apply

**Steps:**
1. Edit `theme:` in user config (e.g. set `ActiveBorder: yellow`).
2. Press `:reload<cr>`.

**Expected:**
- The focus border immediately renders in the new color (theme snapshot pointer-swapped; consumers call `theme.Current()` per frame).

### 7.3 Logs

**Steps:**
1. Inspect `$XDG_STATE_HOME/dbsavvy/dbsavvy.log` after launching.

**Expected:**
- File present, mode `0600`, parent dir mode `0700`. Single-instance assumed.
- Sensitive data (passwords) is **never** logged.

### 7.4 Credential resolution (driver layer)

**Pre:** Profiles exercising each path: inline `password:`, `password_command:`, keyring (file backend), pgpass (`~/.pgpass`), and a profile that would prompt.

**Steps:**
1. For each profile, open the connection from the CONNECTIONS rail.

**Expected:**
- Inline / password_command / keyring / pgpass succeed and open the session.
- A profile that would prompt is rejected by `TUIRefusePrompter` with `errInteractivePromptNotSupported` (interactive password prompting is intentionally not wired into the TUI in this epic).
- `password_command` falls back through `$SHELL → /bin/bash → /bin/sh` on Unix. Stdin is closed; stderr is surfaced; stdout is trimmed.

### 7.5 read-only persistence across pool recycle

**Steps:**
1. Open a `read_only: true` profile.
2. Force a backend recycle (e.g. `SELECT pg_terminate_backend(pg_backend_pid())` in another session targeting the same backend — covered by the integration test).
3. Re-run an `INSERT` to confirm.

**Expected:**
- `default_transaction_read_only = on` is reset by `AfterConnect` on every recycle; the `INSERT` continues to fail with the read-only error.

---

## 8. Status, focus, and visual polish (from dbsavvy-tro)

### 8.1 Focus indicator

**Steps:**
1. Cycle focus between rails with `<tab>` or `1`/`2`/`3`/`4`.

**Expected:**
- Only the focused view's frame is rendered in `theme.ActiveBorder`. All others render `theme.InactiveBorder`. Updated each Layout pass (after gocui resets FrameColor).

### 8.2 Caret in COMMAND_LINE

**Steps:**
1. Press `:` and start typing.

**Expected:**
- A caret renders at the typing position. The `:` prompt fg uses `PromptFg` (added in `dbsavvy-tro.12`) and is visibly brighter than the default fg.

### 8.3 Toast renders to status slot

**Steps:**
1. Trigger any toast (e.g. `:reload`).

**Expected:**
- The toast text replaces the default status text in the AppStatus view for its TTL. Content is sanitized via `config.SafeText` so non-config-sourced error strings cannot inject control bytes.

### 8.4 Cheatsheet glyph & chord prefix

**Steps:**
1. Open the cheatsheet (`?`).

**Expected:**
- The legend glyph and the in-row glyph match (`✱`/`*` unified per `dbsavvy-tro.10`).
- Multi-key chords show their full prefix (e.g. `<leader>q`, not just `q`) per `dbsavvy-tro.9`.

### 8.5 Connections rail truncation fix

**Steps:**
1. With no connections, verify the empty-state hint.

**Expected:**
- The hint reads the full `Press a to add a connection` (no truncation regression from `dbsavvy-tro.8`).

### 8.6 No top-line artifact

**Steps:**
1. Resize the terminal a few times, switch between popups and the main layout.

**Expected:**
- No intermittent thin blue line at the canvas top (verified in `dbsavvy-tro.13`).

---

## 9. Smoke walkthrough (sanity loop)

Run this whenever you've touched the keybinding, layout, or editor surfaces.

1. Launch app → CONNECTIONS rail focused, focus border visible, status mode label = `N`.
2. Press `a` → Driver popup. `<esc>` to cancel.
3. Open an existing connection → schemas rail populates with `app`, `reporting`.
4. `<tab>` through SCHEMAS → TABLES → COLUMNS → INDEXES; each rail loads.
5. Press `H` on a schema → it disappears. `U` → it returns.
6. Press `?` → cheatsheet renders with every action's chord and description.
7. Focus the QUERY_EDITOR, press `i`, type `SELECT 1; SELECT 2;`, `<esc>`, `<space>R` → two result tabs open.
8. Press `<space>1` then `<space>2` to jump; `gt`/`gT` to cycle; `<space>X` to close; `<space>=` to pin.
9. Type `SELECT pg_sleep(10);` and `<space>r`, then switch to SCHEMAS → tab marked `(cancelled, …)`.
10. Press `:reload<cr>` → toast appears in status bar, no stuck which-key.
11. Press `:q<cr>` → app exits cleanly.

If every step passes with no regressions, the closed-epics surface is healthy.

---

## Out of scope (deferred — do **not** test against these)

The following are explicitly **not** implemented yet and should not appear in QA results:

- Tree-sitter SQL highlighting, `gqq` SQL formatter, SQL-string-literal-aware splitter
- Macros (`q{reg}` record, `@{reg}` replay)
- Search / substitute (`/`, `?`, `n`, `N`, `*`, `#`, `:s/foo/bar/g`)
- `R` Replace mode
- Bidirectional jumplist navigation (`<c-o>` / `<c-i>`)
- In-grid filter / sort / column-hide / expanded-mode / pagination tail prefetch
- EXPLAIN tree UI (parsed plan exists; tree renderer is a later epic)
- Transaction submenu UI / `[TX]` indicator / quit-with-tx confirmation / lost-connection recovery dialog
- `:` ex-command routing beyond `:reload`, `:q`, `:quit`
- `search_path` and `statement_timeout` runtime overrides via `:`
- Inline cell editing, FK navigation, schema-aware completion
- Custom commands (`★` shell runtime)
- Edit/delete connection flows (only add is implemented)
- Connection-add suggestions/autocomplete
- Multi-buffer browser (`:buffers`)
- Theme variants beyond `default-dark`
- Non-English translation packs
- Windows support
- Multi-process logrus rotation
- Cell-edit single-line buffer (dropped from MVP, scheduled for E10)
- Enter on tables/columns/indexes (currently a stub no-op — tracked under `dbsavvy-tro.16`)
