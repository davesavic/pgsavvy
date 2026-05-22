# Query Editor

## Purpose
Vim-style modal SQL editor (scope `QUERY_EDITOR`) where the user composes and runs SQL against the active connection.

## Visible state
- Multi-line SQL text from canonical `*editor.Buffer` (mirrored into the gocui view).
- Cursor (line/col).
- Active visual Selection (Visual modes).
- Status-bar mode indicator (N / I / V / V-L / V-B / Op-Pending).

## Modes / states
- `ModeNormal` (default after focus)
- `ModeInsert` — printable runes / Enter / Backspace mutate buffer
- `ModeVisual`, `ModeVisualLine`, `ModeVisualBlock` — selection extension; operators consume `Buffer.Selection`
- `ModeOperatorPending` — second key after an operator completes the range

## Keybindings

### Normal-mode motions (also valid in Op-Pending and all Visual modes; counts supported, e.g. `5j`, `3w`)
- Char: `h` (left), `l` (right), `j` (down), `k` (up)
- Word: `w` / `b` / `e` (word fwd/back/end), `W` / `B` / `E` (WORD fwd/back/end)
- Line: `0` (start), `^` (first non-blank), `$` (end)
- Buffer jumps: `gg` (start), `G` (end) — pushed to jump list
- Paragraph / sentence: `{` / `}` (paragraph), `(` / `)` (sentence)
- Screen (buffer-relative stubs): `H` (top), `M` (middle), `L` (bottom)

### Insert-mode entries (from Normal)
- `i` insert at cursor, `a` append after cursor, `o` open line below, `O` open line above, `I` insert at first non-blank, `A` append at line end

### Insert-mode behavior
Printable runes insert at cursor; `<cr>` newline; `<bs>` deletes rune before cursor (joins lines at col 0). Other special keys are dropped. `<esc>` returns to Normal.

### Operators (Normal / Visual / Op-Pending)
- `d` delete, `y` yank, `c` change (flips to Insert)
- `gU` uppercase, `gu` lowercase
- `>` indent right, `<` indent left (`ShiftWidth = 2`)
- Doubled-shortcut linewise: `dd`, `yy`, `cc`, `>>`, `<<` (count-aware)

### Text objects (Op-Pending / Visual / V-Line)
- Quotes: `i"` / `a"`, `i'` / `a'`
- Brackets: `i(` / `a(`, `i[` / `a[`, `i{` / `a{`, `iB` / `aB` (alias of `i{` / `a{`)
- Paragraph: `ip` / `ap`
- Statement (naive `;`-split — does **not** handle quoted `;`): `is` / `as`

### Visual mode (Normal-only entry; exit via `<esc>`)
- `v` char-wise, `V` line-wise, `<c-v>` block-wise

### Edit / history
- `p` paste after cursor (line-wise vs char-wise inferred from trailing `\n`)
- `u` undo, `<c-r>` redo (UndoTree capped at 1000 nodes)
- `.` repeat last edit (re-resolves motion/text-object from current cursor — vim semantics)

### Registers (count + register prefixes parsed by Matcher)
- `"a..z` named registers, `"0`, etc. — backed by in-memory store
- `"+` / `"*` system clipboard — fall back to in-memory store with one-shot "not yet wired to system clipboard" toast per session
- `"` default unnamed register

### Marks
- `m{a-z}` set mark
- Recall binding `'{a-z}` is documented but **not published** in `GetKeybindings`
- Jump-list push wired; bidirectional navigation `<c-o>` / `<c-i>` not bound

### Query execution (leader = `<space>`)
- `<leader>r` — run statement under cursor (also fans out visual-selection statements via `SplitStatements`, capped at 32; over-cap toasts and aborts)
- `<leader>R` — run every statement in buffer (sequential, one tab per statement)
- `<leader>e` — EXPLAIN under cursor
- `<leader>E` — EXPLAIN ANALYZE (wrapped in `BEGIN; … ROLLBACK;`)
- `<leader>!` — run statement in a fresh transaction with no auto-rollback
- `<leader>x` — cancel active query (disabled when `Capabilities.HasLiveCancel = false`)
- Rail-switch escape hatches: digits `1`..`5` + `<tab>`

## Mouse interactions
- None on the editor view itself (mouse focus-click works via the orchestrator).

## Persisted state
- Buffer auto-saved on `HandleFocusLost` when dirty: `<stateDir>/buffers/<hex(sha256(connID)[:8])>/<uuid>.sql` (file `0o600`, dir `0o700`, raw `.sql`)
- `AppState.LastBufferUUIDs[connID]` — one persisted buffer per connection (loaded post-Connect)
- Jump list bounded at 100 entries (push-only)
- `RepeatStore` (last op / motion / text-object / count / register) is per-context, NOT persisted

## Status / toasts / busy
- "no statement under cursor" / "no active connection" / "no statements found" / "no selection" toasts
- "visual run: N statements exceeds cap 32; narrow selection" hard-abort toast
- One-shot "register + / * not yet wired to system clipboard" toast
- NOTICE / WARNING from runs routed to command-log panel; first per run raises a counter-style toast
- Disabled-binding toast for `<leader>x` when driver lacks live cancel

## Gaps / TODOs / dead-looking code
- `f` / `F` / `t` / `T` / `;` / `,` char-search motions — NOT implemented
- `/` / `?` / `n` / `N` / `*` / `#` search and `:s/.../.../g` substitute — NOT implemented (`n` / `N` are bound to result-grid filter navigation instead)
- `R` Replace mode — NOT implemented
- `r{char}` single-char replace — NOT implemented
- `x` / `X` (delete-char shortcuts) — NOT bound (use `dl` / `dh`)
- Macros `q{reg}` / `@{reg}` — NOT implemented
- `<c-o>` / `<c-i>` jump-list nav — NOT implemented (push-only)
- `'a..z` mark-recall — set works, recall binding not published
- `+` / `*` system clipboard register — falls back to in-memory; one-shot toast
- Ex commands: only `:q`, `:quit`, `:reload` wired; no `:w` / `:s` / `:buffers` / `:set`
- Tree-sitter SQL highlighting — deferred
- `gqq` SQL formatter — deferred
- SQL-string-literal-aware statement splitter — naive `;`-split (documented limitation)
