# Keypress Dispatch Decision Tree

How a single keystroke travels from the terminal to a `Command` handler, and
how the active **mode** (Normal / Insert / Visual / Command), the focused
**scope** (`ContextKey`), and the view's **editable** flag steer that journey.

All `path:line` anchors below were checked against the working tree.

## Layers

```
terminal
   |
   v
gocui runtime (per-view dispatch)
   |
   |-- editable view  -> master gocui.Editor.Edit(...)   ----.
   |                                                          |
   |-- non-editable   -> per-key SetKeybinding shim       ---'
   |                        (one binding per trie-root key)  |
   |                                                          v
   |                                          masterEditor.Edit / .Dispatch
   |                                       (pkg/gui/orchestrator/master_editor.go)
   |                                                          |
   |                                                          v
   |                                          matcher.Dispatch(scope, key)
   |                                              (pkg/gui/keys/matcher.go)
   |                                                          |
   |          .-----------------------------------------------+
   |          |                                               |
   |          v                                               v
   |   Attempt 1: (mode, scope) trie               Attempt 2: (mode, GLOBAL) trie
   |   Lookup(pending + key)                        Lookup(key) FRESH
   |          |                                               |
   |   found? +---- yes ----> handleLookup ----.       found? +-- yes --> handleLookup
   |          |                                |              |
   |          '-- no: drop pending,            |              '-- no ----.
   |              fall through to GLOBAL -------'                         |
   |                                                                      v
   |                                                       Passthrough (Insert/Command
   |                                                       + printable / editor-safe)
   |                                                       else FellThrough
   |                                  .------------------------------'
   |                                  v
   |                          invokeHandler(cmd, ...)
   |                       (Disabled gate -> toast / skip;
   |                        Handler error -> sanitized toast,
   |                        swallowed unless gocui.ErrQuit)
   |                                  |
   |                                  v
   |                          cmd.Handler(ExecCtx{Count, Register, Mode, Scope})
```

## Layer-by-layer

### 1. gocui -> editor or per-key shim (the editable / non-editable split)

The orchestrator decides at wiring time *how* each view feeds the matcher,
based on `ContextKey.IsEditable()`:

- `pkg/gui/types/context.go:151` — `func (k ContextKey) IsEditable() bool`
  returns true for `COMMAND_LINE`, `QUERY_EDITOR`, `PROMPT`, `CELL_EDITOR`.
- `pkg/gui/orchestrator/gui.go:1604` — `func (g *Gui) installKeyDispatch(...)`
  drives the split.
- `pkg/gui/orchestrator/gui.go:1619` — `if key.IsEditable() {` installs a
  **master `gocui.Editor`** (every keystroke routed through the matcher;
  `QUERY_EDITOR` gets a `VimEditor`, others a `masterEditor`).
- `pkg/gui/orchestrator/gui.go:1643` — non-editable branch installs **one
  `SetKeybinding` shim per trie-root key** (`installShimsForScope`), because
  gocui drops char-key bindings on editable views and bracketed-paste on
  non-editable views — see the rationale comment at
  `pkg/gui/types/context.go:134`.
- `pkg/gui/orchestrator/gui.go:1651` — GLOBAL roots are also installed with an
  empty viewname so they fire from any focused view.

### 2. masterEditor.Edit -> matcher

`pkg/gui/orchestrator/master_editor.go:104` — `func (e *masterEditor) Edit(v
*gocui.View, key gocui.Key) bool` (lines 104-121):

- Decodes the gocui key (`keys.KeyFromGocui`) and calls
  `e.matcher.Dispatch(e.scope, k)` (`master_editor.go:110`).
- The bool return follows gocui's convention: `true` = handled / do not
  propagate.
- A handler returning `gocui.ErrQuit` is re-scheduled on the MainLoop via
  `Gui.Update` (`master_editor.go:115`) because `Edit` can only return a bool.
- `master_editor.go:127` — `func (e *masterEditor) Dispatch(...)` is the
  view-less twin used by the test recorder; it shares the same
  `e.matcher.Dispatch` call (`master_editor.go:133`).

### 3. matcher.Dispatch -> trie + GLOBAL fall-through

`pkg/gui/keys/matcher.go:288` — `func (m *Matcher) Dispatch(scope
types.ContextKey, k Key) (DispatchResult, error)` (lines 288-436). The mode is
resolved first (`matcher.go:289`, `mode := m.modes.Get(scope)`), then:

**Mode x scope interaction inside Dispatch:**

- **Insert / Command fast path** (`matcher.go:296`): a printable rune with no
  pending / count / register and no binding at `(mode, scope)` *or*
  `(mode, GLOBAL)` returns `Passthrough` — it is treated as text, not a
  command. Count collection is disabled in these modes.
- **Register prefix** (`matcher.go:314`): a one-key `"` buffer captures the
  next rune as the register name.
- **Count collection** (`matcher.go:336`): Normal / Visual modes only; `1`-`9`
  start a count, continuing digits extend it, unless the digit is itself a
  bound leaf.
- **Register-prefix start** (`matcher.go:360`): idle `"` opens the register
  prompt, guarded so a user-bound `"` is not stolen.
- **Attempt 1 — scope trie** (`matcher.go:382`): `Lookup(pending + key)` at
  `(mode, scope)`; on a `Found` result it calls `handleLookup`
  (`matcher.go:386`). A miss with non-empty pending drops the pending chord and
  records `hadChordPartial` (`matcher.go:391`).
- **Attempt 2 — GLOBAL fall-through** (`matcher.go:398`): re-lookup with the key
  **fresh** at `(mode, GLOBAL)`; on `Found`, `handleLookup` runs with scope =
  `types.GLOBAL` (`matcher.go:403`).
- **No match** (`matcher.go:413`): Insert / Command + printable-or-editor-safe
  key returns `Passthrough`; otherwise count / register are dropped and the
  call returns `FellThrough` (`matcher.go:435`), hiding any which-key popup left
  by the abandoned prefix.

`Mode` itself is the bitmask defined at `pkg/gui/types/mode.go:8`
(`type Mode uint32`): `ModeNormal` is the zero sentinel
(`pkg/gui/types/mode.go:13`), `ModeInsert` (`mode.go:22`), `ModeVisual`
(`mode.go:25`), and `ModeCommand` (`mode.go:37`) are distinct bits.

### 4. handleLookup -> invokeHandler -> registry -> handler

- `pkg/gui/keys/matcher.go:440` — `func (m *Matcher) handleLookup(...)`
  resolves a `Found` lookup, releasing `m.mu` before running the handler;
  an unambiguous leaf fires immediately and calls `invokeHandler`
  (`matcher.go:460`).
- `pkg/gui/keys/matcher.go:545` — `func (m *Matcher) invokeHandler(cmd
  *commands.Command, scope, mode, count, reg) (DispatchResult, error)`:
  - Recovers handler panics into an error (`matcher.go:546`).
  - **Disabled gate** (`matcher.go:569`): `cmd.Disabled(ctx)` true emits a
    toast and returns `Dispatched` without running the handler.
  - **Error boundary** (`matcher.go:573`): a non-nil handler error is converted
    to a sanitized toast and swallowed — *except* `gocui.ErrQuit`, which is
    propagated (`matcher.go:579`) so the `:q` quit path can unwind.
  - The handler runs with `commands.ExecCtx{Count, Register, Mode, Scope}`
    (`matcher.go:559`).

## Summary of the decision axes

| Axis | Effect |
| --- | --- |
| **editable** (`IsEditable`) | Master `gocui.Editor` (all keys -> matcher) vs per-key `SetKeybinding` shims. |
| **mode** (Insert/Command vs Normal/Visual) | Insert/Command: printable runes passthrough, no counts. Normal/Visual: count + register collection. |
| **scope** (`ContextKey`) | Attempt 1 uses the focused scope's trie; unmatched keys fall through to the GLOBAL trie (Attempt 2). |
