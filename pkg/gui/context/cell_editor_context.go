package context

import (
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// cellEditorKey is the placeholder ContextKey for the inline cell-edit
// popup until Z1 (dbsavvy-bwq.23) upstreams CELL_EDITOR into
// pkg/gui/types/context.go. Lives in this file so the rest of the
// codebase has one grep target when Z1 lands ("cell_editor" → switch to
// types.CELL_EDITOR).
//
// Z1 will:
//  1. Declare types.CELL_EDITOR ContextKey = "cell_editor".
//  2. Replace cellEditorKey usages with types.CELL_EDITOR.
//  3. Delete this constant.
const cellEditorKey types.ContextKey = "cell_editor"

// CellEditorContext is the TEMPORARY_POPUP that renders a single-line
// buffer over the cursor cell of the focused result grid. The popup is
// pushed by CellEditorController.Enter on `i`; popped by `<esc>`/`<cr>`
// (commit) or `<c-c>` (discard).
//
// State (OriginalValue, Column, PrimaryKey) is captured at push-time so
// the eventual PendingEdit carries the value the user saw when they
// pressed `i` — NOT a re-fetched value (ADR-14 optimistic concurrency
// uses OldValue=OriginalValue in the WHERE clause).
//
// Runtime source-of-truth for the typed buffer mirrors PromptContext:
// when a *gocui.View is plumbed in via SetView the view's TextArea is
// authoritative; the buf field below remains the test seam for unit
// tests that do not wire a real view. Z1 wires the editable view +
// master Editor so printable runes / backspace / arrow keys flow through
// gocui.DefaultEditor into TextArea.
type CellEditorContext struct {
	BaseContext

	deps  Deps
	modes types.ModeSetter

	// active flips true on Open() and false on Close(). HandleRender
	// no-ops when false so a stale push doesn't paint on top of the
	// focused context.
	active bool

	// originalValue is the cell value at edit-time. Used by the
	// controller to (a) detect "no buffer change → elide" and
	// (b) populate PendingEdit.OldValue for optimistic-concurrency
	// detection at apply time.
	originalValue any

	// column is the result-set column metadata for the cell. Carries
	// the name (PendingEdit.Column) and type hints for per-type entry
	// helpers (A2 consumes Column.TypeOID).
	column models.ColumnMeta

	// primaryKey is the row identity captured at edit-time. Order
	// matches the table's PK column order. Becomes
	// PendingEdit.PrimaryKey on commit.
	primaryKey []any

	// buf is the test-mode line buffer. Production reads the view's
	// TextArea via SetView (mirrors PromptContext).
	buf string

	// view is the live gocui view handle the orchestrator plumbs in
	// via SetView each Layout frame. Nil during unit tests; Buffer()
	// falls back to buf in that case.
	view types.View
}

// NewCellEditorContext builds a CellEditorContext bound to the
// CELL_EDITOR placeholder key. Z1 swaps the key for types.CELL_EDITOR
// when it ships the central wiring.
func NewCellEditorContext(base BaseContext, deps Deps) *CellEditorContext {
	return &CellEditorContext{BaseContext: base, deps: deps}
}

// CellEditorKey returns the ContextKey CellEditorContext is bound to.
// Exists so Z1's central registration code can resolve the key without
// reaching into a package-private constant.
func CellEditorKey() types.ContextKey { return cellEditorKey }

// SetModes records the ModeSetter the focus hooks toggle. Mirrors
// PromptContext: CELL_EDITOR is an editable popup, so focus flips the
// per-scope mode to ModeInsert (the cell-edit buffer accepts free-form
// text input).
func (c *CellEditorContext) SetModes(m types.ModeSetter) { c.modes = m }

// SetView is called by the orchestrator's Layout pass each frame the
// CELL_EDITOR popup is on the focus stack. ReadAndClearBuffer reads
// typed text from the supplied view's TextArea.
func (c *CellEditorContext) SetView(v types.View) { c.view = v }

// Open transitions the context into the active state and captures the
// per-edit snapshot (original value, column metadata, row identity).
// The seeded `initial` text is the value the user sees in the cell —
// typically the string form of originalValue — and lands in both the
// view's TextArea (so Backspace / Left / Right work) and the test-mode
// buf (so tests that skip view wiring still see the seed).
func (c *CellEditorContext) Open(originalValue any, column models.ColumnMeta, primaryKey []any, initial string) {
	c.active = true
	c.originalValue = originalValue
	c.column = column
	// Defensive copy: the controller reads PrimaryKey() back when
	// building the PendingEdit, and the caller (CellEditorController)
	// may reuse its slice across edits.
	if len(primaryKey) == 0 {
		c.primaryKey = nil
	} else {
		c.primaryKey = append([]any(nil), primaryKey...)
	}
	c.buf = initial
	if c.view != nil && c.view.TextArea != nil {
		c.view.TextArea.Clear()
		for _, r := range initial {
			c.view.TextArea.TypeCharacter(string(r))
		}
	}
}

// Close transitions the context back to inactive and clears the
// per-edit snapshot. Called by the controller after dispatching the
// commit / discard branch; the focus-stack Pop is driven separately by
// the controller via its tree handle.
func (c *CellEditorContext) Close() {
	c.active = false
	c.originalValue = nil
	c.column = models.ColumnMeta{}
	c.primaryKey = nil
	c.buf = ""
	if c.view != nil && c.view.TextArea != nil {
		c.view.TextArea.Clear()
	}
}

// Active reports whether the popup is currently waiting for input.
// HandleRender + the controller's commit/discard handlers both guard on
// Active() so a stale dispatch on a popped popup is a no-op.
func (c *CellEditorContext) Active() bool { return c.active }

// OriginalValue returns the cell value captured at Open() time. Used
// by the controller to (a) elide the "no change" case and (b) populate
// PendingEdit.OldValue.
func (c *CellEditorContext) OriginalValue() any { return c.originalValue }

// Column returns the column metadata captured at Open() time. Carries
// Name (PendingEdit.Column) and TypeOID / TypeName (per-type entry
// helpers — A2).
func (c *CellEditorContext) Column() models.ColumnMeta { return c.column }

// PrimaryKey returns a defensive copy of the row identity captured at
// Open() time. Becomes PendingEdit.PrimaryKey on commit.
func (c *CellEditorContext) PrimaryKey() []any {
	if len(c.primaryKey) == 0 {
		return nil
	}
	out := make([]any, len(c.primaryKey))
	copy(out, c.primaryKey)
	return out
}

// SetBuffer replaces the test-mode buffer. Production runtime mutates
// the view's TextArea directly via gocui.DefaultEditor; this setter
// stays so unit tests (which don't wire a view) can drive the buffer
// without spinning up a real gocui surface.
func (c *CellEditorContext) SetBuffer(s string) { c.buf = s }

// Buffer returns the current typed input. Reads from v.TextArea when a
// view has been plumbed in; otherwise returns the test-mode buf.
// Mirrors PromptContext.Buffer().
func (c *CellEditorContext) Buffer() string {
	if c.view != nil && c.view.TextArea != nil {
		return c.view.TextArea.GetContent()
	}
	return c.buf
}

// ReadAndClearBuffer returns the typed text and resets it to "". Used
// by the controller's commit handler to atomically consume the value
// before Close() drops the popup. When a view is plumbed in the
// TextArea is the source of truth; otherwise the test-mode buf is used.
func (c *CellEditorContext) ReadAndClearBuffer() string {
	if c.view != nil && c.view.TextArea != nil {
		s := c.view.TextArea.GetContent()
		c.view.TextArea.Clear()
		c.buf = ""
		return s
	}
	s := c.buf
	c.buf = ""
	return s
}

// HandleRender writes the popup body — a single "> <buffer>" line —
// into the gocui view. No-op when inactive or when no driver is wired.
// The visual frame (border, position over the cursor cell) is owned by
// the layout pass; this hook only paints the buffer.
func (c *CellEditorContext) HandleRender() error {
	if !c.active {
		return nil
	}
	body := "> " + c.Buffer()
	viewName := c.GetViewName()
	writeView(c.deps, func() error {
		return c.deps.GuiDriver.SetContent(viewName, body)
	})
	return nil
}
