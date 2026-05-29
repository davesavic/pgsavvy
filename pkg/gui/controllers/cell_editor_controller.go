package controllers

import (
	"fmt"
	"time"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// Package-level ActionID aliases. The canonical constants now live in
// pkg/gui/commands/actions.go (upstreamed by Z1 Phase A,
// dbsavvy-bwq.23); these aliases retain the original
// controllers.CellEdit* names so existing callers (notably this
// package's tests) keep compiling. New code should reference commands.*
// directly.
const (
	CellEditEnter           = commands.CellEditEnter
	CellEditCommit          = commands.CellEditCommit
	CellEditDiscard         = commands.CellEditDiscard
	CellEditSetNull         = commands.CellEditSetNull
	CellEditExprNow         = commands.CellEditExprNow
	CellEditExprCurrentDate = commands.CellEditExprCurrentDate
	CellEditExprPrompt      = commands.CellEditExprPrompt
)

// GridStatePicker is the narrow read-only surface CellEditorController
// queries to decide whether `i` is enabled and, on Enter, what to
// pre-seed the popup with. The orchestrator wires this to a closure
// over the active result tab's grid.View + RunHandle (Z1).
//
// Every method must be safe to call from the gocui MainLoop. Nil-safety
// is the controller's responsibility — a nil picker disables `i` with
// "no active result grid".
type GridStatePicker interface {
	// Editable reports whether the active grid was determined inline-
	// editable by F2's introspection pass. False keeps `i` disabled
	// with DisabledReason() as the user-facing reason.
	Editable() bool

	// StreamBlocksEdit reports whether the active tab's lifecycle phase
	// has no stable buffer to edit against. ADR-18 (revised) forbids
	// inline edits only while no usable buffer exists — StateQueued (no
	// rows opened) and StateSorting (re-run cleared the buffer). Edits
	// ARE permitted during StateRunning: rows are buffered, appends are
	// append-only, and pending edits are PK-keyed, so editing a buffered
	// row is safe while more rows still stream in. A1 surfaces "wait for
	// current stream to finish" when this is true.
	StreamBlocksEdit() bool

	// SupportsInlineEdit reports the active driver's capability
	// (drivers.Capabilities.SupportsInlineEdit). False is a hard
	// disable with "driver does not support inline edit".
	SupportsInlineEdit() bool

	// IsReadOnly reports whether the active connection profile is
	// flagged read-only. True is a hard disable with "read-only
	// connection".
	IsReadOnly() bool

	// DisabledReason returns the frozen reason string F2 stamped on
	// the active grid at introspection time. Used when Editable()
	// is false. May be empty; the controller falls back to a generic
	// "not editable" label in that case.
	DisabledReason() string

	// CellSnapshot returns the value, column metadata, and primary-key
	// values for the cell currently under the grid cursor. ok=false
	// means no cell is selectable (empty result, no cursor, PK not
	// resolvable) — Enter no-ops with a toast in that case.
	//
	// The returned primaryKey slice is the caller's to retain; the
	// picker MUST return a fresh slice each call (the controller
	// stashes it on the CellEditorContext for later WHERE-clause
	// reconstruction).
	CellSnapshot() (value any, column models.ColumnMeta, primaryKey []any, ok bool)

	// FormatForEdit returns the string form of value the popup pre-
	// seeds the buffer with. Mirrors the cell's on-screen rendering
	// (so the user starts editing what they see). nil values render
	// as "" so backspace doesn't have to skip "NULL".
	FormatForEdit(value any) string
}

// PendingEditStore is the narrow write surface CellEditorController
// uses to stage / discard edits. *models.PendingEditSet satisfies this
// structurally; the interface keeps the controller free of having to
// know which set is "active" (Z1 resolves the active table's set per
// dispatch).
//
// A nil store disables commit + discard with no-op semantics so unit
// tests that skip the wiring still pass.
type PendingEditStore interface {
	Add(e models.PendingEdit) error
	Remove(pk []any, col string)
	// HasEdit reports whether (pk, col) already has a staged edit.
	// Drives the "<c-c> on dirty cell removes + toasts" branch.
	HasEdit(pk []any, col string) bool
}

// FocusPopper is the narrow focus-stack surface the controller calls
// to dismiss the CELL_EDITOR popup. *gui.ContextTree satisfies it.
// Kept as an interface so the controller stays free of the pkg/gui
// import (controllers must not import the orchestrator).
type FocusPopper interface {
	Pop() error
	// Push promotes the supplied context onto the focus stack. Used
	// by the Enter handler to surface the CELL_EDITOR popup.
	Push(ctx types.IBaseContext) error
}

// CellEditorController owns the inline cell-edit lifecycle bindings:
//
//   - `i` on RESULT_GRID: Enter → push CELL_EDITOR (preconditions checked).
//   - `<cr>` / `<esc>` on CELL_EDITOR: Commit → record PendingEdit if changed.
//   - `<c-c>` on CELL_EDITOR: Discard → pop + (on dirty) remove + toast.
//
// SetNull / Expr* bindings are declared so the user-visible chord
// surface is stable from day one; their handlers are reserved for A2
// (dbsavvy-bwq.5) and route to nil dispatchers until then.
//
// Concurrency: every handler runs on the gocui MainLoop. No internal
// locking; the GridStatePicker / PendingEditStore implementations own
// their own synchronisation.
type CellEditorController struct {
	baseController

	ctx    *guicontext.CellEditorContext
	tree   FocusPopper
	picker GridStatePicker
	store  PendingEditStore
}

// NewCellEditorController constructs the controller. All four optional
// collaborators may be nil during unit tests; each handler nil-checks
// before dispatching. Production wiring (Z1) supplies the live context,
// focus-stack tree, grid picker, and PendingEditSet store.
func NewCellEditorController(
	c *common.Common,
	core CoreDeps,
	ui UIDeps,
	ctx *guicontext.CellEditorContext,
	tree FocusPopper,
	picker GridStatePicker,
	store PendingEditStore,
) *CellEditorController {
	return &CellEditorController{
		baseController: newBase(c, HelperBag{CoreDeps: core, UIDeps: ui}),
		ctx:            ctx,
		tree:           tree,
		picker:         picker,
		store:          store,
	}
}

// SetPicker swaps the GridStatePicker post-construction. Z1 wires the
// picker after the result-tabs helper is built (controllers are
// instantiated earlier than helpers); this setter avoids a circular
// dependency at boot.
func (e *CellEditorController) SetPicker(p GridStatePicker) { e.picker = p }

// SetStore swaps the PendingEditStore post-construction. Same rationale
// as SetPicker — the active store is per-table and resolved lazily.
func (e *CellEditorController) SetStore(s PendingEditStore) { e.store = s }

// SetTree swaps the FocusPopper post-construction. The orchestrator
// builds the ContextTree after every controller, so wiring lands here
// once the tree is live.
func (e *CellEditorController) SetTree(t FocusPopper) { e.tree = t }

// Enter is the `i` handler on RESULT_GRID. Validates all preconditions
// in priority order (read_only → driver-cap → streaming → editable);
// the first failing check disables dispatch via the GetDisabled
// predicate on the registered command. When all gates pass, snapshots
// the cursor cell and pushes the CELL_EDITOR popup pre-seeded with
// the cell value formatted for edit.
//
// Returning nil for a passed-precondition handler that just no-ops
// (e.g. nil tree in tests) is intentional — disabled-reason surfacing
// is the Matcher's job via the GetDisabled predicate, not this method.
func (e *CellEditorController) Enter(_ commands.ExecCtx) error {
	if reason, disabled := e.enterDisabled(); disabled {
		// In production the Matcher consults GetDisabled BEFORE
		// invoking the handler, so reaching this branch implies the
		// caller bypassed the gate (test path). Fail loudly only if a
		// logger is wired — staying silent is consistent with the
		// rest of the controllers when collaborators are missing.
		if e.helpers.Logger != nil {
			e.helpers.Logger.Debug(
				fmt.Sprintf("cell.edit.enter dispatched while disabled: %s", reason),
			)
		}
		return nil
	}
	if e.picker == nil || e.ctx == nil || e.tree == nil {
		return nil
	}
	value, col, pk, ok := e.picker.CellSnapshot()
	if !ok {
		return nil
	}
	initial := e.picker.FormatForEdit(value)
	e.ctx.Open(value, col, pk, initial)
	return e.wrapErr("cell.edit.enter", e.tree.Push(e.ctx))
}

// Commit is the `<cr>` / `<esc>` handler on CELL_EDITOR. Reads the
// buffer; if it differs from OriginalValue, stages a Literal-kind
// PendingEdit on the active store. Pops the popup unconditionally.
//
// "Different" is a string compare against the formatted-for-edit
// rendering of OriginalValue — typing the same value back is treated
// as "no change" so the user can `i<esc>` a cell without dirtying it.
func (e *CellEditorController) Commit(_ commands.ExecCtx) error {
	if e.ctx == nil || !e.ctx.Active() {
		return nil
	}
	typed := e.ctx.ReadAndClearBuffer()
	col := e.ctx.Column()
	pk := e.ctx.PrimaryKey()
	originalSeed := ""
	if e.picker != nil {
		originalSeed = e.picker.FormatForEdit(e.ctx.OriginalValue())
	}
	changed := typed != originalSeed
	if changed && e.store != nil && len(pk) > 0 {
		// OldValue carries the ORIGINAL value (not the seed string)
		// so the eventual UPDATE WHERE clause uses the typed value
		// for optimistic concurrency detection.
		if err := e.store.Add(models.PendingEdit{
			PrimaryKey: pk,
			Column:     col.Name,
			OldValue:   e.ctx.OriginalValue(),
			NewValue:   typed,
			Kind:       models.Literal,
			LoadedAt:   time.Now(),
		}); err != nil {
			// Don't swallow — surface via wrapErr so the orchestrator
			// can log + toast at its discretion. The popup still pops
			// below to avoid trapping the user in a broken state.
			e.ctx.Close()
			_ = e.popFocus()
			return e.wrapErr("cell.edit.commit", err)
		}
	}
	e.ctx.Close()
	return e.popFocus()
}

// Discard is the `<c-c>` handler on CELL_EDITOR. On a clean cell,
// pops the popup without recording. On a dirty cell (an existing
// PendingEdit was staged before the user pressed `i` to re-edit),
// removes the prior PendingEdit AND emits the status toast
// `"discarded pending edit on (<pk>, <col>); <leader>cu to retry"`.
func (e *CellEditorController) Discard(_ commands.ExecCtx) error {
	if e.ctx == nil || !e.ctx.Active() {
		return nil
	}
	col := e.ctx.Column()
	pk := e.ctx.PrimaryKey()

	// Detect "dirty cell" — an existing PendingEdit on (pk, col)
	// BEFORE the user pressed `i`. The current edit-in-progress is
	// NOT staged yet (commit is the only path that calls Add) so a
	// nonzero HasEdit count maps cleanly to "this cell was already
	// dirty when the user opened the editor".
	dirty := false
	if e.store != nil && len(pk) > 0 {
		dirty = e.store.HasEdit(pk, col.Name)
	}

	// Reset the buffer so the next `i` on this cell starts fresh.
	_ = e.ctx.ReadAndClearBuffer()
	e.ctx.Close()
	if err := e.popFocus(); err != nil {
		return err
	}

	if dirty {
		e.store.Remove(pk, col.Name)
		if e.helpers.Toast != nil {
			e.helpers.Toast.Show(
				fmt.Sprintf("discarded pending edit on (%v, %s); <leader>cu to retry", pk, col.Name),
				cellEditDiscardToastTTL,
			)
		}
	}
	return nil
}

// popFocus dispatches the focus-stack pop. Centralised so the commit
// and discard paths share the same error-wrapping label.
func (e *CellEditorController) popFocus() error {
	if e.tree == nil {
		return nil
	}
	return e.wrapErr("cell.edit.pop", e.tree.Pop())
}

// cellEditDiscardToastTTL is the lifetime of the "discarded pending
// edit" status toast. Matches the default ToastHelper TTL (1.5s) the
// other controllers use for transient operation feedback.
const cellEditDiscardToastTTL = 1500 * time.Millisecond

// enterDisabled returns the user-facing reason `i` should be disabled
// on RESULT_GRID, or ("", false) when all gates pass. Evaluation order
// matches the AC priority: read-only → driver-cap → streaming →
// editable. The first failing gate wins.
//
// Exposed (lowercase, package-private) for the GetDisabled predicate
// the registered command consults — the same logic must not branch
// between Enter() and the predicate or the user sees a toast that
// disagrees with the actual disable state.
func (e *CellEditorController) enterDisabled() (string, bool) {
	if e.picker == nil {
		return "no active result grid", true
	}
	if e.picker.IsReadOnly() {
		return "read-only connection", true
	}
	if !e.picker.SupportsInlineEdit() {
		return "driver does not support inline edit", true
	}
	if e.picker.StreamBlocksEdit() {
		return "wait for current stream to finish", true
	}
	if !e.picker.Editable() {
		reason := e.picker.DisabledReason()
		if reason == "" {
			reason = "result is not inline-editable"
		}
		return reason, true
	}
	return "", false
}

// GetKeybindings returns the chord bindings owned by this controller:
//
//   - `i` on RESULT_GRID (ModeNormal): Enter.
//   - `<cr>` / `<esc>` on CELL_EDITOR (ModeInsert): Commit.
//   - `<c-c>` on CELL_EDITOR (ModeInsert): Discard.
//   - SetNull / Expr* bindings on CELL_EDITOR scope, reserved for A2.
//     Declared here so the chord surface is stable; their handlers are
//     registered as no-ops until A2 (dbsavvy-bwq.5) wires the real
//     per-type entry logic. Defaults follow the epic spec:
//   - <c-n>  → CellEditSetNull       (NULL setter)
//   - <c-t>  → CellEditExprNow       (now())
//   - <c-d>  → CellEditExprCurrentDate
//   - <c-e>  → CellEditExprPrompt    (free-form expression prompt)
//
// CELL_EDITOR scope bindings are published under ModeInsert because the
// popup flips the per-scope mode on focus (CELL_EDITOR is editable —
// see CellEditorContext.HandleFocus, wired by Z1).
func (e *CellEditorController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	scope := guicontext.CellEditorKey()
	return []*types.ChordBinding{
		// Enter — RESULT_GRID `i`.
		{
			Sequence:    []types.ChordKey{{Code: 'i'}},
			Mode:        types.ModeNormal,
			Scope:       types.RESULT_GRID,
			ActionID:    CellEditEnter,
			Description: "Edit cell",
		},
		// Commit — CELL_EDITOR `<cr>` and `<esc>`.
		{
			Sequence:    []types.ChordKey{{Special: types.KeyEnter}},
			Mode:        types.ModeInsert,
			Scope:       scope,
			ActionID:    CellEditCommit,
			Description: "Commit edit",
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyEsc}},
			Mode:        types.ModeInsert,
			Scope:       scope,
			ActionID:    CellEditCommit,
			Description: "Commit edit",
		},
		// Discard — CELL_EDITOR `<c-c>`.
		{
			Sequence:    []types.ChordKey{{Code: 'c', Mod: types.ChordModCtrl}},
			Mode:        types.ModeInsert,
			Scope:       scope,
			ActionID:    CellEditDiscard,
			Description: "Discard edit",
		},
		// A2 (dbsavvy-bwq.5) — per-type entry helpers. Key→ActionID
		// mapping mirrors the AC:
		//   <c-n>  → CellEditSetNull           (set NULL; disabled NOT NULL)
		//   <c-d>  → CellEditExprNow           (inject now())
		//   <c-t>  → CellEditExprCurrentDate   (inject current_date)
		//   <c-e>  → CellEditExprPrompt        (free-form expression)
		//
		// NOTE: the epic spec was originally authored against
		// `<leader>cn` / `<leader>ce` chords. A1 routed them through
		// <c-n>/<c-e> as the in-popup chord surface so the prefix
		// doesn't double-handle the `<leader>` waiter from the master
		// editor. Z1 (dbsavvy-bwq.23) reconciles both chord surfaces
		// once central keybinding registration lands.
		{
			Sequence:    []types.ChordKey{{Code: 'n', Mod: types.ChordModCtrl}},
			Mode:        types.ModeInsert,
			Scope:       scope,
			ActionID:    CellEditSetNull,
			Description: "Set NULL",
		},
		{
			Sequence:    []types.ChordKey{{Code: 'd', Mod: types.ChordModCtrl}},
			Mode:        types.ModeInsert,
			Scope:       scope,
			ActionID:    CellEditExprNow,
			Description: "Insert now() expression",
		},
		{
			Sequence:    []types.ChordKey{{Code: 't', Mod: types.ChordModCtrl}},
			Mode:        types.ModeInsert,
			Scope:       scope,
			ActionID:    CellEditExprCurrentDate,
			Description: "Insert current_date expression",
		},
		{
			Sequence:    []types.ChordKey{{Code: 'e', Mod: types.ChordModCtrl}},
			Mode:        types.ModeInsert,
			Scope:       scope,
			ActionID:    CellEditExprPrompt,
			Description: "Prompt for expression",
		},
	}
}

// RegisterActions registers the four handlers (Enter / Commit /
// Discard plus the SetNull/Expr* no-op placeholders) with reg. Enter's
// command carries a GetDisabled predicate so the Matcher surfaces the
// correct user-facing reason (see enterDisabled).
func (e *CellEditorController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	_ = reg.Register(&commands.Command{
		ID:          CellEditEnter,
		Description: "Edit cell",
		Tag:         "Cell Edit",
		Handler:     e.Enter,
		GetDisabled: func(_ commands.ExecCtx) (string, bool) {
			return e.enterDisabled()
		},
	})
	_ = reg.Register(&commands.Command{
		ID:          CellEditCommit,
		Description: "Commit cell edit",
		Tag:         "Cell Edit",
		Handler:     e.Commit,
	})
	_ = reg.Register(&commands.Command{
		ID:          CellEditDiscard,
		Description: "Discard cell edit",
		Tag:         "Cell Edit",
		Handler:     e.Discard,
	})

	// A2 (dbsavvy-bwq.5) — real handlers backing the per-type entry
	// chord surface. SetNull carries a GetDisabled predicate so the
	// Matcher surfaces "column is NOT NULL" without dispatching.
	_ = reg.Register(&commands.Command{
		ID:          CellEditSetNull,
		Description: "Set cell value to NULL",
		Tag:         "Cell Edit",
		Handler:     e.SetNull,
		GetDisabled: func(_ commands.ExecCtx) (string, bool) {
			return e.setNullDisabled()
		},
	})
	_ = reg.Register(&commands.Command{
		ID:          CellEditExprNow,
		Description: "Inject now() expression",
		Tag:         "Cell Edit",
		Handler:     e.ExprNow,
	})
	_ = reg.Register(&commands.Command{
		ID:          CellEditExprCurrentDate,
		Description: "Inject current_date expression",
		Tag:         "Cell Edit",
		Handler:     e.ExprCurrentDate,
	})
	_ = reg.Register(&commands.Command{
		ID:          CellEditExprPrompt,
		Description: "Prompt for SQL expression (verbatim)",
		Tag:         "Cell Edit",
		Handler:     e.ExprPrompt,
		GetDisabled: func(_ commands.ExecCtx) (string, bool) {
			return e.exprPromptDisabled()
		},
	})
}

// SetNull is the `<c-n>` handler on CELL_EDITOR. Stages a literal-kind
// PendingEdit with NewValue=nil for the active cell. Disabled (via
// setNullDisabled / the registered GetDisabled predicate) when the
// target column is NOT NULL.
//
// On success the popup is closed and the focus stack is popped — same
// terminal shape as Commit so the user lands back on the result grid.
func (e *CellEditorController) SetNull(_ commands.ExecCtx) error {
	if e.ctx == nil || !e.ctx.Active() {
		return nil
	}
	col := e.ctx.Column()
	pk := e.ctx.PrimaryKey()
	if len(pk) == 0 || e.store == nil {
		// No store / no row identity — nothing to record. Still close
		// the popup so the user isn't trapped.
		e.ctx.Close()
		return e.popFocus()
	}
	edit, err := helpers.BuildSetNullEdit(pk, col, e.ctx.OriginalValue())
	if err != nil {
		// Predicate should have gated this; if we get here (test path
		// or stale ctx) surface the error and still close cleanly.
		e.ctx.Close()
		_ = e.popFocus()
		return e.wrapErr("cell.edit.set_null", err)
	}
	if err := e.store.Add(edit); err != nil {
		e.ctx.Close()
		_ = e.popFocus()
		return e.wrapErr("cell.edit.set_null", err)
	}
	e.ctx.Close()
	return e.popFocus()
}

// ExprNow is the `<c-d>` handler on CELL_EDITOR. Stages an Expression-
// kind PendingEdit with NewExpr="now()" for the active cell.
func (e *CellEditorController) ExprNow(_ commands.ExecCtx) error {
	return e.injectExpression("cell.edit.expr.now",
		helpers.InjectNow)
}

// ExprCurrentDate is the `<c-t>` handler on CELL_EDITOR. Stages an
// Expression-kind PendingEdit with NewExpr="current_date".
func (e *CellEditorController) ExprCurrentDate(_ commands.ExecCtx) error {
	return e.injectExpression("cell.edit.expr.current_date",
		helpers.InjectCurrentDate)
}

// injectExpression is the shared body of ExprNow / ExprCurrentDate.
// builder closes over the per-action canned expression.
func (e *CellEditorController) injectExpression(
	label string,
	builder func(pk []any, col models.ColumnMeta, old any) models.PendingEdit,
) error {
	if e.ctx == nil || !e.ctx.Active() {
		return nil
	}
	col := e.ctx.Column()
	pk := e.ctx.PrimaryKey()
	if len(pk) == 0 || e.store == nil {
		e.ctx.Close()
		return e.popFocus()
	}
	edit := builder(pk, col, e.ctx.OriginalValue())
	if err := e.store.Add(edit); err != nil {
		e.ctx.Close()
		_ = e.popFocus()
		return e.wrapErr(label, err)
	}
	e.ctx.Close()
	return e.popFocus()
}

// ExprPrompt is the `<c-e>` handler on CELL_EDITOR. Opens a warning-
// themed PROMPT (TEMPORARY_POPUP) for free-form SQL expression entry.
// On submit, stages an Expression-kind PendingEdit with NewExpr=user
// input. Disabled on read_only connections (the predicate carries the
// reason); visually marked on confirm_writes — that marking is a
// PromptContext concern wired by Z1.
//
// The warning text is helpers.WarnExprPromptLabel and asserts
// "expressions are injected verbatim" per amendment.
//
// TODO(dbsavvy-bwq.23 / Z1): switch the prompt's border colour to
// WarnBorder once the theme key lands. Until then the standard PROMPT
// border is used and the warning text alone carries the message.
func (e *CellEditorController) ExprPrompt(_ commands.ExecCtx) error {
	if e.ctx == nil || !e.ctx.Active() {
		return nil
	}
	if e.helpers.Prompt == nil {
		// No prompt helper wired (unit-test path); nothing else to do.
		return nil
	}
	// Capture the per-edit snapshot NOW — the PROMPT push will pop the
	// CELL_EDITOR popup off the focus stack in Z1's final wiring, so
	// the controller can't rely on ctx.Active() being true on submit.
	col := e.ctx.Column()
	pk := e.ctx.PrimaryKey()
	old := e.ctx.OriginalValue()

	onSubmit := func(value string) error {
		if value == "" || e.store == nil || len(pk) == 0 {
			return nil
		}
		edit := helpers.BuildExprEdit(pk, col, old, value)
		if err := e.store.Add(edit); err != nil {
			return e.wrapErr("cell.edit.expr.prompt", err)
		}
		// The cell-editor popup is still on the stack underneath the
		// prompt (the prompt is pushed on top). Close + pop it now so
		// the user lands back on the grid after the prompt dismisses.
		if e.ctx != nil {
			e.ctx.Close()
		}
		return e.popFocus()
	}
	onCancel := func() error { return nil }
	return e.wrapErr("cell.edit.expr.prompt",
		e.helpers.Prompt.Prompt(helpers.WarnExprPromptLabel, "", onSubmit, onCancel))
}

// setNullDisabled returns ("column is NOT NULL", true) when the
// currently-edited column is NOT NULL. The predicate is checked by the
// Matcher before SetNull is dispatched.
//
// Falls back to ("", false) on no active context so unit tests that
// skip the popup open path don't see a spurious disable.
func (e *CellEditorController) setNullDisabled() (string, bool) {
	if e.ctx == nil || !e.ctx.Active() {
		return "", false
	}
	if !e.ctx.Column().Nullable {
		return helpers.ErrColumnNotNullable.Error(), true
	}
	return "", false
}

// exprPromptDisabled returns ("read-only connection", true) on read-
// only connections; otherwise enabled. Mirrors the Enter predicate's
// read-only branch — the warning prompt MUST NOT open against a read-
// only conn since the user could spend time crafting an expression
// that's guaranteed to fail at commit time.
func (e *CellEditorController) exprPromptDisabled() (string, bool) {
	if e.picker != nil && e.picker.IsReadOnly() {
		return "read-only connection", true
	}
	return "", false
}

// AttachToContext registers GetKeybindings on both RESULT_GRID (for
// the `i` Enter binding) and CELL_EDITOR (for Commit / Discard).
// Either context may be nil during tests — nil receivers are silently
// dropped by BaseContext.AddKeybindingsFn.
//
// Z1 owns the central wiring that calls AttachToContext with the live
// context handles; this method is the seam the wiring uses.
func (e *CellEditorController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(e.GetKeybindings)
}
