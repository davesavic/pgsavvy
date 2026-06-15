package editor_test

import (
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/editor"
)

// TestInsertAfterDeleteLastLine reproduces the "stuck in insert mode,
// can't type" bug: after `dd` on the last line, the buffer Cursor is
// left pointing at a line that no longer exists. The next insert-mode
// keystroke (what VimEditor.insertKey does — Apply an Insert at the live
// Cursor) then fails with ErrEditOutOfRange and is silently swallowed,
// so typing does nothing while the mode indicator still reads INSERT.
func TestInsertAfterDeleteLastLine(t *testing.T) {
	b := bufFrom("aaa\nbbb")
	// User navigated down to the last line (j).
	if err := editor.SetCursor(b, editor.Position{Line: 1, Col: 0}); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}

	// `dd` on the last line — line-wise delete of line 1 (what the
	// operator.delete spec runs for the doubled-shortcut dd).
	r := editor.Range{
		Start:    editor.Position{Line: 1, Col: 0},
		End:      editor.Position{Line: 1, Col: 0},
		LineWise: true,
	}
	if _, err := editor.Delete(b, r); err != nil {
		t.Fatalf("dd delete: %v", err)
	}

	// Now simulate the very first insert-mode keystroke: Apply an Insert
	// at the live cursor, exactly as VimEditor.insertKey does.
	cur := b.CursorPos()
	err := b.Apply(editor.Edit{
		Kind:  editor.EditKindInsert,
		Range: editor.Range{Start: cur, End: cur},
		Text:  "x",
	})
	if err != nil {
		t.Fatalf("insert at cursor after dd failed (stuck-in-insert bug): cursor=%+v lines=%d err=%v",
			cur, len(b.Lines), err)
	}
}
