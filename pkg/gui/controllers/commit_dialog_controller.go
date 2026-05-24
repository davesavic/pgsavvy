package controllers

import (
	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// Package-level ActionID aliases. Canonical constants live in
// pkg/gui/commands/actions.go (upstreamed by Z1 Phase A,
// dbsavvy-bwq.23). Aliases retain the controllers.CommitDialog* names
// so existing callers (notably this package's tests) keep compiling.
const (
	CommitDialogOpen      = commands.CommitDialogOpen
	CommitDialogApply     = commands.CommitDialogApply
	CommitDialogDryRun    = commands.CommitDialogDryRun
	CommitDialogShowSql   = commands.CommitDialogShowSql
	CommitDialogCancel    = commands.CommitDialogCancel
	CommitDialogTypeChar  = commands.CommitDialogTypeChar
	CommitDialogBackspace = commands.CommitDialogBackspace
)

// CommitDialogApplyHook is invoked when `[a]` is pressed on a default
// connection (or on a confirm_writes connection once TypedName matches).
// The implementation lives in A5 (dbsavvy-bwq.8) and routes through the
// pending-edit apply helper. The controller defines the interface here
// so it stays free of any apply-package import — Z1 wires the concrete
// implementation post-construction.
type CommitDialogApplyHook interface {
	Apply(set *models.PendingEditSet, conn *models.Connection) error
}

// CommitDialogDryRunHook is invoked when `[d]` is pressed. A5 wraps the
// apply helper in BEGIN; ... ; ROLLBACK and returns one report entry
// per executed statement so the dialog can render `[N rows] <sql>`.
type CommitDialogDryRunHook interface {
	DryRun(set *models.PendingEditSet, conn *models.Connection) ([]guicontext.DryRunStmtResult, error)
}

// CommitDialogShowSqlHook is invoked each time the body flips into
// SqlPreview mode. Hook owners (typically the session logger) emit a
// one-shot audit line so the rendered SQL is captured per ADR-28.
// Nil-safe: when unwired the body still renders but no audit fires.
type CommitDialogShowSqlHook interface {
	OnShowSQL(set *models.PendingEditSet, conn *models.Connection)
}

// CommitDialogCancelHook is invoked when `[Esc]` / `[c]` are pressed.
// Default behaviour is "pop focus, leave PendingEditSet untouched";
// the hook exists so Z1 can layer extra cleanup (e.g. flush a
// per-dialog audit entry) without forcing this controller to depend
// on the orchestrator.
type CommitDialogCancelHook interface {
	OnCancel()
}

// CommitDialogController owns the COMMIT_DIALOG-scope bindings:
//
//   - `[a]` on COMMIT_DIALOG: Apply (gated on ApplyEnabled).
//   - `[d]` on COMMIT_DIALOG: DryRun (flips mode + invokes OnDryRun).
//   - `[s]` on COMMIT_DIALOG: ShowSql (toggles SqlPreview mode).
//   - `[Esc]` / `[c]` on COMMIT_DIALOG: Cancel.
//
// CommitDialogOpen is also registered (so `:w` / `<leader>cw` resolve)
// but its handler is a no-op here — A5 / Z1 own the open path because
// they hold the per-table PendingEditSet handle.
//
// Concurrency: every handler runs on the gocui MainLoop. No internal
// locking; the collaborators own their own synchronisation.
type CommitDialogController struct {
	baseController

	ctx       *guicontext.CommitDialogContext
	tree      FocusPopper
	applyHook CommitDialogApplyHook
	dryRunFn  CommitDialogDryRunHook
	showSqlFn CommitDialogShowSqlHook
	cancelFn  CommitDialogCancelHook
}

// NewCommitDialogController constructs the controller. Every
// collaborator may be nil during unit tests; each handler nil-checks
// before dispatching. Production wiring (Z1) supplies the live context,
// focus-stack tree, apply / dry-run / show-sql / cancel hooks (A5's
// concrete impls satisfy these interfaces).
//
// The controller installs DefaultCommitDialogRender on the context as
// the body-renderer; SetRenderHook overrides this for tests that need
// to assert on a specific render output.
func NewCommitDialogController(
	c *common.Common,
	helpers HelperBag,
	ctx *guicontext.CommitDialogContext,
	tree FocusPopper,
) *CommitDialogController {
	ctrl := &CommitDialogController{
		baseController: newBase(c, helpers),
		ctx:            ctx,
		tree:           tree,
	}
	if ctx != nil {
		ctx.SetRenderHook(DefaultCommitDialogRender)
	}
	return ctrl
}

// SetTree swaps the FocusPopper post-construction. Mirrors
// CellEditorController.SetTree — the orchestrator builds the tree
// after the controllers, so wiring lands here.
func (e *CommitDialogController) SetTree(t FocusPopper) { e.tree = t }

// SetApplyHook wires the OnApply collaborator (A5). Nil-safe: a nil
// hook disables [a] dispatch even when the typed-name gate is open.
func (e *CommitDialogController) SetApplyHook(h CommitDialogApplyHook) { e.applyHook = h }

// SetDryRunHook wires the OnDryRun collaborator. Nil-safe.
func (e *CommitDialogController) SetDryRunHook(h CommitDialogDryRunHook) { e.dryRunFn = h }

// SetShowSqlHook wires the OnShowSql collaborator. Nil-safe.
func (e *CommitDialogController) SetShowSqlHook(h CommitDialogShowSqlHook) { e.showSqlFn = h }

// SetCancelHook wires the OnCancel collaborator. Nil-safe.
func (e *CommitDialogController) SetCancelHook(h CommitDialogCancelHook) { e.cancelFn = h }

// Apply is the `[a]` handler. Consults ctx.ApplyEnabled(); on a
// passed gate it invokes the OnApply hook and pops the dialog. On a
// failed gate it no-ops — the registered command's GetDisabled
// predicate is the user-facing surface for the failure reason.
func (e *CommitDialogController) Apply(_ commands.ExecCtx) error {
	if e.ctx == nil || !e.ctx.Active() {
		return nil
	}
	if !e.ctx.ApplyEnabled() {
		return nil
	}
	if e.applyHook == nil {
		// No hook wired yet (A5 hasn't landed). Treat as no-op rather
		// than error so the popup still pops and the user isn't
		// trapped — preserves the dialog's "always escapable" rule.
		e.ctx.Close()
		return e.popFocus()
	}
	set := e.ctx.Set()
	conn := e.ctx.Connection()
	if err := e.applyHook.Apply(set, conn); err != nil {
		// Surface the error; the popup stays open so the user can
		// re-read the diff and retry. ApplyHook is responsible for
		// emitting a toast on its own (it owns the failure context).
		return e.wrapErr("commit.dialog.apply", err)
	}
	e.ctx.Close()
	return e.popFocus()
}

// DryRun is the `[d]` handler. Flips the body to DryRunResult mode,
// invokes the dry-run hook, stashes the report on the context, and
// HandleRender repaints. nil hook → mode flips and renders the
// "press [d] to run dry-run" hint instead of a report (drives the
// user to wire the hook).
func (e *CommitDialogController) DryRun(_ commands.ExecCtx) error {
	if e.ctx == nil || !e.ctx.Active() {
		return nil
	}
	e.ctx.SetMode(guicontext.CommitDialogDryRunResult)
	if e.dryRunFn == nil {
		// Clear any stale report so the dialog doesn't show last
		// invocation's data after the hook is unwired (mainly a test
		// concern, but cheap).
		e.ctx.SetDryRunResult(nil)
		return nil
	}
	report, err := e.dryRunFn.DryRun(e.ctx.Set(), e.ctx.Connection())
	if err != nil {
		// Surface the error via a single-entry report so the body
		// has something to render — keeps the user inside the dialog
		// with actionable feedback.
		e.ctx.SetDryRunResult([]guicontext.DryRunStmtResult{{Err: err}})
		return e.wrapErr("commit.dialog.dryrun", err)
	}
	e.ctx.SetDryRunResult(report)
	return nil
}

// ShowSql is the `[s]` handler. Toggles between SqlPreview and
// Preview body modes. The OnShowSql hook fires every time the body
// flips INTO SqlPreview (not on the toggle-back) so the audit emission
// is once-per-render-cycle, not once-per-keypress.
func (e *CommitDialogController) ShowSql(_ commands.ExecCtx) error {
	if e.ctx == nil || !e.ctx.Active() {
		return nil
	}
	if e.ctx.Mode() == guicontext.CommitDialogSqlPreview {
		e.ctx.SetMode(guicontext.CommitDialogPreview)
		return nil
	}
	e.ctx.SetMode(guicontext.CommitDialogSqlPreview)
	if e.showSqlFn != nil {
		e.showSqlFn.OnShowSQL(e.ctx.Set(), e.ctx.Connection())
	}
	return nil
}

// Cancel is the `[Esc]` / `[c]` handler. Pops the dialog without
// modifying the PendingEditSet. Invokes the OnCancel hook (if wired)
// BEFORE the pop so audit / cleanup runs against the live state.
func (e *CommitDialogController) Cancel(_ commands.ExecCtx) error {
	if e.ctx == nil || !e.ctx.Active() {
		return nil
	}
	if e.cancelFn != nil {
		e.cancelFn.OnCancel()
	}
	e.ctx.Close()
	return e.popFocus()
}

// Open is the no-op handler registered for CommitDialogOpen. The
// real open path lives in A5 / Z1 (they hold the per-table
// PendingEditSet handle the controller doesn't see). Registered so
// the Matcher resolves the ActionID even before Z1 wires the chord
// + ExCommand dispatchers.
func (e *CommitDialogController) Open(_ commands.ExecCtx) error { return nil }

// popFocus dispatches the focus-stack pop. Centralised so apply +
// cancel share the same error-wrapping label.
func (e *CommitDialogController) popFocus() error {
	if e.tree == nil {
		return nil
	}
	return e.wrapErr("commit.dialog.pop", e.tree.Pop())
}

// applyDisabled returns the user-facing reason `[a]` should be
// disabled on COMMIT_DIALOG, or ("", false) when ready. Mirrors
// ctx.ApplyEnabled() with a specific reason per failure mode so the
// Matcher can surface a meaningful toast.
func (e *CommitDialogController) applyDisabled() (string, bool) {
	if e.ctx == nil || !e.ctx.Active() {
		return "no commit dialog active", true
	}
	conn := e.ctx.Connection()
	if conn == nil {
		return "no active connection", true
	}
	set := e.ctx.Set()
	if set == nil || set.IsEmpty() {
		return "no staged edits", true
	}
	if conn.ReadOnly {
		return "read-only connection", true
	}
	if conn.ConfirmWrites && e.ctx.TypedName() != conn.Name {
		return "type the connection name to enable apply", true
	}
	return "", false
}

// GetKeybindings returns the chord bindings owned by this controller:
//
//   - `[a]` on COMMIT_DIALOG (ModeNormal): Apply.
//   - `[d]` on COMMIT_DIALOG (ModeNormal): DryRun.
//   - `[s]` on COMMIT_DIALOG (ModeNormal): ShowSql.
//   - `[Esc]` on COMMIT_DIALOG (ModeNormal): Cancel.
//   - `[c]` on COMMIT_DIALOG (ModeNormal): Cancel.
//
// COMMIT_DIALOG is a TEMPORARY_POPUP scope; the dialog is non-editable
// (the typed-name input is driven via a dedicated input view Z1 wires,
// NOT by the dialog body). Mode is ModeNormal because the [a]/[d]/[s]
// keys MUST be reachable as bare letters — confirm_writes connections
// route printable runes into the typed-name input via a separate
// scope Z1 wires post-task (currently the rune buffer is driven via
// SetTypedName for tests).
func (e *CommitDialogController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	scope := guicontext.CommitDialogKey()
	return []*types.ChordBinding{
		{
			Sequence:    []types.ChordKey{{Code: 'a'}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    CommitDialogApply,
			Description: "Apply pending edits",
		},
		{
			Sequence:    []types.ChordKey{{Code: 'd'}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    CommitDialogDryRun,
			Description: "Dry-run pending edits",
		},
		{
			Sequence:    []types.ChordKey{{Code: 's'}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    CommitDialogShowSql,
			Description: "Toggle SQL preview",
		},
		{
			Sequence:    []types.ChordKey{{Code: 'c'}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    CommitDialogCancel,
			Description: "Cancel commit dialog",
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyEsc}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    CommitDialogCancel,
			Description: "Cancel commit dialog",
		},
	}
}

// RegisterActions registers every handler with reg. Apply's command
// carries a GetDisabled predicate so the Matcher surfaces the
// typed-name / read-only / empty-set reasons.
func (e *CommitDialogController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	_ = reg.Register(&commands.Command{
		ID:          CommitDialogOpen,
		Description: "Open commit dialog (A5/Z1 wires the real handler)",
		Tag:         "Commit",
		Handler:     e.Open,
	})
	_ = reg.Register(&commands.Command{
		ID:          CommitDialogApply,
		Description: "Apply pending edits",
		Tag:         "Commit",
		Handler:     e.Apply,
		GetDisabled: func(_ commands.ExecCtx) (string, bool) {
			return e.applyDisabled()
		},
	})
	_ = reg.Register(&commands.Command{
		ID:          CommitDialogDryRun,
		Description: "Dry-run pending edits",
		Tag:         "Commit",
		Handler:     e.DryRun,
	})
	_ = reg.Register(&commands.Command{
		ID:          CommitDialogShowSql,
		Description: "Toggle SQL preview",
		Tag:         "Commit",
		Handler:     e.ShowSql,
	})
	_ = reg.Register(&commands.Command{
		ID:          CommitDialogCancel,
		Description: "Cancel commit dialog",
		Tag:         "Commit",
		Handler:     e.Cancel,
	})

	// Typed-name input no-ops — Z1 will replace these with the
	// real per-rune / backspace handlers once the editable view is
	// wired. Registered here so the Matcher resolves the IDs.
	noop := func(commands.ExecCtx) error { return nil }
	for _, id := range []string{CommitDialogTypeChar, CommitDialogBackspace} {
		_ = reg.Register(&commands.Command{
			ID:          id,
			Description: id + " (pending Z1)",
			Tag:         "Commit",
			Handler:     noop,
		})
	}
}

// AttachToContext registers GetKeybindings on the COMMIT_DIALOG
// context. Mirrors the CellEditorController pattern.
func (e *CommitDialogController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(e.GetKeybindings)
}
