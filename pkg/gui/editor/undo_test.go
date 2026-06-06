package editor

import (
	"fmt"
	"testing"
)

func mkInsert(line, col int, text string) Edit {
	return Edit{
		Kind:  EditKindInsert,
		Range: Range{Start: Position{line, col}, End: Position{line, col}},
		Text:  text,
	}
}

func TestUndoTreeUndoOnEmptyReturnsFalse(t *testing.T) {
	tr := NewUndoTree(10)
	rev, ok := tr.Undo()
	if ok {
		t.Fatalf("Undo on empty returned ok=true with %+v", rev)
	}
}

func TestUndoTreeRedoOnLeafReturnsFalse(t *testing.T) {
	tr := NewUndoTree(10)
	tr.Apply(Edit{Kind: EditKindInsert})
	_, ok := tr.Redo()
	if ok {
		t.Fatalf("Redo at leaf returned ok=true")
	}
}

func TestUndoTreeApplyUndoRedo(t *testing.T) {
	b := bufFromLines("a")
	mustApply(t, b, mkInsert(0, 1, "B"))
	if got := b.String(); got != "aB" {
		t.Fatalf("after Apply: %q", got)
	}
	if err := b.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if got := b.String(); got != "a" {
		t.Fatalf("after Undo: %q", got)
	}
	if err := b.Redo(); err != nil {
		t.Fatalf("Redo: %v", err)
	}
	if got := b.String(); got != "aB" {
		t.Fatalf("after Redo: %q", got)
	}
}

func TestUndoTreeBranchingPreservesOldBranch(t *testing.T) {
	// Apply A, Apply B, Undo (back to A), Apply C.
	// After Apply C, Siblings(C-node) should contain the B-node so
	// the older branch is discoverable.
	b := bufFromLines("x")
	mustApply(t, b, mkInsert(0, 1, "A"))
	mustApply(t, b, mkInsert(0, 2, "B"))
	if err := b.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	mustApply(t, b, mkInsert(0, 2, "C"))
	curNode := b.History.Current()
	if curNode.Edit().Text != "C" {
		t.Fatalf("current node Text = %q, want %q", curNode.Edit().Text, "C")
	}
	sibs := b.History.Siblings(curNode)
	if len(sibs) != 1 {
		t.Fatalf("Siblings(C-node) len = %d, want 1", len(sibs))
	}
	if sibs[0].Edit().Text != "B" {
		t.Fatalf("Sibling.Text = %q, want %q", sibs[0].Edit().Text, "B")
	}
}

func TestUndoTreeRedoAfterBranchPicksNewBranch(t *testing.T) {
	// After branching, Redo from the branch point walks the NEW
	// branch (children[0]), not the older sibling.
	b := bufFromLines("x")
	mustApply(t, b, mkInsert(0, 1, "A"))
	mustApply(t, b, mkInsert(0, 2, "B"))
	if err := b.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	mustApply(t, b, mkInsert(0, 2, "C"))
	if err := b.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	// Now at A. Redo should walk to C (newest), not B.
	if err := b.Redo(); err != nil {
		t.Fatalf("Redo: %v", err)
	}
	if got := b.History.Current().Edit().Text; got != "C" {
		t.Fatalf("Redo picked %q, want %q (newest branch first)", got, "C")
	}
}

func TestUndoTreeCapEvictsOldestBranchOnOverflow(t *testing.T) {
	const capN = 5
	tr := NewUndoTree(capN)
	for i := range capN + 1 {
		tr.Apply(Edit{Kind: EditKindInsert, Text: fmt.Sprintf("e%d", i)})
	}
	if got, want := tr.NodeCount(), capN; got != want {
		t.Fatalf("NodeCount after %d applies = %d, want %d", capN+1, got, want)
	}
	// Undo capN times succeeds.
	for i := range capN {
		if _, ok := tr.Undo(); !ok {
			t.Fatalf("Undo iteration %d returned ok=false", i)
		}
	}
	// The (capN+1)th Undo must fail — only capN edits remain.
	if _, ok := tr.Undo(); ok {
		t.Fatalf("Undo past cap returned ok=true; cap should bound history")
	}
}

func TestUndoTreeCap1000(t *testing.T) {
	tr := NewUndoTree(1000)
	for range 1001 {
		tr.Apply(Edit{Kind: EditKindInsert, Text: "x"})
	}
	if got := tr.NodeCount(); got != 1000 {
		t.Fatalf("NodeCount after 1001 applies = %d, want 1000", got)
	}
}

func TestUndoTreeBufferRemainsOperationalAfterEviction(t *testing.T) {
	// Buffer should be perfectly usable after the cap kicks in. We
	// don't assert the deepest pre-eviction edit is still reachable
	// (it isn't — that's the whole point of the cap), but the
	// recent history must work.
	b := bufFromLines("")
	// Force an UndoTree with a small cap by initialising History directly.
	b.History = NewUndoTree(3)
	for i := range 5 {
		// Insert at start of line 0 so each iteration is a valid edit
		// regardless of accumulated length.
		mustApply(t, b, mkInsert(0, 0, fmt.Sprintf("%d", i)))
	}
	// Buffer string should contain the latest 5 inserts irrespective
	// of cap (mutations always succeeded). Cap only affects history.
	if got := b.String(); got == "" {
		t.Fatalf("post-eviction Buffer empty, expected content")
	}
	// Undo should run cap times (3), no more.
	for i := range 3 {
		if err := b.Undo(); err != nil {
			t.Fatalf("Undo %d: %v", i, err)
		}
	}
	// Further Undo is a no-op (root reached).
	prev := b.String()
	if err := b.Undo(); err != nil {
		t.Fatalf("Undo at root: %v", err)
	}
	if b.String() != prev {
		t.Fatalf("Undo past cap mutated buffer: %q → %q", prev, b.String())
	}
}

func TestUndoTreeSixDepthMixedSequencePreservesBranches(t *testing.T) {
	// Build a 6-edge mixed Apply/Undo/Redo sequence and verify every
	// historical Edit is reachable from the root via Siblings/children
	// walk.
	b := bufFromLines("0")
	seq := []struct {
		op   string
		text string
	}{
		{"apply", "A"},
		{"apply", "B"},
		{"undo", ""},
		{"apply", "C"},
		{"undo", ""},
		{"apply", "D"},
	}
	for _, step := range seq {
		switch step.op {
		case "apply":
			mustApply(t, b, mkInsert(0, len(b.Lines[0].Runes), step.text))
		case "undo":
			if err := b.Undo(); err != nil {
				t.Fatalf("Undo: %v", err)
			}
		}
	}
	// Every applied text must be reachable somewhere in the tree.
	want := map[string]bool{"A": false, "B": false, "C": false, "D": false}
	walk(b.History.Current(), want, "child")
	// Walk from root down too.
	walkAllNodes(rootOf(b.History), want)
	for k, found := range want {
		if !found {
			t.Errorf("Edit %q not reachable in tree after mixed sequence", k)
		}
	}
}

func walk(n *Node, want map[string]bool, _ string) {
	if n == nil {
		return
	}
	if _, ok := want[n.Edit().Text]; ok {
		want[n.Edit().Text] = true
	}
	for _, c := range n.children {
		walk(c, want, "child")
	}
}

func walkAllNodes(n *Node, want map[string]bool) {
	if n == nil {
		return
	}
	if _, ok := want[n.Edit().Text]; ok {
		want[n.Edit().Text] = true
	}
	for _, c := range n.children {
		walkAllNodes(c, want)
	}
}

func rootOf(t *UndoTree) *Node {
	if t == nil {
		return nil
	}
	return t.root
}

func TestUndoTreeNewWithNonPositiveCapDefaults(t *testing.T) {
	if NewUndoTree(0).Cap() != undoCap {
		t.Fatalf("zero cap should default to %d", undoCap)
	}
	if NewUndoTree(-7).Cap() != undoCap {
		t.Fatalf("negative cap should default to %d", undoCap)
	}
}
