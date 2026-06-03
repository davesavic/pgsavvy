package orchestrator

import "testing"

// InsertAtCursor must no-op (return nil, no panic) when the adapter or
// its QueryEditorContext is nil — mirroring the ReplaceAll/ReplaceSelection
// guards so a pre-wire (early-bootstrap) caller never crashes. The
// nil-Buffer branch is unreachable from outside package context
// (NewQueryEditorContext always seeds a non-nil Buffer and SetBuffer
// rejects nil), so it is not exercised here.
func TestEditorBufferAdapterInsertAtCursorNilGuards(t *testing.T) {
	var nilAdapter *editorBufferAdapter
	if err := nilAdapter.InsertAtCursor("x"); err != nil {
		t.Fatalf("nil adapter InsertAtCursor = %v, want nil", err)
	}

	nilQec := newEditorBufferAdapter(nil)
	if err := nilQec.InsertAtCursor("x"); err != nil {
		t.Fatalf("nil-qec adapter InsertAtCursor = %v, want nil", err)
	}
}
