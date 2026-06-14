package grid

// Defaults wired throughout the grid package. Exported because callers
// (keymap wiring, ResultBufferManager seeding) read them to
// stay in lock-step with the View's own sizing assumptions.
const (
	// ResultInitialRows is the size of the synchronous initial-fill
	// drain in ResultBufferManager.NewQueryTask (DESIGN.md §12.1).
	ResultInitialRows = 200

	// ResultPrefetchRows is the page size requested when the cursor
	// crosses PrefetchThreshold of the loaded tail.
	ResultPrefetchRows = 50

	// ResultPageSize is the default page size for explicit ReadRows
	// requests issued by keyboard navigation.
	ResultPageSize = 200

	// MinColumnWidth is the floor for auto-sized column widths in
	// display cells. Below this value column headers become unreadable.
	MinColumnWidth = 6

	// MaxColumnWidth is the ceiling for auto-sized column widths. Cells
	// exceeding this width are truncated with a trailing ellipsis.
	MaxColumnWidth = 64

	// AutoSizeSampleRowCount is the number of rows sampled before
	// column widths are frozen. Subsequent overlong cells truncate.
	AutoSizeSampleRowCount = 100

	// PrefetchThreshold is the distance (in rows) from the loaded tail
	// at which the View fires OnNearTail to request more rows.
	PrefetchThreshold = 25

	// ColSepWidth is the display-column width of the separator between
	// columns. renderHeaderLine uses "·│·" (space-pipe-space) and
	// renderDataLine uses "···" (three spaces) — both 3 display columns.
	// headerColumnAt must use the same constant so click hit-testing
	// stays aligned with what the user sees.
	ColSepWidth = 3

	// MaxCellRenderBytes is the safety cap on per-cell stringification
	// before truncation kicks in regardless of column width. Guards
	// against pathological 10 MB JSON cells flowing through fmt.Sprintf.
	MaxCellRenderBytes = 10 * 1024

	// EmptyResultIndicator is the placeholder shown when no rows have
	// been appended yet.
	EmptyResultIndicator = "(0 rows)"
)

// SelectionMode enumerates the active vim-style selection mode.
type SelectionMode int

const (
	// SelectionNone — no active selection. Cursor still moves.
	SelectionNone SelectionMode = iota
	// SelectionCell — single cell (vim 'v' on a grid cell).
	SelectionCell
	// SelectionRow — full row (vim 'V').
	SelectionRow
	// SelectionBlock — rectangular block (vim '<C-v>').
	SelectionBlock
)
