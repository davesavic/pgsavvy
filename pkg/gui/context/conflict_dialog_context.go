package context

import (
	"errors"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// ErrNoConflicts is returned by Open when called with a nil/empty
// Conflicts slice. The dialog is meaningless without at least one
// conflict to display; the caller (A5's apply path) is responsible for
// only opening this dialog after a non-empty conflicts return.
var ErrNoConflicts = errors.New("conflict dialog: no conflicts to display")

// ConflictDialogContext is the TEMPORARY_POPUP that renders the list of
// ConflictedEdits returned by A5's apply helper when one or more staged
// edits' OldValue no longer matches the server. The dialog offers three
// resolutions:
//
//   - `[r]` refresh — drop CONFLICTING edits from the PendingEditSet,
//     re-fetch the touched rows; non-conflicting edits remain staged.
//   - `[o]` overwrite — re-apply each conflicted UPDATE with a PK-only
//     predicate. HIDDEN ENTIRELY on confirm_writes:true connections
//     (binding skipped, legend omitted; not just visually disabled).
//   - `[Esc]` cancel — pop the dialog; PendingEditSet retained verbatim.
//
// View-state owned here:
//   - Conflicts: the conflict batch (captured at Open()).
//   - Conn: the connection profile (drives color, OverwriteAllowed gate).
type ConflictDialogContext struct {
	BaseContext

	deps Deps

	// active flips true on Open() and false on Close(). HandleRender
	// no-ops when false so a stale push doesn't paint on top of the
	// focused context. Mirrors CommitDialogContext.active.
	active bool

	// conflicts is the conflict batch captured at Open() time. Captured
	// so the dialog renders a stable snapshot even if A5's apply helper
	// is re-invoked concurrently (which it shouldn't be, but defensive).
	conflicts []models.ConflictedEdit

	// conn is the active connection profile. Drives:
	//   - Border color (Conn.Color via PresentationHook).
	//   - `[o]` overwrite visibility (Conn.ConfirmWrites hides it).
	conn *models.Connection

	// renderHook is invoked by HandleRender to produce the body text.
	// Defaults to nil until the controller installs DefaultConflictDialogRender
	// via SetRenderHook. Mirrors CommitDialogContext.renderHook.
	renderHook func(view ConflictDialogView) string
}

// ConflictDialogView is the read-only surface the render hook consumes.
// Keeps the render code free of any pointer into the context's mutable
// state, so the formatting logic is purely a function of its inputs.
type ConflictDialogView struct {
	Conflicts []models.ConflictedEdit
	Conn      *models.Connection
}

// NewConflictDialogContext builds a ConflictDialogContext bound to
// types.CONFLICT_DIALOG.
func NewConflictDialogContext(base BaseContext, deps Deps) *ConflictDialogContext {
	return &ConflictDialogContext{BaseContext: base, deps: deps}
}

// ConflictDialogKey returns the ContextKey ConflictDialogContext is
// bound to. Retained as an accessor so callers don't need to import the
// types package directly; resolves to types.CONFLICT_DIALOG.
func ConflictDialogKey() types.ContextKey { return types.CONFLICT_DIALOG }

// SetRenderHook installs the body renderer the controller supplies.
// Nil is treated as "no render" (HandleRender writes an empty body).
func (c *ConflictDialogContext) SetRenderHook(fn func(ConflictDialogView) string) {
	c.renderHook = fn
}

// Open transitions the context into the active state and captures the
// dialog's per-invocation snapshot. Returns ErrNoConflicts if conflicts
// is nil or empty — the dialog refuses to open on a meaningless batch
// (AC: empty Conflicts list precondition).
func (c *ConflictDialogContext) Open(conflicts []models.ConflictedEdit, conn *models.Connection) error {
	if len(conflicts) == 0 {
		return ErrNoConflicts
	}
	c.active = true
	c.conflicts = conflicts
	c.conn = conn
	return nil
}

// Close transitions the context back to inactive and clears the
// per-invocation snapshot. Called by the controller after dispatching
// refresh / overwrite / cancel; the focus-stack Pop is driven separately.
func (c *ConflictDialogContext) Close() {
	c.active = false
	c.conflicts = nil
	c.conn = nil
}

// Active reports whether the dialog is currently waiting for input.
// HandleRender + the controller's handlers guard on Active() so a
// stale dispatch on a popped popup is a no-op.
func (c *ConflictDialogContext) Active() bool { return c.active }

// Conflicts returns the conflict batch captured at Open() time. nil
// when the dialog is inactive.
func (c *ConflictDialogContext) Conflicts() []models.ConflictedEdit { return c.conflicts }

// Connection returns the connection profile captured at Open() time.
// nil when the dialog is inactive.
func (c *ConflictDialogContext) Connection() *models.Connection { return c.conn }

// OverwriteAllowed reports whether `[o]` should be rendered AND bound.
// Hard-disabled on confirm_writes:true connections: per the epic AC,
// the option is HIDDEN ENTIRELY (key not bound, legend omitted) rather
// than visibly disabled. Mirrors ApplyEnabled's "single source of truth"
// pattern from CommitDialogContext.
func (c *ConflictDialogContext) OverwriteAllowed() bool {
	if !c.active || c.conn == nil {
		return false
	}
	return !c.conn.ConfirmWrites
}

// Presentation resolves the popup border style and header text via
// deps.PresentationHook. Mirrors CommitDialogContext.Presentation —
// Z1 wires the same PresentationHook so the border picks up
// connection.color.
func (c *ConflictDialogContext) Presentation() (types.TextStyle, string) {
	if c.deps.PresentationHook == nil {
		return types.TextStyle{}, ""
	}
	return c.deps.PresentationHook(c.conn)
}

// HandleRender writes the dialog body via the installed render hook.
// No-op when inactive, when no hook is installed, or when no driver is
// wired. The visual frame (border, position) is owned by the layout
// pass; this hook only paints the body.
func (c *ConflictDialogContext) HandleRender() error {
	if !c.active || c.renderHook == nil {
		return nil
	}
	view := ConflictDialogView{
		Conflicts: c.conflicts,
		Conn:      c.conn,
	}
	body := c.renderHook(view)
	viewName := c.GetViewName()
	writeView(c.deps, func() error {
		return c.deps.GuiDriver.SetContent(viewName, body)
	})
	return nil
}
