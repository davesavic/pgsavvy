package context

import (
	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// CommitDialogMode selects which body the dialog renders.
type CommitDialogMode int

const (
	// CommitDialogPreview lists the row diffs (PK + column-by-column
	// old → new). Default mode when the dialog is opened.
	CommitDialogPreview CommitDialogMode = iota
	// CommitDialogSqlPreview renders the generated SQL (one UPDATE per
	// column-change, IS NOT DISTINCT FROM predicate, wrapped in a
	// BEGIN/COMMIT envelope).
	CommitDialogSqlPreview
	// CommitDialogDryRunResult renders the per-statement rows-affected
	// list returned by a dry-run apply (BEGIN; ... ; ROLLBACK).
	CommitDialogDryRunResult
)

// DryRunStmtResult is one entry in the dry-run report displayed in
// CommitDialogDryRunResult mode. One entry per UPDATE statement; A5's
// apply helper populates the slice and hands it back via SetDryRunResult.
type DryRunStmtResult struct {
	// SQL is the statement text (already scrubbed for log emission).
	SQL string
	// RowsAffected is the count returned by the driver. -1 indicates
	// the driver did not report a count (kept distinct from a real 0).
	RowsAffected int64
	// Err is the per-statement error, if any. Non-nil entries surface
	// in the dialog as "[ERR]" rows.
	Err error
}

// CommitDialogContext is the TEMPORARY_POPUP that renders the staged
// PendingEditSet, a typed-name confirmation gate (for confirm_writes
// connections), and switchable Preview / SqlPreview / DryRunResult
// bodies.
//
// View-state owned here:
//   - Set: the staged edits being committed (captured at Open()).
//   - Conn: the connection profile (drives color, icon, label, name
//     gate, confirm_writes flag).
//   - Mode: which body to render (Preview by default).
//   - TypedName: the user's typed-name buffer for the confirm gate.
//     Persists across mode toggles within the same dialog instance;
//     cleared on Close (ADR-12: no confirmation memory).
//   - DryRunResult: the report from the most recent OnDryRun invocation
//     (nil until [d] is pressed at least once).
//   - pkCols: the resolved PK column names for the active table,
//     captured at Open(). Used by the SQL builder for real column
//     names instead of pk1/pk2 placeholders.
//   - encoder: the SQL literal encoder from the active tab's session,
//     captured at Open(). Used to inline Go values into SQL text.
//     Stale on connection switch (documented with toast).
//
// Rendering is owned by HandleRender — it delegates body assembly to
// pkg/gui/controllers/commit_dialog_render.go so the formatting logic
// stays unit-testable without spinning up a context.
type CommitDialogContext struct {
	BaseContext

	deps Deps

	// active flips true on Open() and false on Close(). HandleRender
	// no-ops when false so a stale push doesn't paint on top of the
	// focused context.
	active bool

	// set carries the staged edits being committed. Captured at
	// Open() so the dialog renders a stable snapshot even if the
	// underlying PendingEditSet is mutated concurrently.
	set *models.PendingEditSet

	// conn is the active connection profile. Drives:
	//   - Border color (Conn.Color via PresentationHook).
	//   - Header icon + label (Conn.Icon / Conn.Label).
	//   - Typed-name gate (Conn.Name, when ConfirmWrites).
	conn *models.Connection

	// mode is the current body selector. Defaults to CommitDialogPreview.
	mode CommitDialogMode

	// typedName is the user's typed-name buffer for the confirm-writes
	// gate. Persists across mode transitions within a single dialog
	// instance (per epic amendment). Reset on Close so re-opening the
	// dialog re-prompts (ADR-12 no memory).
	typedName string

	// dryRunResult is the most recent OnDryRun report. nil until [d]
	// is pressed; the controller writes it back via SetDryRunResult.
	dryRunResult []DryRunStmtResult

	// pkCols carries the resolved PK column names for the active table,
	// captured at Open(). Used by the SQL builder.
	pkCols []string

	// encoder is the SQL literal encoder from the active tab's session,
	// captured at Open(). Used to inline Go values into SQL text.
	encoder drivers.Encoder

	// renderHook is invoked by HandleRender to produce the body text.
	// Defaults to DefaultCommitDialogRender (set by the controller via
	// SetRenderHook). Kept as a hook so the rendering code lives in
	// pkg/gui/controllers (the existing home for visual styling) without
	// pulling controllers into the context package's imports.
	renderHook func(view CommitDialogView) string
}

// CommitDialogView is the read-only surface the render hook consumes.
// Keeps the render code free of any pointer into the context's mutable
// state, so the formatting logic is purely a function of its inputs.
type CommitDialogView struct {
	Set          *models.PendingEditSet
	Conn         *models.Connection
	Mode         CommitDialogMode
	TypedName    string
	DryRunResult []DryRunStmtResult
	// PkCols carries the resolved PK column names for the active table.
	// Populated from the orchestrator adapter at Open() time so the
	// render path has real names (not pk1/pk2 placeholders).
	PkCols []string
	// Encoder produces SQL literals for the server dialect. Populated
	// from the active tab's runner at Open() time. Used by the preview
	// and dry-run render paths to inline literal values into SQL text.
	// Stale on connection switch (documented with toast).
	Encoder drivers.Encoder
}

// NewCommitDialogContext builds a CommitDialogContext bound to
// types.COMMIT_DIALOG.
func NewCommitDialogContext(base BaseContext, deps Deps) *CommitDialogContext {
	return &CommitDialogContext{BaseContext: base, deps: deps}
}

// CommitDialogKey returns the ContextKey CommitDialogContext is bound
// to. Retained as an accessor so callers don't need to import the types
// package directly; resolves to types.COMMIT_DIALOG.
func CommitDialogKey() types.ContextKey { return types.COMMIT_DIALOG }

// SetRenderHook installs the body renderer the controller supplies.
// Nil is treated as "no render" (HandleRender writes an empty body).
func (c *CommitDialogContext) SetRenderHook(fn func(CommitDialogView) string) {
	c.renderHook = fn
}

// Open transitions the context into the active state and captures the
// dialog's per-invocation snapshot. typedName resets to "" and mode
// resets to Preview each time the dialog opens (ADR-12: no memory).
//
// When ConfirmWrites is true but Conn.Name is empty, typedName is set
// to a sentinel value that no human input can match — the gate can never
// pass on a connection with an empty name.
func (c *CommitDialogContext) Open(set *models.PendingEditSet, conn *models.Connection) {
	c.active = true
	c.set = set
	c.conn = conn
	c.mode = CommitDialogPreview
	c.typedName = ""
	c.dryRunResult = nil
	if conn != nil && conn.ConfirmWrites && conn.Name == "" {
		c.typedName = "\x00empty-name-sentinel"
	}
}

// Close transitions the context back to inactive and clears the
// per-invocation snapshot. Called by the controller after dispatching
// apply / cancel; the focus-stack Pop is driven separately.
func (c *CommitDialogContext) Close() {
	c.active = false
	c.set = nil
	c.conn = nil
	c.mode = CommitDialogPreview
	c.typedName = ""
	c.dryRunResult = nil
	c.pkCols = nil
	c.encoder = nil
}

// Active reports whether the dialog is currently waiting for input.
// HandleRender + the controller's handlers guard on Active() so a
// stale dispatch on a popped popup is a no-op.
func (c *CommitDialogContext) Active() bool { return c.active }

// Set returns the staged edit set captured at Open() time. nil when
// the dialog is inactive.
func (c *CommitDialogContext) Set() *models.PendingEditSet { return c.set }

// Connection returns the connection profile captured at Open() time.
// nil when the dialog is inactive.
func (c *CommitDialogContext) Connection() *models.Connection { return c.conn }

// Mode returns the current body selector.
func (c *CommitDialogContext) Mode() CommitDialogMode { return c.mode }

// SetMode flips the body selector. TypedName is NOT cleared — the
// epic amendment requires it to persist across mode toggles within a
// single dialog instance.
func (c *CommitDialogContext) SetMode(m CommitDialogMode) { c.mode = m }

// TypedName returns the current typed-name buffer.
func (c *CommitDialogContext) TypedName() string { return c.typedName }

// SetTypedName replaces the typed-name buffer. Used by the controller
// to back the typed-name input (and by tests that want to drive the
// gate directly).
func (c *CommitDialogContext) SetTypedName(s string) { c.typedName = s }

// DryRunResult returns the most recent dry-run report, or nil if [d]
// has not been pressed in this dialog.
func (c *CommitDialogContext) DryRunResult() []DryRunStmtResult { return c.dryRunResult }

// SetDryRunResult records the report the controller received from
// OnDryRun. Pass nil to clear (e.g. when switching modes).
func (c *CommitDialogContext) SetDryRunResult(r []DryRunStmtResult) { c.dryRunResult = r }

// PkCols returns the resolved PK column names for the active table,
// captured at Open().
func (c *CommitDialogContext) PkCols() []string { return c.pkCols }

// SetPkCols replaces the PK column names. Called by the orchestrator
// adapter when wiring the dialog.
func (c *CommitDialogContext) SetPkCols(pkCols []string) { c.pkCols = pkCols }

// Encoder returns the SQL literal encoder captured at Open().
func (c *CommitDialogContext) Encoder() drivers.Encoder { return c.encoder }

// SetEncoder replaces the encoder. Called by the orchestrator adapter
// when wiring the dialog.
func (c *CommitDialogContext) SetEncoder(enc drivers.Encoder) { c.encoder = enc }

// ApplyEnabled reports whether [a] should fire. Mirrors the controller's
// internal gate so both the registered command's Disabled predicate
// and the dialog body (which renders "type <name> to enable" until
// gate passes) can consult one source of truth.
//
// Hard disables: nil connection (defensive — shouldn't happen
// post-Open), empty set (apply would be a no-op — caller should never
// open the dialog in this state), read-only connection (caller
// should hard-disable :w upstream), and confirm_writes:true with an
// empty connection name (gate can never pass — no name to match).
//
// Soft disable: confirm_writes:true with TypedName != Conn.Name.
func (c *CommitDialogContext) ApplyEnabled() bool {
	if !c.active || c.conn == nil || c.set == nil || c.set.IsEmpty() {
		return false
	}
	if c.conn.ReadOnly {
		return false
	}
	if c.conn.ConfirmWrites && c.conn.Name == "" {
		return false
	}
	if c.conn.ConfirmWrites && c.typedName != c.conn.Name {
		return false
	}
	return true
}

// Presentation resolves the popup border style and header text via
// deps.PresentationHook. Mirrors ConfirmationContext.Presentation —
// Z1 wires the same PresentationHook so the border picks up
// connection.color.
func (c *CommitDialogContext) Presentation() (types.TextStyle, string) {
	if c.deps.PresentationHook == nil {
		return types.TextStyle{}, ""
	}
	return c.deps.PresentationHook(c.conn)
}

// HandleRender writes the dialog body via the installed render hook.
// No-op when inactive, when no hook is installed, or when no driver is
// wired. The visual frame (border, position) is owned by the layout
// pass; this hook only paints the body.
func (c *CommitDialogContext) HandleRender() error {
	if !c.active || c.renderHook == nil {
		return nil
	}
	view := CommitDialogView{
		Set:          c.set,
		Conn:         c.conn,
		Mode:         c.mode,
		TypedName:    c.typedName,
		DryRunResult: c.dryRunResult,
		PkCols:       c.pkCols,
		Encoder:      c.encoder,
	}
	body := c.renderHook(view)
	viewName := c.GetViewName()
	writeView(c.deps, func() error {
		return c.deps.GuiDriver.SetContent(viewName, body)
	})
	return nil
}
