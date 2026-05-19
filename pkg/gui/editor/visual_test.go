package editor

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

func TestEnterVisualCharSeedsSelectionAtCursor(t *testing.T) {
	b := bufFromLines("SELECT 1")
	b.Cursor = Position{Line: 0, Col: 3}
	EnterVisual(b, types.ModeVisual)
	if b.Selection == nil {
		t.Fatalf("Selection is nil after EnterVisual")
	}
	if b.Selection.Start != (Position{0, 3}) || b.Selection.End != (Position{0, 3}) {
		t.Fatalf("Selection = %+v, want {Start:{0,3},End:{0,3}}", *b.Selection)
	}
	if b.Selection.LineWise || b.Selection.BlockWise {
		t.Fatalf("char-wise visual should not set LineWise/BlockWise: %+v", *b.Selection)
	}
}

func TestEnterVisualLineFlagsLineWise(t *testing.T) {
	b := bufFromLines("a", "b")
	b.Cursor = Position{Line: 1, Col: 0}
	EnterVisual(b, types.ModeVisualLine)
	if b.Selection == nil || !b.Selection.LineWise || b.Selection.BlockWise {
		t.Fatalf("Selection = %+v, want LineWise true, BlockWise false", b.Selection)
	}
}

func TestEnterVisualBlockFlagsBlockWise(t *testing.T) {
	b := bufFromLines("a", "b")
	EnterVisual(b, types.ModeVisualBlock)
	if b.Selection == nil || !b.Selection.BlockWise || b.Selection.LineWise {
		t.Fatalf("Selection = %+v, want BlockWise true, LineWise false", b.Selection)
	}
}

func TestEnterVisualUnknownModeNoOp(t *testing.T) {
	b := bufFromLines("a")
	EnterVisual(b, types.ModeNormal)
	if b.Selection != nil {
		t.Fatalf("Selection should be nil for ModeNormal, got %+v", *b.Selection)
	}
}

func TestEnterVisualNilBufferSafe(t *testing.T) {
	EnterVisual(nil, types.ModeVisual)
}

func TestExitVisualClearsSelection(t *testing.T) {
	b := bufFromLines("abc")
	b.Selection = &Range{Start: Position{0, 0}, End: Position{0, 3}}
	ExitVisual(b)
	if b.Selection != nil {
		t.Fatalf("Selection should be nil after ExitVisual, got %+v", *b.Selection)
	}
}

func TestExitVisualNilBufferSafe(t *testing.T) {
	ExitVisual(nil)
}

func TestExitVisualNoSelectionNoOp(t *testing.T) {
	b := bufFromLines("a")
	ExitVisual(b) // should not panic
	if b.Selection != nil {
		t.Fatalf("Selection should remain nil")
	}
}

func TestExtendSelectionMovesEndAndCursor(t *testing.T) {
	b := bufFromLines("hello world")
	b.Cursor = Position{Line: 0, Col: 0}
	EnterVisual(b, types.ModeVisual)
	ExtendSelection(b, Position{Line: 0, Col: 5})
	if b.Selection.End != (Position{0, 5}) {
		t.Fatalf("Selection.End = %+v, want {0,5}", b.Selection.End)
	}
	if b.Selection.Start != (Position{0, 0}) {
		t.Fatalf("Selection.Start moved: %+v, want {0,0}", b.Selection.Start)
	}
	if b.Cursor != (Position{0, 5}) {
		t.Fatalf("Cursor = %+v, want {0,5}", b.Cursor)
	}
}

func TestExtendSelectionBackwardsAllowed(t *testing.T) {
	b := bufFromLines("hello")
	b.Cursor = Position{Line: 0, Col: 3}
	EnterVisual(b, types.ModeVisual)
	ExtendSelection(b, Position{Line: 0, Col: 1})
	// Vim allows backwards selection; operator handlers normalise later.
	if b.Selection.Start != (Position{0, 3}) || b.Selection.End != (Position{0, 1}) {
		t.Fatalf("Selection = %+v, want Start={0,3} End={0,1}", *b.Selection)
	}
}

func TestExtendSelectionNoSelectionNoOp(t *testing.T) {
	b := bufFromLines("a")
	b.Cursor = Position{0, 0}
	ExtendSelection(b, Position{0, 1})
	if b.Selection != nil {
		t.Fatalf("ExtendSelection with nil Selection should not create one")
	}
	if b.Cursor != (Position{0, 0}) {
		t.Fatalf("ExtendSelection should not move Cursor when Selection nil; got %+v", b.Cursor)
	}
}

func TestSetSelectionInstallsCopy(t *testing.T) {
	b := bufFromLines("abc")
	r := Range{Start: Position{0, 0}, End: Position{0, 2}}
	SetSelection(b, &r)
	if b.Selection == nil {
		t.Fatalf("Selection nil after SetSelection")
	}
	if b.Selection == &r {
		t.Fatalf("SetSelection should copy, not alias")
	}
	if *b.Selection != r {
		t.Fatalf("Selection = %+v, want %+v", *b.Selection, r)
	}
}

func TestSetSelectionNilClears(t *testing.T) {
	b := bufFromLines("abc")
	b.Selection = &Range{Start: Position{0, 0}, End: Position{0, 1}}
	SetSelection(b, nil)
	if b.Selection != nil {
		t.Fatalf("SetSelection(nil) should clear Selection")
	}
}

func TestCancelSelectionIfOverlapClearsOnInsertInside(t *testing.T) {
	b := bufFromLines("hello world")
	b.Selection = &Range{Start: Position{0, 0}, End: Position{0, 5}}
	// Insert in the middle of selection: clears.
	err := b.Apply(Edit{
		Kind:  EditKindInsert,
		Range: Range{Start: Position{0, 2}, End: Position{0, 2}},
		Text:  "X",
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if b.Selection != nil {
		t.Fatalf("Selection should be cleared by overlapping insert; got %+v", *b.Selection)
	}
}

func TestCancelSelectionIfOverlapLeavesNonOverlappingEditAlone(t *testing.T) {
	b := bufFromLines("hello world")
	sel := Range{Start: Position{0, 0}, End: Position{0, 4}}
	b.Selection = &sel
	// Delete entirely after selection: leaves it intact.
	err := b.Apply(Edit{
		Kind:  EditKindDelete,
		Range: Range{Start: Position{0, 6}, End: Position{0, 11}},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if b.Selection == nil {
		t.Fatalf("Selection should remain after non-overlapping edit")
	}
	if *b.Selection != sel {
		t.Fatalf("Selection mutated: %+v, want %+v", *b.Selection, sel)
	}
}

func TestCancelSelectionIfOverlapClearsOnDeleteCoveringSelection(t *testing.T) {
	b := bufFromLines("hello world")
	b.Selection = &Range{Start: Position{0, 2}, End: Position{0, 4}}
	err := b.Apply(Edit{
		Kind:  EditKindDelete,
		Range: Range{Start: Position{0, 0}, End: Position{0, 5}},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if b.Selection != nil {
		t.Fatalf("Selection should clear when delete covers it")
	}
}
