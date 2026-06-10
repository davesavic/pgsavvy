package controllers

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/grid"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// Package-level ActionID aliases. Canonical constants live in
// pkg/gui/commands/actions.go (upstreamed by Z1 Phase A,
// dbsavvy-bwq.23). Aliases retain the controllers.ConflictDialog* names
// so existing callers (notably this package's tests) keep compiling.
const (
	ConflictDialogRefresh   = commands.ConflictDialogRefresh
	ConflictDialogOverwrite = commands.ConflictDialogOverwrite
	ConflictDialogCancel    = commands.ConflictDialogCancel
)

// ConflictDialogRefreshHook is invoked when `[r]` is pressed. The
// implementation lives in A5/Z1 and:
//  1. Re-fetches each conflict's row by PK so the grid has fresh data.
//  2. Drops the conflicting edits from the PendingEditSet (matching
//     edits by PK + Column).
//  3. Leaves non-conflicting edits staged so the user can re-run `:w`.
//
// The controller defines the interface here so it stays free of any
// apply-package import — Z1 wires the concrete implementation
// post-construction.
type ConflictDialogRefreshHook interface {
	Refresh(conflicts []models.ConflictedEdit, conn *models.Connection) error
}

// ConflictDialogOverwriteHook is invoked when `[o]` is pressed on a
// non-confirm_writes connection. The implementation re-applies each
// conflicted UPDATE with a PK-only predicate (no IS NOT DISTINCT FROM
// guard) so the staged NewValue lands regardless of the server-side
// drift.
type ConflictDialogOverwriteHook interface {
	Overwrite(conflicts []models.ConflictedEdit, conn *models.Connection) error
}

// ConflictDialogCancelHook is invoked when `[Esc]` is pressed. Default
// behaviour is "pop focus, leave PendingEditSet untouched"; the hook
// exists so Z1 can layer extra cleanup without forcing this controller
// to depend on the orchestrator. Mirrors CommitDialogCancelHook.
type ConflictDialogCancelHook interface {
	OnCancel()
}

// ConflictDialogController owns the CONFLICT_DIALOG-scope bindings:
//
//   - `[r]` on CONFLICT_DIALOG: Refresh.
//   - `[o]` on CONFLICT_DIALOG: Overwrite (NOT bound on confirm_writes).
//   - `[Esc]` on CONFLICT_DIALOG: Cancel.
//
// Concurrency: every handler runs on the gocui MainLoop. No internal
// locking; the collaborators own their own synchronisation.
type ConflictDialogController struct {
	baseController

	ctx         *guicontext.ConflictDialogContext
	tree        FocusPopper
	refreshHook ConflictDialogRefreshHook
	overwriteFn ConflictDialogOverwriteHook
	cancelFn    ConflictDialogCancelHook
}

// NewConflictDialogController constructs the controller. Every
// collaborator may be nil during unit tests; each handler nil-checks
// before dispatching. Production wiring (Z1) supplies the live context,
// focus-stack tree, refresh / overwrite / cancel hooks (A5's concrete
// impls satisfy these interfaces).
//
// The controller installs DefaultConflictDialogRender on the context as
// the body-renderer; SetRenderHook overrides this for tests that need
// to assert on a specific render output.
func NewConflictDialogController(
	c *common.Common,
	core CoreDeps,
	ctx *guicontext.ConflictDialogContext,
	tree FocusPopper,
) *ConflictDialogController {
	ctrl := &ConflictDialogController{
		baseController: newBase(c, HelperBag{CoreDeps: core}),
		ctx:            ctx,
		tree:           tree,
	}
	if ctx != nil {
		ctx.SetRenderHook(DefaultConflictDialogRender)
	}
	return ctrl
}

// SetTree swaps the FocusPopper post-construction. Mirrors
// CommitDialogController.SetTree — the orchestrator builds the tree
// after the controllers, so wiring lands here.
func (e *ConflictDialogController) SetTree(t FocusPopper) { e.tree = t }

// SetRefreshHook wires the OnRefresh collaborator (A5). Nil-safe: a
// nil hook still pops the dialog so the user is never trapped.
func (e *ConflictDialogController) SetRefreshHook(h ConflictDialogRefreshHook) { e.refreshHook = h }

// SetOverwriteHook wires the OnOverwrite collaborator (A5). Nil-safe.
func (e *ConflictDialogController) SetOverwriteHook(h ConflictDialogOverwriteHook) {
	e.overwriteFn = h
}

// SetCancelHook wires the OnCancel collaborator. Nil-safe.
func (e *ConflictDialogController) SetCancelHook(h ConflictDialogCancelHook) { e.cancelFn = h }

// Refresh is the `[r]` handler. Invokes the refresh hook and pops the
// dialog on success. Hook owners are responsible for re-fetching the
// touched rows and dropping conflicting edits from the PendingEditSet —
// the controller is intentionally ignorant of that logic.
func (e *ConflictDialogController) Refresh(_ commands.ExecCtx) error {
	if e.ctx == nil || !e.ctx.Active() {
		return nil
	}
	if e.refreshHook == nil {
		// No hook wired (A5 hasn't landed yet). Treat as no-op pop so
		// the user isn't trapped in the dialog — preserves the popup's
		// "always escapable" rule.
		e.ctx.Close()
		return e.popFocus()
	}
	conflicts := e.ctx.Conflicts()
	conn := e.ctx.Connection()
	if err := e.refreshHook.Refresh(conflicts, conn); err != nil {
		// Surface the error; the popup stays open so the user can
		// retry. The hook owner is responsible for emitting a toast.
		return e.wrapErr("conflict.dialog.refresh", err)
	}
	e.ctx.Close()
	return e.popFocus()
}

// Overwrite is the `[o]` handler. Gated on ctx.OverwriteAllowed() —
// confirm_writes:true connections cause this handler to no-op even if
// the binding somehow fires (defence in depth on top of the omitted
// binding registration).
func (e *ConflictDialogController) Overwrite(_ commands.ExecCtx) error {
	if e.ctx == nil || !e.ctx.Active() {
		return nil
	}
	if !e.ctx.OverwriteAllowed() {
		// confirm_writes:true: even if [o] is somehow dispatched (e.g.
		// stale registry entry from a future bug), the handler refuses.
		return nil
	}
	if e.overwriteFn == nil {
		// Same graceful pop as Refresh without a hook.
		e.ctx.Close()
		return e.popFocus()
	}
	conflicts := e.ctx.Conflicts()
	conn := e.ctx.Connection()
	if err := e.overwriteFn.Overwrite(conflicts, conn); err != nil {
		return e.wrapErr("conflict.dialog.overwrite", err)
	}
	e.ctx.Close()
	return e.popFocus()
}

// Cancel is the `[Esc]` handler. Pops the dialog without modifying the
// PendingEditSet. Invokes the OnCancel hook (if wired) BEFORE the pop
// so audit / cleanup runs against the live state.
func (e *ConflictDialogController) Cancel(_ commands.ExecCtx) error {
	if e.ctx == nil || !e.ctx.Active() {
		return nil
	}
	if e.cancelFn != nil {
		e.cancelFn.OnCancel()
	}
	e.ctx.Close()
	return e.popFocus()
}

// popFocus dispatches the focus-stack pop. Centralised so refresh +
// overwrite + cancel share the same error-wrapping label.
func (e *ConflictDialogController) popFocus() error {
	if e.tree == nil {
		return nil
	}
	return e.wrapErr("conflict.dialog.pop", e.tree.Pop())
}

// GetKeybindings returns the chord bindings owned by this controller.
// The `[o]` chord is OMITTED ENTIRELY on confirm_writes:true
// connections — per the epic AC, the binding is unmapped (not visibly
// disabled). The render layer omits the legend in lockstep so the user
// never sees the option.
//
// CONFLICT_DIALOG is a TEMPORARY_POPUP scope; the dialog is non-editable.
// Mode is ModeNormal because the [r]/[o] keys MUST be reachable as bare
// letters.
func (e *ConflictDialogController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	scope := guicontext.ConflictDialogKey()
	out := []*types.ChordBinding{
		{
			Sequence:    []types.ChordKey{{Code: 'r'}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    ConflictDialogRefresh,
			Description: "Refresh conflicted rows",
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyEsc}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    ConflictDialogCancel,
			Description: "Cancel conflict dialog",
		},
	}
	// `[o]` is appended ONLY when the active connection allows
	// overwrite. ctx may be nil during early wiring; in that case we
	// register the binding so the registry resolves the action (the
	// handler still no-ops on a nil ctx). Once the dialog is opened with
	// a confirm_writes connection, the binding is absent on subsequent
	// GetKeybindings invocations.
	if e.ctx == nil || !e.ctx.Active() || e.ctx.OverwriteAllowed() {
		out = append(out, &types.ChordBinding{
			Sequence:    []types.ChordKey{{Code: 'o'}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    ConflictDialogOverwrite,
			Description: "Overwrite with PK-only predicate",
		})
	}
	return out
}

// RegisterActions registers every handler with reg.
func (e *ConflictDialogController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	_ = reg.Register(&commands.Command{
		ID:          ConflictDialogRefresh,
		Description: "Refresh conflicted rows",
		Tag:         "Conflict",
		Handler:     e.Refresh,
	})
	_ = reg.Register(&commands.Command{
		ID:          ConflictDialogOverwrite,
		Description: "Overwrite conflicted rows (PK-only predicate)",
		Tag:         "Conflict",
		Handler:     e.Overwrite,
		GetDisabled: func(_ commands.ExecCtx) (string, bool) {
			if e.ctx == nil || !e.ctx.Active() {
				return "no conflict dialog active", true
			}
			if !e.ctx.OverwriteAllowed() {
				return "confirm_writes connection: overwrite disabled", true
			}
			return "", false
		},
	})
	_ = reg.Register(&commands.Command{
		ID:          ConflictDialogCancel,
		Description: "Cancel conflict dialog",
		Tag:         "Conflict",
		Handler:     e.Cancel,
	})
}

// AttachToContext registers GetKeybindings on the CONFLICT_DIALOG
// context. Mirrors the CommitDialogController pattern.
func (e *ConflictDialogController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(e.GetKeybindings)
}

// === Render layer ========================================================

// conflictDialogHeaderText is the always-on header for the dialog body.
// Exported as an internal const so tests can assert on the exact wording.
const conflictDialogHeaderText = "Conflicts detected — no changes applied"

// DefaultConflictDialogRender is the renderHook installed by the
// controller. Exposed (not lowercase) so unit tests can call it
// directly with a synthetic ConflictDialogView. Per the epic amendment,
// the body emits a header + one row per ConflictedEdit (with
// `your edit:`, `server now:`, `loaded at:` lines) and a legend that
// omits `[o]` when the connection is confirm_writes:true.
func DefaultConflictDialogRender(v guicontext.ConflictDialogView) string {
	var b strings.Builder
	b.WriteString(conflictDialogHeaderText)
	b.WriteByte('\n')

	for i, c := range v.Conflicts {
		if i > 0 {
			b.WriteByte('\n')
		}
		writeConflictRow(&b, c)
	}

	b.WriteByte('\n')
	writeConflictLegend(&b, v.Conn)
	return b.String()
}

// writeConflictRow emits one ConflictedEdit. When the server's current
// value equals the staged NewValue, the row is annotated as
// "already applied by another session" per the epic amendment — the
// `[r]` action silently drops that edit (no UI surface change beyond
// this annotation).
func writeConflictRow(b *strings.Builder, c models.ConflictedEdit) {
	fmt.Fprintf(b, "row %s · column %s\n", formatConflictPK(c.Edit.PrimaryKey), c.Edit.Column)
	fmt.Fprintf(b, "  your edit:  %s\n", formatConflictValue(stagedNewPayload(c.Edit), c.Edit.ColumnType))
	fmt.Fprintf(b, "  server now: %s\n", formatConflictValue(c.ServerValue, c.Edit.ColumnType))
	fmt.Fprintf(b, "  loaded at:  %s", c.LoadedAt.Format(time.RFC3339))
	if isAlreadyApplied(c) {
		b.WriteString("\n  (already applied by another session)")
	}
}

// writeConflictLegend emits the per-key legend. `[o]` is omitted when
// the connection is confirm_writes:true so the user never sees the
// option. Mirrors the GetKeybindings gating to keep the visible legend
// and the bound keys in lockstep.
func writeConflictLegend(b *strings.Builder, conn *models.Connection) {
	parts := []string{"[r] refresh"}
	if conn == nil || !conn.ConfirmWrites {
		parts = append(parts, "[o] overwrite")
	}
	parts = append(parts, "[Esc] cancel")
	b.WriteString(strings.Join(parts, "   "))
}

// stagedNewPayload returns the payload the user staged for the conflict.
// For Expression edits the rendered form is the verbatim NewExpr (no
// parameter binding); for Literal edits it's NewValue.
func stagedNewPayload(e models.PendingEdit) any {
	if e.Kind == models.Expression {
		return e.NewExpr
	}
	return e.NewValue
}

// isAlreadyApplied is true when the server's current value equals the
// staged NewValue — the conflict is benign because another session has
// already landed the same change. Compared via reflect.DeepEqual to
// match PendingEditSet.pkEqual's semantics; Expression edits are never
// considered already-applied (the NewExpr is a SQL fragment, not a
// value to compare against the server's current column).
func isAlreadyApplied(c models.ConflictedEdit) bool {
	if c.Edit.Kind == models.Expression {
		return false
	}
	return reflect.DeepEqual(c.ServerValue, c.Edit.NewValue)
}

// formatConflictPK renders a PK tuple as `(v1, v2, …)` for the multi-
// column case and as the bare value for single-column PKs (the common
// case). Matches the commit-dialog row-header style.
func formatConflictPK(pk []any) string {
	if len(pk) == 1 {
		return fmt.Sprintf("%v", pk[0])
	}
	parts := make([]string, len(pk))
	for i, v := range pk {
		parts[i] = fmt.Sprintf("%v", v)
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// formatConflictValue renders a value with NULL-safety, type-aware on the
// column's SQL type. json/jsonb values render as JSON text — the same form
// the grid and commit preview show — rather than Go's byte-slice form for a
// []byte the server returned (dbsavvy-2ij6). Nil renders as the literal
// `NULL` so the user can distinguish a missing row from an empty string.
func formatConflictValue(v any, columnType string) string {
	if v == nil {
		return "NULL"
	}
	if grid.IsJSONColumn(models.ColumnMeta{TypeName: columnType}) {
		return grid.FormatJSONValue(v)
	}
	return fmt.Sprintf("%v", v)
}
