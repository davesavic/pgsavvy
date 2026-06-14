package context

import (
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
)

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

	// initial is the value captured at Open() time, read back by the
	// layout's freshView seed path (single seed source) via Initial().
	// Independent of buf/view so seeding survives buf being cleared.
	initial string

	// view is the live gocui view handle the orchestrator plumbs in
	// via SetView each Layout frame. Nil during unit tests; Buffer()
	// falls back to buf in that case.
	view types.View
}

// NewCellEditorContext builds a CellEditorContext bound to
// types.CELL_EDITOR.
func NewCellEditorContext(base BaseContext, deps Deps) *CellEditorContext {
	return &CellEditorContext{BaseContext: base, deps: deps}
}

// CellEditorKey returns the ContextKey CellEditorContext is bound to.
// Retained as an accessor so callers don't need to import the types
// package directly; resolves to types.CELL_EDITOR.
func CellEditorKey() types.ContextKey { return types.CELL_EDITOR }

// SetModes records the ModeSetter the focus hooks toggle. Mirrors
// PromptContext: CELL_EDITOR is an editable popup, so focus flips the
// per-scope mode to ModeInsert (the cell-edit buffer accepts free-form
// text input).
func (c *CellEditorContext) SetModes(m types.ModeSetter) { c.modes = m }

// HandleFocus flips the CELL_EDITOR scope into ModeInsert so the master
// Editor's Passthrough routes printable runes (and arrow/backspace/paste
// dispatches with no chord binding) through gocui.DefaultEditor into the
// view's TextArea, and enables the terminal caret so the edit point is
// visible. ModeInsert (not ModeCommand) is intentional: the commit/discard
// chords bind under ModeInsert. nil modes / nil driver → no-op.
func (c *CellEditorContext) HandleFocus(_ types.OnFocusOpts) error {
	if c.modes != nil {
		c.modes.Set(types.CELL_EDITOR, types.ModeInsert)
	}
	if c.deps.GuiDriver != nil {
		c.deps.GuiDriver.SetCaretEnabled(true)
	}
	return nil
}

// HandleFocusLost resets the CELL_EDITOR mode, disables the caret, and
// drops the cached view + buffer. The orchestrator DeleteView's the popup
// on pop and re-creates it on re-push, so a cached pointer would dangle;
// clearing buf prevents a prior cell's value leaking into a re-open.
// Mirrors PromptContext.HandleFocusLost.
func (c *CellEditorContext) HandleFocusLost(_ types.OnFocusLostOpts) error {
	if c.modes != nil {
		c.modes.Reset(types.CELL_EDITOR)
	}
	if c.deps.GuiDriver != nil {
		c.deps.GuiDriver.SetCaretEnabled(false)
	}
	c.view = nil
	c.buf = ""
	return nil
}

// SetView is called by the orchestrator's Layout pass each frame the
// CELL_EDITOR popup is on the focus stack. ReadAndClearBuffer reads
// typed text from the supplied view's TextArea.
func (c *CellEditorContext) SetView(v types.View) { c.view = v }

// Open transitions the context into the active state and captures the
// per-edit snapshot (original value, column metadata, row identity).
// The seeded `initial` text is the value the user sees in the cell —
// typically the string form of originalValue. The TextArea seed now
// happens in the layout freshView path; Open only
// captures `initial` (for the layout to read back via Initial()) and
// `buf` (the test-mode fallback for tests that skip view wiring).
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
	c.initial = initial
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
	c.initial = ""
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

// Initial returns the value captured at Open() time, independent of the
// plumbed view. The layout's freshView path reads this to seed a fresh
// view's TextArea exactly once (it must NOT route through Buffer(),
// which returns the empty TextArea content once a view is plumbed).
func (c *CellEditorContext) Initial() string { return c.initial }

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

// cellEditorPrefix is the body prefix HandleRender writes before the
// buffer. Its width is the horizontal offset the caret (CursorXY) and the
// scroll window (hScroll) both account for.
const cellEditorPrefix = "> "

// HandleRender writes the popup body — a single "> <buffer>" line —
// into the gocui view. No-op when inactive or when no driver is wired.
// The visual frame (border, position over the cursor cell) is owned by
// the layout pass; this hook only paints the buffer.
//
// The buffer is horizontally scrolled (hScroll) so the caret stays
// visible when the value is wider than the box: only the window of runes
// around the cursor is painted, and CursorXY places the caret at the
// matching column. The layout pass pins the view origin to 0 so these
// absolute coordinates line up.
func (c *CellEditorContext) HandleRender() error {
	if !c.active {
		return nil
	}
	buf := c.Buffer()
	start, _, win := c.hScroll(buf)
	body := cellEditorPrefix + windowRunes(buf, start, win)
	viewName := c.GetViewName()
	writeView(c.deps, func() error {
		return c.deps.GuiDriver.SetContent(viewName, body)
	})
	return nil
}

// hScroll computes the horizontal scroll state for the current frame:
// the rune offset of the first visible buffer rune (start), the TextArea
// cursor column (cx), and the number of columns available for buffer text
// after the prefix (win). Once the cursor passes the right edge the
// window slides right so the caret is always inside it. With no view
// wired (unit tests) the window is unbounded and start is 0.
func (c *CellEditorContext) hScroll(buf string) (start, cx, win int) {
	cx = len([]rune(buf))
	width := 0
	if c.view != nil {
		if c.view.TextArea != nil {
			cx, _ = c.view.TextArea.GetCursorXY()
		}
		width = c.view.InnerWidth()
	}
	if width <= 0 {
		return 0, cx, 0 // no view / unmeasurable: render the whole buffer
	}
	win = max(width-len(cellEditorPrefix), 1)
	if cx >= win {
		start = cx - win + 1
	}
	return start, cx, win
}

// CursorXY returns the caret coordinates (origin-0, after the "> "
// prefix) the layout pass feeds to SetViewCursor. The X is the prefix
// width plus the cursor's offset inside the visible window, so the caret
// tracks edits and Left/Right motion even when the value is scrolled.
// ok is false while inactive so the layout skips placing a caret on a
// popup that isn't shown. Mirrors PromptContext.CursorXY.
func (c *CellEditorContext) CursorXY() (int, int, bool) {
	if !c.active {
		return 0, 0, false
	}
	start, cx, _ := c.hScroll(c.Buffer())
	return len(cellEditorPrefix) + (cx - start), 0, true
}

// windowRunes returns the substring of s spanning [start, start+win)
// runes, clamped to the bounds of s. A win of 0 (no measurable view
// width) returns the remainder from start so the whole buffer renders.
func windowRunes(s string, start, win int) string {
	r := []rune(s)
	start = min(max(start, 0), len(r))
	if win <= 0 {
		return string(r[start:])
	}
	end := min(start+win, len(r))
	return string(r[start:end])
}
