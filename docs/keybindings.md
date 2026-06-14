# Customizing keybindings

dbsavvy lets you remap almost every key from a YAML config file. This page
explains the config schema, how user bindings interact with the shipped
defaults, how to discover the action IDs you can bind, and which keys are
deliberately fixed and cannot be changed.

> Config file location: `$XDG_CONFIG_HOME/dbsavvy/config.yml`
> (typically `~/.config/dbsavvy/config.yml`). See the
> [Config File notes](review/cross_cutting.md#config-file) for how the file
> is created and overlaid onto the built-in defaults.

---

## The `keybindings:` schema

Keybindings live under the top-level `keybindings:` list. Each entry is one
binding:

```yaml
keybindings:
  - mode: n            # which editor mode(s) the binding is active in
    scope: query_editor # which context (or "global"/"all") it applies to
    key: <leader>r      # the key or chord sequence
    action: query.run   # the action ID to invoke
    description: Run query # optional, shown in help/cheatsheet
```

Field reference:

| Field         | Meaning |
|---------------|---------|
| `mode`        | One mode token, or a comma-separated subset of `n,i,v,V,<c-v>,o,x,c` (normal, insert, visual, visual-line, visual-block, operator-pending, and the command-line variants). |
| `scope`       | A context name (e.g. `query_editor`, `schemas`, `confirmation`), the literal `global`, or the literal `all` (see [Per-context scope vs `all`](#per-context-scope-vs-all)). |
| `key`         | A single key or chord label, e.g. `<leader>tr`, `gg`, `<c-w>v`, `<cr>`, `<esc>`. |
| `action`      | The action ID to run (see [Discovering action IDs](#discovering-action-ids)). |
| `command`     | A shell command string. **Not yet wired** — see the note below. |
| `description` | Optional human-readable label shown in the cheatsheet and which-key. |

You must set **exactly one** of `action:` or `command:` per entry
(they are mutually exclusive).

> **`command:` is not yet implemented.** The schema accepts a `command:`
> field, and a binding that uses it is recorded so the cheatsheet renders
> correctly, but custom shell-command **dispatch is not wired** — pressing
> the key does nothing yet. Use `action:` for anything you want to actually
> run today.

---

## User bindings replace the defaults wholesale

When you provide a `keybindings:` list, it **replaces** the default
keybinding list entirely. There is **no element-wise merge** — your list is
not added on top of the defaults, it becomes the whole list.

That means: if you write a `keybindings:` section, you must re-include every
binding you still want, **including a quit binding**. Omitting `app.quit`
leaves the app with no key-driven exit — and dbsavvy now refuses to start in
that state (see [Recovering from a lockout](#recovering-from-a-lockout)).

(The shipped defaults that do *not* come from this list — e.g. the `:`
command-line and vim editor motions — are layered in separately and are not
removed by your `keybindings:` list. Only the entries that would otherwise
appear in the default `keybindings:` list are replaced.)

---

## Per-context scope vs `all`

`scope:` selects where a binding is active.

- **A context name** (e.g. `scope: query_editor`, `scope: schemas`,
  `scope: confirmation`) targets exactly that context. The binding only
  fires when that context is focused.
- **`scope: global`** registers the binding at the global fall-through
  layer, so it can fire from any context that doesn't already consume the
  key.
- **`scope: all`** fans the binding out to every non-popup context at once.

What `scope: all` reaches and what it does **not**:

- It **does** reach the side rails and main panes, and notably also reaches
  `connection_manager` and `cheatsheet`.
- It does **not** reach popups (menus, prompts, confirmation/commit dialogs,
  and similar modal overlays).
- It does **not** reach the transient overlays **which-key** and the
  **limit** popup (and the hide-columns overlay) — these are explicitly
  excluded from `scope: all` even though they would otherwise look
  eligible.

So `scope: all` is a convenient way to bind a key across all your normal
navigation/editing surfaces without enumerating each context, while leaving
modal/transient UI untouched.

---

## Discovering action IDs

Action IDs are stable, dot-namespaced strings of the form `family.name`
(or `family.subfamily.name`), for example `app.quit`, `motion.line_down`,
`selection.down`, `query.run`, `tx.begin`.

To find the ID you want:

- Open the **in-app cheatsheet** (`help.cheatsheet`, default `?`) to see the
  active bindings and their actions.
- Browse the **action catalog** in source at
  `pkg/gui/commands/actions.go` — every bindable action ID is declared there
  with a comment describing what it does and its default key.

When you set `action:` to an ID that does not exist, config validation
rejects it at startup with an "unknown action" error, so typos are caught
early.

---

## Unbinding a default

To disable a default key without binding anything else to it, bind it to the
unbind sentinel `<nop>`:

```yaml
keybindings:
  - mode: n
    scope: query_editor
    key: <leader>e
    action: <nop>   # disables <leader>e in the query editor
```

`<nop>` is the only non-action value accepted by `action:`; it is a
sentinel meaning "do nothing", not a real action ID.

---

## Remapping a vim motion once (it composes everywhere)

The query editor is a vim-like modal editor. Its motions, operators, and
text objects normally span several modes (Normal, operator-pending, and the
visual variants). You do **not** have to enumerate those modes when you
remap one.

**Write a single Normal-mode binding for a motion, and it auto-composes**
with operators, counts, and visual mode. For example, remapping
`motion.line_down` from `j` to `n` in Normal mode makes all of these work
with no further configuration:

```yaml
keybindings:
  - mode: n
    scope: query_editor
    key: n
    action: motion.line_down
```

- `n` moves the cursor down one line.
- `dn` deletes one line down (operator + remapped motion).
- `3n` moves down three lines (count + remapped motion).
- visual `n` extends the selection down one line.
- `.` after `dn` repeats by action, re-running the same motion.

Two consequences to know:

- **The original shipped key goes inert.** After remapping
  `motion.line_down` to `n`, both `j` and `dj` stop driving that motion —
  only `n` / `dn` / `3n` / visual-`n` do.
- **Reserved targets are rejected.** Remapping a cross-mode vim motion onto
  a bare digit `0`–`9` or onto the register prefix `"` is rejected wholesale
  (the shipped default is left intact), because those keys form the vim
  count and register grammar. See
  [Keys you cannot rebind](#keys-you-cannot-rebind).

This auto-composition applies to the cross-mode vim grammar: the
`motion.*`, `operator.*`, `textobject.*`, and visual `selection.extend`
families. Normal-only actions (e.g. `insert.enter`, `editor.undo`,
`app.quit`) take the ordinary single-binding path and are not freed or
propagated.

The full set of remappable vim action IDs:

```action-ids
motion.char_left
motion.char_right
motion.line_down
motion.line_up
motion.word_next
motion.word_prev
motion.word_end
motion.word_next_big
motion.word_prev_big
motion.word_end_big
motion.line_start
motion.line_first_nonblank
motion.line_end
motion.buffer_start
motion.buffer_end
motion.paragraph_prev
motion.paragraph_next
motion.sentence_prev
motion.sentence_next
motion.screen_top
motion.screen_middle
motion.screen_bottom
operator.delete
operator.yank
operator.change
operator.upper
operator.lower
operator.indent_right
operator.indent_left
operator.delete_eol
textobject.inner_word
textobject.around_word
textobject.inner_word_big
textobject.around_word_big
textobject.inner_quote_double
textobject.around_quote_double
textobject.inner_quote_single
textobject.around_quote_single
textobject.inner_paren
textobject.around_paren
textobject.inner_bracket
textobject.around_bracket
textobject.inner_brace
textobject.around_brace
textobject.inner_paragraph
textobject.around_paragraph
textobject.inner_statement
textobject.around_statement
visual.enter
visual.enter_line
visual.enter_block
visual.exit
selection.extend
insert.enter
insert.append
insert.open_below
insert.open_above
insert.first_nonblank
insert.append_end
mode.normal
editor.undo
editor.redo
editor.repeat
editor.paste
```

---

## Recovering from a lockout

Because user `keybindings:` replace the defaults wholesale, it is possible
to write a config with no exit. dbsavvy guards against this:

- **A config with no `app.quit` binding fails startup.** Validation is a
  hard error (not a warning): the app refuses to open the TUI and prints a
  clear message to stderr naming `app.quit` and the path to your config
  file. This happens *before* the alternate screen is entered, so the
  message is visible.

If you ever find yourself stuck, these escapes always work:

- **`:q` / `:quit` always exit.** The command-line and its quit commands are
  shipped defaults that survive wholesale replacement of `keybindings:`.
- **Ctrl-C always quits, and cannot be rebound.** Ctrl-C is reserved as an
  emergency exit and is intercepted before the keybinding map is consulted.
  A user binding that maps `<c-c>` to some other action is intentionally
  shadowed — the emergency exit wins.
- **External `kill -INT` / `kill -TERM`** from another process also exits.

To recover permanently, **edit or remove your config file**
(`~/.config/dbsavvy/config.yml`) to restore a valid set of bindings, then
restart. There is no `--ignore-config` flag.

---

## Keys you cannot rebind

A few keys are fixed by design and are not user-configurable:

- **Ctrl-C** — reserved as the emergency quit. It always quits from any
  context and cannot be remapped (see above).
- **The first-run tip's Esc / Enter** — the one-time welcome tip is
  dismissed with Esc or Enter; this is hardcoded.
- **Escape-as-abort** — Esc consistently aborts the current
  popup/chord/overlay. Transient overlays such as which-key are torn down on
  Esc, and this abort behavior is not remappable.
- **The vim register prefix `"` and the count digits `0`–`9`** — these form
  the vim register and count grammar in the query editor. They are
  intentionally non-configurable, and remapping a motion onto them is
  rejected (see [Remapping a vim motion](#remapping-a-vim-motion-once-it-composes-everywhere)).

Also note that **custom shell-command dispatch (`command:`) is not yet
implemented** — see the schema section above.

---

## Worked examples

Each snippet below is a copy-pasteable `keybindings:` entry. Remember that a
real config's `keybindings:` list replaces the defaults wholesale, so a
complete config must also re-include a quit binding (and anything else you
want to keep).

### Rail (side-list) — Schemas / Tables

Make `r` refresh and `<cr>` confirm on the schemas rail:

```yaml
keybindings:
  - mode: n
    scope: schemas
    key: r
    action: rail.refresh
  - mode: n
    scope: schemas
    key: <cr>
    action: list.confirm
```

### Editor (query editor / vim motion)

Run the current statement with `<leader>r`, and remap the down motion to `n`
(auto-composes with operators/counts/visual, per above):

```yaml
keybindings:
  - mode: n
    scope: query_editor
    key: <leader>r
    action: query.run
  - mode: n
    scope: query_editor
    key: n
    action: motion.line_down
```

### Dialog (confirmation popup)

Bind `y` to confirm and `n` to cancel in the confirmation dialog:

```yaml
keybindings:
  - mode: n
    scope: confirmation
    key: y
    action: confirm.yes
  - mode: n
    scope: confirmation
    key: n
    action: confirm.no
```

---

## See also

- [Config File notes](review/cross_cutting.md#config-file) — how the config
  file is located, created, and overlaid onto defaults.
- [Command line (`:` commands)](review/command_line.md#recognised--commands) —
  the shipped `:q`, `:quit`, and `:reload` commands.
</content>
</invoke>
