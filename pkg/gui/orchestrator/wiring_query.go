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
	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
)

// editorBufferAdapter satisfies controllers.EditorBufferReader by
// reading the canonical *editor.Buffer hung off QueryEditorContext.
// Buffer is the source of truth (epic dbsavvy-wwd Architecture
// Decision 2); the QUERY_EDITOR view is a mirror written by VimEditor
// on every Insert-mode Passthrough.
//
// Both methods return safe zero values when qec or its Buffer is nil
// so controllers that read pre-wire (early bootstrap) see "" / 0
// instead of panicking.
type editorBufferAdapter struct {
	qec *guicontext.QueryEditorContext
}

func newEditorBufferAdapter(qec *guicontext.QueryEditorContext) *editorBufferAdapter {
	return &editorBufferAdapter{qec: qec}
}

// BufferText returns the canonical Buffer text, or "" when qec or its
// Buffer is nil.
func (a *editorBufferAdapter) BufferText() string {
	if a == nil || a.qec == nil {
		return ""
	}
	buf := a.qec.Buffer()
	if buf == nil {
		return ""
	}
	return buf.String()
}

// CursorOffset returns the byte offset of the canonical Buffer cursor
// into BufferText(), or 0 when qec or its Buffer is nil.
func (a *editorBufferAdapter) CursorOffset() int {
	if a == nil || a.qec == nil {
		return 0
	}
	buf := a.qec.Buffer()
	if buf == nil {
		return 0
	}
	return buf.CursorByteOffset()
}

// SelectionText returns the text covered by the canonical Buffer.Selection
// when Visual mode is live, or ("", false) when no selection exists or
// the wiring is nil. dbsavvy-wwd.7's <leader>r-in-Visual fan-out routes
// through this method.
func (a *editorBufferAdapter) SelectionText() (string, bool) {
	if a == nil || a.qec == nil {
		return "", false
	}
	buf := a.qec.Buffer()
	if buf == nil {
		return "", false
	}
	return buf.SelectionText()
}
