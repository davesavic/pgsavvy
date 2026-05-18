// wiring_query.go centralises the per-Connect wiring of the query
// runtime: the orchestrator owns one process-wide *query.History opened
// lazily on the first wireWithDriver pass, and one empty *data.QueryRunner
// stashed in the HelperBag at construction time. connectInvoker.Connect
// (see adapters.go) acquires a SECOND drivers.Session from the live
// Connection — ConnectHelper's own session keeps serving the schema rail
// — builds a SQLSession around that second session with the History as
// HistoryRecorder, and Bind()s the QueryRunner so the controller sees a
// HasSession()==true runner on the next <leader>r.
//
// dbsavvy-66p.16.
package orchestrator

import (
	"strings"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// editorBufferAdapter satisfies controllers.EditorBufferReader by
// reading the live QUERY_EDITOR view via the GuiDriver. BufferText is a
// straight passthrough to driver.GetViewBuffer; CursorOffset walks the
// view buffer to translate the (origin+cursor) grid position into a
// byte offset.
//
// Both methods return safe zero values when the view has not been
// created yet (early bootstrap, before the QueryEditorContext's first
// layout pass). The controller already nil-checks the helper bag's
// EditorBuffer; this adapter additionally tolerates a missing view so
// no spurious panic / error fires during the layout race.
type editorBufferAdapter struct {
	driver types.GuiDriver
}

func newEditorBufferAdapter(d types.GuiDriver) *editorBufferAdapter {
	return &editorBufferAdapter{driver: d}
}

// BufferText returns the full text in the QUERY_EDITOR view, or "" when
// the view does not exist yet.
func (a *editorBufferAdapter) BufferText() string {
	if a == nil || a.driver == nil {
		return ""
	}
	return a.driver.GetViewBuffer(string(types.QUERY_EDITOR))
}

// CursorOffset returns the byte offset of the QUERY_EDITOR caret into
// BufferText(). The grid position is (origin.x + cursor.x, origin.y +
// cursor.y); we walk the buffer line-by-line to sum byte lengths.
//
// Returns 0 when the view has not been created yet OR when the driver
// returns a nil View handle (the recorder driver in tests does that
// for known views). Out-of-range grid positions are clamped: an
// over-shot row falls back to the buffer length; an over-shot column
// falls back to the line length.
func (a *editorBufferAdapter) CursorOffset() int {
	if a == nil || a.driver == nil {
		return 0
	}
	v, err := a.driver.ViewByName(string(types.QUERY_EDITOR))
	if err != nil || v == nil {
		return 0
	}
	cx, cy := v.Cursor()
	ox, oy := v.Origin()
	row := cy + oy
	col := cx + ox
	if row < 0 {
		row = 0
	}
	if col < 0 {
		col = 0
	}

	// Prefer the view's own Buffer() so this method does not race the
	// driver's content sink with the BufferText() reader above.
	buf := v.Buffer()
	if buf == "" {
		return 0
	}
	lines := strings.Split(buf, "\n")
	if row >= len(lines) {
		// Cursor parked past the final \n — return total length.
		return len(buf)
	}
	// Sum every full line BEFORE row (including its trailing \n).
	offset := 0
	for i := 0; i < row; i++ {
		offset += len(lines[i]) + 1 // +1 for the newline
	}
	// Within the target row, clamp the column to the line length.
	if col > len(lines[row]) {
		col = len(lines[row])
	}
	offset += col
	if offset > len(buf) {
		offset = len(buf)
	}
	return offset
}
