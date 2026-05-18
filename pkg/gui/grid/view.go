package grid

import (
	"sync"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// ClipboardWriter is the indirection through which Yank publishes the
// selected text to the host environment. Production wires this to a
// real OSC-52 / xclip / pbcopy adapter (66p.11+ scope); tests inject a
// recording fake. A nil ClipboardWriter is normalised to noopClipboard
// in SetClipboard so Render / Yank can call it unconditionally.
type ClipboardWriter interface {
	Write(text string) error
}

// noopClipboard is the zero-value ClipboardWriter. Always succeeds,
// drops the payload. Used as the default until SetClipboard is called.
type noopClipboard struct{}

func (noopClipboard) Write(string) error { return nil }

// View is the embeddable result-grid renderer. It owns the row buffer,
// column metadata, cursor + selection state, and writes ANSI-styled
// cell contents into a host *gocui.View when Render is invoked.
//
// All exported mutators are safe to call from any goroutine; mu guards
// the row buffer and selection state. Render must be invoked on the UI
// thread (i.e. inside a gocui MainLoop callback) so its writes against
// the *gocui.View serialise with the runtime's draw pass.
//
// Lifecycle:
//
//  1. NewView() returns a zero-state grid.
//  2. SetColumns([]ColumnMeta) installs the schema and resets sizing.
//  3. AppendRows([]Row) is called repeatedly from
//     ResultBufferManager.appendRows (already routed via OnUIThread
//     by the manager). The first AutoSizeSampleRowCount rows seed
//     auto-sizing; afterwards widths are frozen.
//  4. Render(*gocui.View) draws the current viewport.
//  5. Cursor + selection methods (MoveCursorDown etc) are wired to
//     the keymap in 66p.11/66p.12.
type View struct {
	// mu guards rows, cols, title, cursor + selection. Held for the
	// shortest possible time in mutators; Render takes a snapshot
	// under RLock and releases before writing to the target view so
	// concurrent AppendRows from a different UI-thread-scheduled
	// callback doesn't stall the draw.
	mu sync.RWMutex

	rows []models.Row
	cols []models.ColumnMeta

	title string

	// widths is the per-column display width in cells. Sized to
	// len(cols). Populated lazily from the first AutoSizeSampleRowCount
	// rows; -1 means "not yet sized". Once widths are committed the
	// value is the locked-in width (≥ MinColumnWidth, ≤ MaxColumnWidth).
	widths []int

	// widthsLocked flips true once AutoSizeSampleRowCount rows have
	// been observed (or when SetColumns has been called and at least
	// one render has happened). After lock, widths never change even
	// if wider cells arrive.
	widthsLocked bool

	// Cursor coordinates in row/col cell space. cursorRow is bounded
	// by [0, len(rows)); cursorCol by [0, len(cols)).
	cursorRow int
	cursorCol int

	// Selection state. selMode==SelectionNone means anchor is unset
	// and Yank() collapses to the cell under the cursor.
	selMode   SelectionMode
	anchorRow int
	anchorCol int

	// Frozen-first-column toggle. When true, column 0 always renders
	// at screen X=0 regardless of horizontal scroll.
	frozenFirstCol bool

	// Horizontal scroll offset in column-count (not cells). 0 means
	// the leftmost column is at screen X=0 (or X=col[0].width when
	// frozenFirstCol is on).
	colOffset int

	// Vertical scroll offset in rows. 0 means row 0 is at the first
	// data line. Render keeps cursorRow inside the visible window by
	// adjusting rowOffset before drawing.
	rowOffset int

	// onNearTail is invoked once each time the cursor crosses into
	// the prefetch window near the loaded tail. A nil callback is
	// treated as "auto-prefetch disabled" — used in tests and the
	// pre-wiring orchestrator state.
	onNearTail func(n int)

	// lastNearTailFireAt is the rows-length at which onNearTail was
	// last invoked. Re-firing is gated on rows growing past this
	// value, so a stationary cursor near the tail doesn't spam the
	// callback once per movement.
	lastNearTailFireAt int

	clipboard ClipboardWriter
}

// NewView returns an empty grid in its initial state: no rows, no
// columns, cursor at (0,0), selection cleared, clipboard set to the
// no-op writer. Safe to use without further configuration; SetColumns
// + AppendRows + Render bring it to life.
func NewView() *View {
	return &View{
		clipboard:          noopClipboard{},
		lastNearTailFireAt: -1,
	}
}

// SetTitle installs the title shown in the host gocui view's frame.
// Safe from any goroutine; the title is read into the target view's
// Title field on the next Render.
func (v *View) SetTitle(t string) {
	v.mu.Lock()
	v.title = t
	v.mu.Unlock()
}

// Title returns the currently configured title string.
func (v *View) Title() string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.title
}

// SetColumns installs the result-set schema and resets all derived
// state: row buffer is cleared, cursor moves to (0,0), selection is
// cleared, widths are reset to the unsized sentinel. Idempotent for
// the same column list (still resets — the manager guarantees a fresh
// taskKey before re-installing the same schema).
func (v *View) SetColumns(cols []models.ColumnMeta) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.cols = append([]models.ColumnMeta(nil), cols...)
	v.rows = v.rows[:0]
	v.widths = make([]int, len(cols))
	for i := range v.widths {
		v.widths[i] = -1
	}
	v.widthsLocked = false
	v.cursorRow = 0
	v.cursorCol = 0
	v.rowOffset = 0
	v.colOffset = 0
	v.selMode = SelectionNone
	v.anchorRow = 0
	v.anchorCol = 0
	v.lastNearTailFireAt = -1
}

// AppendRows extends the row buffer. Concurrency-safe (held under the
// write lock for the duration of the append + auto-size pass). After
// AutoSizeSampleRowCount rows have been observed, widths are frozen.
func (v *View) AppendRows(rows []models.Row) {
	if len(rows) == 0 {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.rows = append(v.rows, rows...)
	if !v.widthsLocked {
		v.autoSizeFromSampleLocked()
	}
}

// RowCount returns the number of rows currently buffered. Safe from
// any goroutine.
func (v *View) RowCount() int {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return len(v.rows)
}

// ColumnCount returns the number of configured columns.
func (v *View) ColumnCount() int {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return len(v.cols)
}

// SetOnNearTail wires the auto-prefetch callback. Pass nil to disable.
func (v *View) SetOnNearTail(fn func(n int)) {
	v.mu.Lock()
	v.onNearTail = fn
	v.mu.Unlock()
}

// SetClipboard installs the writer used by Yank. A nil writer is
// normalised to the no-op clipboard so Yank can always invoke Write.
func (v *View) SetClipboard(w ClipboardWriter) {
	v.mu.Lock()
	if w == nil {
		v.clipboard = noopClipboard{}
	} else {
		v.clipboard = w
	}
	v.mu.Unlock()
}

// ToggleFrozenFirstColumn flips the frozen-first-column rendering mode.
// When on, column 0 is always drawn at screen X=0 regardless of the
// horizontal scroll position.
func (v *View) ToggleFrozenFirstColumn() {
	v.mu.Lock()
	v.frozenFirstCol = !v.frozenFirstCol
	v.mu.Unlock()
}

// FrozenFirstColumn reports whether the first column is currently
// pinned at screen X=0.
func (v *View) FrozenFirstColumn() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.frozenFirstCol
}

// snapshot returns a shallow read-only view of the grid state suitable
// for a single Render pass. The caller must not mutate any of the
// returned slices. Held under RLock for the duration of the copy; the
// row/col slices share backing memory with the View — safe because the
// View only ever appends to rows (never overwrites in place) and
// replaces cols+widths wholesale under the write lock.
func (v *View) snapshot() viewSnapshot {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return viewSnapshot{
		rows:           v.rows,
		cols:           v.cols,
		widths:         v.widths,
		title:          v.title,
		cursorRow:      v.cursorRow,
		cursorCol:      v.cursorCol,
		rowOffset:      v.rowOffset,
		colOffset:      v.colOffset,
		selMode:        v.selMode,
		anchorRow:      v.anchorRow,
		anchorCol:      v.anchorCol,
		frozenFirstCol: v.frozenFirstCol,
	}
}

// viewSnapshot is the immutable bundle Render works against. Decoupled
// from View so render-time mutation of the underlying fields by a
// concurrent AppendRows can't tear the draw mid-frame.
type viewSnapshot struct {
	rows   []models.Row
	cols   []models.ColumnMeta
	widths []int
	title  string

	cursorRow int
	cursorCol int
	rowOffset int
	colOffset int

	selMode   SelectionMode
	anchorRow int
	anchorCol int

	frozenFirstCol bool
}

// Render draws the current grid into the target gocui view. target may
// be nil — used by tests that exercise the lifecycle without a tcell
// screen. When non-nil the target's buffer is fully replaced (Clear +
// WriteString) so stale content from a prior frame never bleeds through.
//
// The render layout:
//
//	row 0    : header row (column names, type styling implied)
//	row 1..N : data rows, with the cell under the cursor / inside the
//	           selection painted with the SelectedRowBg style
//
// Title is written into target.Title verbatim (gocui draws it into the
// view frame). Empty title is allowed; the frame just doesn't show one.
func (v *View) Render(target *gocui.View) {
	snap := v.snapshot()

	if target != nil {
		target.Title = snap.title
		target.Clear()
	}

	if len(snap.cols) == 0 {
		if target != nil {
			target.WriteString(EmptyResultIndicator)
		}
		return
	}

	// Available width / height inside the frame. For a nil target
	// (test path) we fall back to a sensible default so renderBody
	// still exercises its layout logic.
	innerW, innerH := 80, 24
	if target != nil {
		iw, ih := target.InnerSize()
		if iw > 0 {
			innerW = iw
		}
		if ih > 0 {
			innerH = ih
		}
	}

	// Adjust scroll offsets so the cursor stays in the visible
	// window. This is a snapshot-local computation; the persisted
	// offsets are updated under the write lock so the next frame
	// starts in the right place.
	snap.rowOffset, snap.colOffset = v.clampOffsetsLocked(snap, innerW, innerH)

	body := renderBody(snap, innerW, innerH)
	if target != nil {
		target.WriteString(body)
	}

	// Auto-prefetch trigger: if the cursor is within PrefetchThreshold
	// of the loaded tail and onNearTail hasn't fired since the last
	// growth past it, fire now.
	v.maybeFireNearTail()
}

// clampOffsetsLocked computes the row + column offsets that keep the
// cursor inside the visible window. Pure function of the snapshot +
// viewport size; persists the result back into the View so subsequent
// renders pick up the same scroll position. mu is briefly acquired in
// write mode at the end.
func (v *View) clampOffsetsLocked(snap viewSnapshot, innerW, innerH int) (rowOffset, colOffset int) {
	// Reserve one line for the header.
	dataRows := innerH - 1
	if dataRows < 1 {
		dataRows = 1
	}

	rowOffset = snap.rowOffset
	if snap.cursorRow < rowOffset {
		rowOffset = snap.cursorRow
	}
	if snap.cursorRow >= rowOffset+dataRows {
		rowOffset = snap.cursorRow - dataRows + 1
	}
	if rowOffset < 0 {
		rowOffset = 0
	}

	// Horizontal: walk column widths from colOffset until we've
	// either consumed innerW cells or reached cursorCol. If cursorCol
	// is left of colOffset, snap colOffset to cursorCol. If it's off
	// the right edge, advance colOffset until cursorCol fits.
	colOffset = snap.colOffset
	if snap.cursorCol < colOffset {
		colOffset = snap.cursorCol
	}
	for colOffset < snap.cursorCol {
		// Sum widths from colOffset through cursorCol; if total
		// exceeds innerW, advance colOffset by one.
		used := 0
		for c := colOffset; c <= snap.cursorCol && c < len(snap.widths); c++ {
			used += effectiveWidth(snap.widths, c)
			used++ // column separator
		}
		if used <= innerW {
			break
		}
		colOffset++
	}
	if colOffset < 0 {
		colOffset = 0
	}

	v.mu.Lock()
	v.rowOffset = rowOffset
	v.colOffset = colOffset
	v.mu.Unlock()
	return rowOffset, colOffset
}

// maybeFireNearTail fires the prefetch callback when the cursor is
// within PrefetchThreshold rows of the loaded tail. Re-firing is
// gated on rows growing past lastNearTailFireAt so a stationary
// cursor doesn't spam.
func (v *View) maybeFireNearTail() {
	v.mu.RLock()
	cb := v.onNearTail
	rowsLen := len(v.rows)
	cursorRow := v.cursorRow
	last := v.lastNearTailFireAt
	v.mu.RUnlock()
	if cb == nil || rowsLen == 0 {
		return
	}
	if rowsLen-cursorRow > PrefetchThreshold {
		return
	}
	if rowsLen == last {
		return
	}
	v.mu.Lock()
	v.lastNearTailFireAt = rowsLen
	v.mu.Unlock()
	cb(ResultPrefetchRows)
}

// effectiveWidth returns the per-column width to use for layout,
// clamped to [MinColumnWidth, MaxColumnWidth]. An unsized column
// (-1 sentinel) falls back to MinColumnWidth.
func effectiveWidth(widths []int, col int) int {
	if col < 0 || col >= len(widths) {
		return MinColumnWidth
	}
	w := widths[col]
	if w <= 0 {
		return MinColumnWidth
	}
	if w < MinColumnWidth {
		return MinColumnWidth
	}
	if w > MaxColumnWidth {
		return MaxColumnWidth
	}
	return w
}
