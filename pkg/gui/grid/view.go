package grid

import (
	"sync"
	"time"

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

	// rowsAffected is the driver command-tag count for a DML statement
	// that returned no result set (zero cols). When > 0 the empty-body
	// renderers show "(N row(s) affected)" instead of the misleading
	// "(0 rows)". Set once at stream completion via SetRowsAffected;
	// stays 0 for SELECTs and in-flight streams. dbsavvy-outq.
	rowsAffected int64

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

	// onSortRequest is invoked once each time a header double-click
	// detects a qualifying sort request. The grid no longer owns the
	// asc→desc→clear cycle; it just reports the RAW v.cols index and the
	// Tab-level flow (QueryEditorController.sortActiveResult) decides the
	// direction and re-runs the query DB-side. A nil callback means
	// "sort routing not wired" — used in tests and the pre-wiring
	// orchestrator state. dbsavvy-72k.5.
	onSortRequest func(col int)

	clipboard ClipboardWriter

	// searchState carries the active plain-substring SEARCH, if any. See
	// search.go for the type definition and the SetSearch / ClearSearch /
	// NextMatch / PrevMatch / SearchStatus surface. The search never hides
	// rows; it drives cell-major n/N cursor navigation. dbsavvy-2ttm.
	searchState searchState

	// sortState carries the active column sort, if any. See sort.go for
	// the SetSort / SortActive / SortIndicator surface. dbsavvy-uv0.5.
	sortState sortState

	// lastHeaderClick records the column + timestamp of the most recent
	// row-0 (header) left-click. Used by HandleHeaderClick to detect a
	// double-click on the same column inside the configured debounce
	// window. dbsavvy-uv0.5.
	lastHeaderClick headerClickState

	// mouseDoubleClickMs is the maximum gap (in milliseconds) that still
	// counts as a double-click on the same header. 0 falls back to
	// defaultMouseDoubleClickMs. Wired from config at chord-registration
	// time. dbsavvy-uv0.5.
	mouseDoubleClickMs int

	// hiddenColSet is the per-View hide-cols state used by the <leader>gH
	// overlay. Keys are indices into the CURRENT cols slice. SetColumns
	// clears this map (the indices are not stable across schema attaches);
	// callers re-seed from persisted column NAMES via SetHiddenCols.
	// dbsavvy-uv0.6.
	hiddenColSet map[int]bool

	// viewMode picks the render path: ViewModeGrid (default) renders the
	// row/col table; ViewModeExpanded renders one record at a time in
	// psql `\x` style. Persisted globally via AppState.LastResultViewMode
	// — see helpers/ui/result_tabs_helper.go. dbsavvy-uv0.7.
	viewMode string

	// estimatedRowsLoader returns the optimiser's row-count estimate for
	// the active stream, or 0 when unknown. snapshot() invokes it under
	// RLock so the expanded-mode separator can display "~total" without
	// the grid package importing the task runner. nil means "unknown".
	// dbsavvy-uv0.7.
	estimatedRowsLoader func() int64

	// expandedLineOffset is the wrapped-line offset inside the active
	// record in expanded mode. Bumped by WrappedLineDown / WrappedLineUp;
	// reset to 0 when the cursor moves to a new record. dbsavvy-uv0.7.
	expandedLineOffset int

	// viewHeight tracks the data-row capacity of the most recent Render
	// (innerH - 1 for the header). 0 means "no Render yet"; VisibleRows
	// falls back to a no-op in that case. Stamped under v.mu.Lock by
	// clampOffsetsLocked so export-time readers see a consistent value.
	// dbsavvy-uv0.9.
	viewHeight int

	// editable, rowIdentity, disabledReason, identitySchema carry the
	// post-introspection editability state populated by Z1 via
	// SetEditability. SetColumns resets all to zero (a fresh schema attach
	// invalidates the previous decision). identitySchema is the catalog-
	// resolved schema (pg_namespace.nspname) used to schema-qualify the
	// apply-path UPDATE; the SQL-parsed base table loses it for unqualified
	// SELECTs (dbsavvy-8q6). dbsavvy-bwq.2 (F2).
	editable       bool
	rowIdentity    []int
	disabledReason string
	identitySchema string

	// pendingEdits is the per-View staged-edit set. Read by the dirty-cell
	// renderer (DecorateDirtyCell / GutterMarker) and the status indicator
	// (BuildPendingIndicator). Owned externally — A1 records edits, A7
	// clears them on discard. Nil means "no edits staged" (treated as
	// IsEmpty by the helpers). dbsavvy-bwq.6 (A3).
	pendingEdits *models.PendingEditSet
}

// headerClickState is the per-View state used by HandleHeaderClick to
// detect a double-click. col == -1 means "no prior click recorded".
// dbsavvy-uv0.5.
type headerClickState struct {
	col int
	t   time.Time
}

// defaultMouseDoubleClickMs mirrors the config default
// (ui.mouse.double_click_ms = 400). dbsavvy-uv0.5.
const defaultMouseDoubleClickMs = 400

// NewView returns an empty grid in its initial state: no rows, no
// columns, cursor at (0,0), selection cleared, clipboard set to the
// no-op writer. Safe to use without further configuration; SetColumns
// + AppendRows + Render bring it to life.
func NewView() *View {
	return &View{
		clipboard:          noopClipboard{},
		lastNearTailFireAt: -1,
		lastHeaderClick:    headerClickState{col: -1},
		mouseDoubleClickMs: defaultMouseDoubleClickMs,
	}
}

// SetMouseDoubleClickMs installs the maximum gap (in milliseconds) that
// counts as a double-click on the same column header. n <= 0 falls back
// to defaultMouseDoubleClickMs. Wired from config at chord-registration
// time. dbsavvy-uv0.5.
func (v *View) SetMouseDoubleClickMs(n int) {
	if n <= 0 {
		n = defaultMouseDoubleClickMs
	}
	v.mu.Lock()
	v.mouseDoubleClickMs = n
	v.mu.Unlock()
}

// SetTitle installs the title shown in the host gocui view's frame.
// Safe from any goroutine; the title is read into the target view's
// Title field on the next Render.
func (v *View) SetTitle(t string) {
	v.mu.Lock()
	v.title = t
	v.mu.Unlock()
}

// Title returns the currently configured title string with the sort
// indicator appended when a sort is active. The base title set via
// SetTitle is left untouched; the indicator is applied here so callers
// (and the result-tab layout pass) see the dynamic decoration without
// having to re-call SetTitle on every sort flip. dbsavvy-uv0.5.
func (v *View) Title() string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.title + sortIndicatorLocked(v.sortState, v.cols)
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
	// Clear any active search — a new schema attach invalidates the
	// cell-major match list (row/col indices are not stable across a
	// schema reset). dbsavvy-2ttm.
	v.searchState = searchState{}
	// Clear any active sort: a fresh schema attach resets sort/hide/filter
	// (dbsavvy-uv0 AD-5). T6 will reseed hide-cols from AppState after this
	// point in its own SetColumns extension.
	v.sortState = sortState{}
	v.lastHeaderClick = headerClickState{col: -1}
	// Clear hide-cols: int indices are not stable across schema attaches.
	// Callers reseed via SetHiddenCols after re-translating persisted names
	// against the new cols slice. dbsavvy-uv0.6 AD-5.
	v.hiddenColSet = nil
	// Reset editability — the prior introspection no longer describes
	// the new schema. Z1 re-runs EditabilityIntrospect on each schema
	// attach. dbsavvy-bwq.2 (F2).
	v.editable = false
	v.rowIdentity = nil
	v.disabledReason = ""
	v.identitySchema = ""

	// Seed column widths from headers so an empty result (0 rows) still
	// renders full-width headings instead of falling back to MinColumnWidth.
	// widthsLocked stays false, so a later AppendRows re-evaluates.
	v.autoSizeFromSampleLocked()
}

// SetEditability installs the post-introspection editability decision.
// All fields are stored atomically under the write lock so a concurrent
// reader sees a consistent snapshot. A nil rowIdentity is stored as-is;
// getters return defensive copies. schema is the catalog-resolved schema
// of the base relation (empty when unknown). dbsavvy-bwq.2 (F2),
// dbsavvy-8q6 (schema).
func (v *View) SetEditability(editable bool, rowIdentity []int, disabledReason, schema string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.editable = editable
	if len(rowIdentity) == 0 {
		v.rowIdentity = nil
	} else {
		v.rowIdentity = append([]int(nil), rowIdentity...)
	}
	v.disabledReason = disabledReason
	v.identitySchema = schema
}

// Editable reports whether the current result set is inline-editable.
func (v *View) Editable() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.editable
}

// RowIdentity returns a defensive copy of the SELECT-order indexes that
// form the minimal row identity. Returns nil when no row identity is set.
func (v *View) RowIdentity() []int {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if len(v.rowIdentity) == 0 {
		return nil
	}
	out := make([]int, len(v.rowIdentity))
	copy(out, v.rowIdentity)
	return out
}

// IdentitySchema returns the catalog-resolved schema of the editable base
// relation, or "" when unknown / not editable. The apply path uses it to
// schema-qualify the UPDATE so an unqualified SELECT against a non-public
// table still writes back correctly (dbsavvy-8q6).
func (v *View) IdentitySchema() string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.identitySchema
}

// DisabledReason returns the frozen reason string explaining why the
// result is not inline-editable. Empty when Editable() is true.
func (v *View) DisabledReason() string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.disabledReason
}

// SetHiddenCols installs the set of column indices to hide from the
// render. A nil / empty set clears any prior hide state. Indices outside
// [0, len(cols)) are silently dropped — caller is responsible for
// translating persisted column NAMES to indices against the current
// columns slice before calling. dbsavvy-uv0.6.
func (v *View) SetHiddenCols(set map[int]bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(set) == 0 {
		v.hiddenColSet = nil
		return
	}
	out := make(map[int]bool, len(set))
	for k, val := range set {
		if !val {
			continue
		}
		if k < 0 || k >= len(v.cols) {
			continue
		}
		out[k] = true
	}
	if len(out) == 0 {
		v.hiddenColSet = nil
		return
	}
	v.hiddenColSet = out
	// If the column under the cursor was just hidden, move the cursor to
	// a visible neighbor so it never renders invisibly. dbsavvy hidden-col
	// navigation fix.
	v.snapCursorOffHiddenLocked()
}

// HiddenCols returns a defensive copy of the current hidden-col index
// set. Callers may mutate the returned map without affecting view state.
// Returns nil when no columns are hidden. dbsavvy-uv0.6.
func (v *View) HiddenCols() map[int]bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if len(v.hiddenColSet) == 0 {
		return nil
	}
	out := make(map[int]bool, len(v.hiddenColSet))
	for k, val := range v.hiddenColSet {
		if val {
			out[k] = true
		}
	}
	return out
}

// HiddenColumnNames returns the names of currently-hidden columns in
// the View's column order. Names of indices that fall outside the
// current cols slice are silently skipped. Used by the helper to
// translate the runtime int-set into the persisted []string for
// AppState.HiddenColumns. dbsavvy-uv0.6.
func (v *View) HiddenColumnNames() []string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if len(v.hiddenColSet) == 0 {
		return nil
	}
	out := make([]string, 0, len(v.hiddenColSet))
	for i := 0; i < len(v.cols); i++ {
		if v.hiddenColSet[i] {
			out = append(out, v.cols[i].Name)
		}
	}
	return out
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

// AllRows returns a snapshot of every buffered row (header excluded).
// The returned slice is a fresh allocation, so the caller may iterate
// or mutate it while concurrent AppendRows continues without observing
// torn state. Used by the export pipeline's Scope=All path.
// dbsavvy-uv0.9.
func (v *View) AllRows() []models.Row {
	v.mu.RLock()
	defer v.mu.RUnlock()
	out := make([]models.Row, len(v.rows))
	copy(out, v.rows)
	return out
}

// VisibleRows returns a defensive copy of the rows currently inside the
// rendered viewport — the same window the user sees on screen. The
// window is [rowOffset, rowOffset+viewHeight), clamped to the buffer.
// When no Render has happened yet (viewHeight == 0) or the buffer is
// empty, an empty slice is returned. Used by the export pipeline's
// Scope=Visible path. dbsavvy-uv0.9.
func (v *View) VisibleRows() []models.Row {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if len(v.rows) == 0 || v.viewHeight <= 0 {
		return []models.Row{}
	}
	start := v.rowOffset
	if start < 0 {
		start = 0
	}
	if start > len(v.rows) {
		start = len(v.rows)
	}
	end := start + v.viewHeight
	if end > len(v.rows) {
		end = len(v.rows)
	}
	out := make([]models.Row, end-start)
	copy(out, v.rows[start:end])
	return out
}

// ColumnCount returns the number of configured columns.
func (v *View) ColumnCount() int {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return len(v.cols)
}

// Columns returns a defensive copy of the column-metadata slice. Used by
// the <leader>oe export pipeline to feed exporter.RowSource. Returns nil
// when no columns are configured. dbsavvy-uv0.9.
func (v *View) Columns() []models.ColumnMeta {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if len(v.cols) == 0 {
		return nil
	}
	out := make([]models.ColumnMeta, len(v.cols))
	copy(out, v.cols)
	return out
}

// ColumnName returns the configured column name at index i, or "" when
// i is out of range. Used by the sort picker to render the column-name
// overlay. dbsavvy-uv0.5.
func (v *View) ColumnName(i int) string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if i < 0 || i >= len(v.cols) {
		return ""
	}
	return v.cols[i].Name
}

// SetOnNearTail wires the auto-prefetch callback. Pass nil to disable.
func (v *View) SetOnNearTail(fn func(n int)) {
	v.mu.Lock()
	v.onNearTail = fn
	v.mu.Unlock()
}

// SetOnSortRequest wires the header double-click sort hook. The callback
// receives the RAW v.cols index the user double-clicked; routing it (and
// the asc→desc→clear cycle) is the Tab-level flow's responsibility. Pass
// nil to disable. dbsavvy-72k.5.
func (v *View) SetOnSortRequest(fn func(col int)) {
	v.mu.Lock()
	v.onSortRequest = fn
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
		rows: v.rows,
		cols: v.cols,
		// The rendered title carries the dynamic sort indicator (e.g.
		// " (sort: name ↑)") computed under the same RLock so Render
		// sees a tearing-free combination of base title + sort flip.
		// dbsavvy-uv0.5.
		widths:             v.widths,
		title:              v.title + sortIndicatorLocked(v.sortState, v.cols),
		cursorRow:          v.cursorRow,
		cursorCol:          v.cursorCol,
		rowOffset:          v.rowOffset,
		colOffset:          v.colOffset,
		selMode:            v.selMode,
		anchorRow:          v.anchorRow,
		anchorCol:          v.anchorCol,
		frozenFirstCol:     v.frozenFirstCol,
		searchMatches:      copyMatches(v.searchState.matches),
		searchCurrentIdx:   v.searchState.current,
		searchActive:       v.searchState.query != "",
		searchQuery:        v.searchState.query,
		hidden:             v.hiddenColSet,
		viewMode:           normaliseViewMode(v.viewMode),
		estimatedRows:      v.loadEstimatedRowsLocked(),
		expandedLineOffset: v.expandedLineOffset,
		pendingEdits:       v.pendingEdits,
		rowIdentity:        v.rowIdentity,
		rowsAffected:       v.rowsAffected,
	}
}

// SetRowsAffected records the driver command-tag affected-row count for a
// DML statement that returned no result set. The empty-body renderers use
// it to show "(N row(s) affected)" in place of "(0 rows)". Safe from any
// goroutine. dbsavvy-outq.
func (v *View) SetRowsAffected(n int64) {
	v.mu.Lock()
	v.rowsAffected = n
	v.mu.Unlock()
}

// loadEstimatedRowsLocked invokes the configured loader (under v.mu).
// Returns 0 when no loader is wired or the loader reports unknown.
// Used by snapshot() to surface "~total" in the expanded mode banner
// without grid importing the task runner. dbsavvy-uv0.7.
func (v *View) loadEstimatedRowsLocked() int64 {
	if v.estimatedRowsLoader == nil {
		return 0
	}
	n := v.estimatedRowsLoader()
	if n < 0 {
		return 0
	}
	return n
}

// SetViewMode flips the render path. Accepts ViewModeGrid /
// ViewModeExpanded; any other value falls back to ViewModeGrid. Moving
// to a new mode resets the expanded line-offset so the next render
// starts at the top of the active record. dbsavvy-uv0.7.
func (v *View) SetViewMode(m string) {
	v.mu.Lock()
	v.viewMode = normaliseViewMode(m)
	v.expandedLineOffset = 0
	v.mu.Unlock()
}

// ViewMode returns the current render mode (ViewModeGrid or
// ViewModeExpanded). Safe from any goroutine. dbsavvy-uv0.7.
func (v *View) ViewMode() string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return normaliseViewMode(v.viewMode)
}

// SetEstimatedRowsLoader wires the row-count-estimate provider for
// expanded-mode rendering. The loader is invoked on every snapshot so
// the banner stays current as the optimiser estimate is refined. Pass
// nil to clear. dbsavvy-uv0.7.
func (v *View) SetEstimatedRowsLoader(fn func() int64) {
	v.mu.Lock()
	v.estimatedRowsLoader = fn
	v.mu.Unlock()
}

// viewSnapshot is the immutable bundle Render works against. Decoupled
// from View so render-time mutation of the underlying fields by a
// concurrent AppendRows can't tear the draw mid-frame.
type viewSnapshot struct {
	rows   []models.Row
	cols   []models.ColumnMeta
	widths []int
	title  string

	// rowsAffected drives the empty-body text for DML without a result
	// set. > 0 only at stream completion for changing statements. dbsavvy-outq.
	rowsAffected int64

	cursorRow int
	cursorCol int
	rowOffset int
	colOffset int

	selMode   SelectionMode
	anchorRow int
	anchorCol int

	frozenFirstCol bool

	// Search projection inputs. The highlight pass (T2) reads these
	// (never v.searchState directly) so a concurrent SetSearch / Next /
	// Prev cannot tear the draw between snapshot capture and render.
	// searchMatches is a DEFENSIVE COPY of the live slice. dbsavvy-2ttm.
	searchMatches    []cellMatch
	searchCurrentIdx int
	searchActive     bool
	searchQuery      string

	// hidden is the index-set of columns to skip in visibleColumnOrder.
	// Captured under the same RLock as the rest of the snapshot so a
	// concurrent SetHiddenCols cannot tear the frame. dbsavvy-uv0.6.
	hidden map[int]bool

	// viewMode + estimatedRows are the expanded-mode projection inputs.
	// viewMode is normalised at snapshot time so renderExpanded never
	// has to guess; estimatedRows is the loader's last-known value.
	// dbsavvy-uv0.7.
	viewMode           string
	estimatedRows      int64
	expandedLineOffset int

	// pendingEdits + rowIdentity are the dirty-cell projection inputs.
	// renderDataLine reads the snapshot (never v.* directly) so a
	// concurrent SetPendingEdits / SetEditability cannot tear the frame.
	// rowIdentity holds the SELECT-order PK column indexes used to match a
	// row against staged edits. dbsavvy-cyh.
	pendingEdits *models.PendingEditSet
	rowIdentity  []int
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
			target.WriteString(emptyResultText(snap))
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

	var body string
	if snap.viewMode == ViewModeExpanded {
		body = renderExpanded(snap, innerW, innerH)
	} else {
		body = renderBody(snap, innerW, innerH)
	}
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

	// cursorRow is a raw-buffer index, but the viewport scrolls over the
	// projected (filter -> sort -> hide) row order. Translate the cursor
	// to its position within the projection so the visible window follows
	// the row the cursor actually renders on — without this the viewport
	// scrolls in raw-index space and the cursor falls off-screen whenever
	// a sort reorders the buffer. Falls back to position 0 when the
	// cursor's row isn't in the projection (e.g. filtered out).
	// dbsavvy-dr6.
	proj := project(snap)
	projectedCount := len(proj)
	cursorPos := projectedPos(proj, snap.cursorRow)
	if cursorPos < 0 {
		cursorPos = 0
	}
	rowOffset = snap.rowOffset
	if cursorPos < rowOffset {
		rowOffset = cursorPos
	}
	if cursorPos >= rowOffset+dataRows {
		rowOffset = cursorPos - dataRows + 1
	}
	if rowOffset < 0 {
		rowOffset = 0
	}
	// Don't allow rowOffset to point past the projected tail.
	if projectedCount > 0 && rowOffset > projectedCount-1 {
		rowOffset = projectedCount - 1
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
			used += ColSepWidth // separator; must match renderHeaderLine/renderDataLine
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
	// Record the latest data-row capacity so VisibleRows (export Scope)
	// can return the on-screen window without re-deriving the layout.
	// dbsavvy-uv0.9.
	v.viewHeight = dataRows
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

// HandleHeaderClick processes a mouse left-click at view-relative (x, y).
// y == 0 means the click landed on the header row; y > 0 is a data-cell
// click and is currently a no-op (preserves the row-select invariant from
// the AC).
//
// On a header click the (x) coordinate is translated to a column index
// using the snapshot's column-width layout (frozen-first-col + colOffset
// honored). If a prior header click on the SAME column happened within
// the configured mouseDoubleClickMs window, this click counts as a
// double-click and onSortRequest(col) fires (when wired) with the RAW
// v.cols index — the grid no longer owns the sort cycle; the Tab-level
// flow does (dbsavvy-72k.5). Otherwise the click is recorded so the NEXT
// click against the same column within the window completes the
// double-click. The recorded col is reset on any click against a
// different column.
//
// The now argument is injected so tests can drive the debounce state
// machine deterministically; production callers pass time.Now().
//
// dbsavvy-uv0.5.
func (v *View) HandleHeaderClick(x, y int, now time.Time) {
	if y != 0 {
		// Non-header click. AC: data-row clicks must NOT alter sort state
		// or the recorded prior-click; only row==0 participates.
		return
	}
	col := v.headerColumnAt(x)
	if col < 0 {
		return
	}
	v.mu.Lock()
	window := time.Duration(v.mouseDoubleClickMs) * time.Millisecond
	prior := v.lastHeaderClick
	if prior.col == col && !prior.t.IsZero() && now.Sub(prior.t) <= window {
		// Inside window: this is the second click of the pair → sort request.
		// Reset prior-click so the NEXT click starts a fresh first-click. The
		// grid only DETECTS the double-click here; the asc→desc→clear cycle
		// and DB re-run run through the Tab-level onSortRequest sink with the
		// RAW v.cols index (dbsavvy-72k.5). Read the hook under lock, then
		// release before invoking it (mirrors maybeFireNearTail).
		v.lastHeaderClick = headerClickState{col: -1}
		cb := v.onSortRequest
		v.mu.Unlock()
		if cb != nil {
			cb(col)
		}
		return
	}
	// Either no prior click, prior on a different column, or window
	// expired — record this as the new first-click.
	v.lastHeaderClick = headerClickState{col: col, t: now}
	v.mu.Unlock()
}

// headerColumnAt translates a view-relative x coordinate to the column
// index under the header at that position, or -1 when x lands beyond the
// visible columns. Mirrors the layout walk in renderHeaderLine so the
// hit-test stays in sync with what the user sees on screen.
//
// Caller must NOT hold v.mu; the function acquires its own RLock.
func (v *View) headerColumnAt(x int) int {
	snap := v.snapshot()
	if len(snap.cols) == 0 || x < 0 {
		return -1
	}
	used := 0
	for _, c := range visibleColumnOrder(snap) {
		w := effectiveWidth(snap.widths, c)
		// Column c occupies [used, used+w); the separator region between
		// columns is NOT part of the clickable region (clicking the gap
		// falls into neither).
		if x >= used && x < used+w {
			return c
		}
		used += w + ColSepWidth
	}
	return -1
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
