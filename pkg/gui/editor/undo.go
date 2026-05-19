package editor

// Node is one node in the branching UndoTree. Apply prepends new
// children, so children[0] always traces the most recent branch —
// Redo walks children[0]. Older sibling branches survive after an
// edit-following-undo and are discoverable via Siblings(node).
type Node struct {
	edit     Edit
	parent   *Node
	children []*Node
}

// Edit returns the Edit recorded at n. The root sentinel returns
// the zero Edit (Kind == 0).
func (n *Node) Edit() Edit { return n.edit }

// UndoTree is the per-Buffer branching diff history. It is capped
// at cap nodes (vim undolevels=1000). When the cap is exceeded,
// evictOnce drops the oldest top-level branch — for a linear chain
// this rebases the root sentinel forward, forgetting the topmost
// edit and preserving the most recent cap edits.
//
// Concurrency: UndoTree is NOT safe for concurrent use. Buffer holds
// the only reference and serialises access through Buffer.mu.
type UndoTree struct {
	root    *Node
	current *Node
	count   int
	cap     int
}

// NewUndoTree returns an empty tree (single root sentinel) capped
// at maxNodes. A non-positive maxNodes defaults to 1000 to match
// vim's undolevels.
func NewUndoTree(maxNodes int) *UndoTree {
	if maxNodes <= 0 {
		maxNodes = undoCap
	}
	root := &Node{}
	return &UndoTree{
		root:    root,
		current: root,
		cap:     maxNodes,
	}
}

// Apply records e as a new child of the current node and advances
// the cursor to it. New nodes are prepended; pre-existing children
// (from undo+new-apply branching) survive as later siblings.
//
// When the resulting count exceeds cap, evictOnce drops the oldest
// top-level branch — for a linear chain this rebases the root
// sentinel forward, dropping the topmost edit.
func (t *UndoTree) Apply(e Edit) {
	n := &Node{edit: e, parent: t.current}
	t.current.children = append([]*Node{n}, t.current.children...)
	t.current = n
	t.count++
	for t.count > t.cap {
		if !t.evictOnce() {
			break
		}
	}
}

// Undo walks the cursor to current.parent and returns the cursor
// node's reverse Edit (the inverse mutation the caller should
// apply to the Buffer). Returns (zero Edit, false) when already at
// the root sentinel.
func (t *UndoTree) Undo() (Edit, bool) {
	if t.current == nil || t.current == t.root {
		return Edit{}, false
	}
	rev := t.current.edit.Reverse()
	t.current = t.current.parent
	return rev, true
}

// Redo walks the cursor to children[0] of current (the most recent
// branch) and returns the forward Edit. Returns (zero Edit, false)
// when current has no children.
func (t *UndoTree) Redo() (Edit, bool) {
	if t.current == nil || len(t.current.children) == 0 {
		return Edit{}, false
	}
	nxt := t.current.children[0]
	t.current = nxt
	return nxt.edit, true
}

// Siblings returns the sibling nodes of n in tree order, excluding
// n itself. Returns nil when n is nil or n has no parent. Test-only
// API: there are no g-/g+ UI bindings in MVP; Siblings is kept
// exported so this package and successor epics can assert
// branching invariants.
func (t *UndoTree) Siblings(n *Node) []*Node {
	if n == nil || n.parent == nil {
		return nil
	}
	out := make([]*Node, 0, len(n.parent.children)-1)
	for _, c := range n.parent.children {
		if c != n {
			out = append(out, c)
		}
	}
	return out
}

// NodeCount returns the number of non-sentinel nodes currently in
// the tree.
func (t *UndoTree) NodeCount() int { return t.count }

// Current returns the cursor node. The root sentinel is returned
// when the cursor is at the start of history.
func (t *UndoTree) Current() *Node { return t.current }

// Cap returns the configured node cap. Useful for tests that want
// to assert eviction without hard-coding 1000.
func (t *UndoTree) Cap() int { return t.cap }

// evictOnce drops one node (or one branch) to bring count under
// cap. Returns false when no eviction is possible (cursor at root
// with no other branches), letting Apply break out of its loop.
//
// Strategy: when root has multiple direct children, drop the oldest
// branch (children[len-1] — since Apply prepends, the slice is
// ordered newest-first). The current node is by construction on the
// children[0] branch, so dropping the tail is always safe. When
// root has a single child, "rebase" by promoting that child to be
// the new root sentinel (its Edit is discarded) — equivalent to
// forgetting the topmost edit in a linear history.
func (t *UndoTree) evictOnce() bool {
	if t.root == nil || len(t.root.children) == 0 {
		return false
	}
	if len(t.root.children) > 1 {
		last := len(t.root.children) - 1
		old := t.root.children[last]
		t.root.children = t.root.children[:last]
		t.count -= subtreeSize(old)
		return true
	}
	only := t.root.children[0]
	if only == t.current {
		return false
	}
	only.parent = nil
	only.edit = Edit{}
	t.root = only
	t.count--
	return true
}

func subtreeSize(n *Node) int {
	if n == nil {
		return 0
	}
	total := 1
	for _, c := range n.children {
		total += subtreeSize(c)
	}
	return total
}
