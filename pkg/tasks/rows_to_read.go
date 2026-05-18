package tasks

// RowsToRead is the per-pull request value sent on the manager's
// rowsToRead channel. Direct port of lazygit's
// pkg/tasks/tasks.go:LinesToRead (lines 64-75 in the vendored fork)
// re-shaped for SQL row streams: lazygit reads N text lines from a
// bufio.Scanner, we read N models.Row values from a drivers.RowStream.
//
// DESIGN.md §12.1 ("verbatim port") — the field set matches lazygit's
// so the chan-driven pull loop in ResultBufferManager keeps the same
// semantics as ViewBufferManager.
type RowsToRead struct {
	// Total is the number of rows to read. -1 means "drain to end of
	// stream" — the worker keeps pulling rows until RowStream.Next
	// reports clean EOF or an error.
	Total int

	// InitialRefreshAfter is the row count at which the view should
	// be refreshed for the first time during the initial fill. It is
	// only set on the initial request constructed inside
	// NewQueryTask; subsequent ReadRows / ReadToEnd pulls leave it
	// at -1. The chan-driven SQL path does not currently consume this
	// (the initial fill drains synchronously inside NewQueryTask
	// before the chan loop starts), but the field is retained for
	// symmetry with lazygit and so a future caller can switch to a
	// fully chan-driven initial fill without re-shaping the request.
	InitialRefreshAfter int

	// Then is an optional callback invoked after this batch's last
	// row has been delivered to the UI thread. Used by ReadToEnd to
	// notify the caller that a full drain has completed.
	Then func()
}
