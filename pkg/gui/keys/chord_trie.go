package keys

import (
	"fmt"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
)

// trieNode is the unexported node of a ChordTrie. After Builder.Build
// completes the node graph is treated as immutable; Matcher / Walk /
// Lookup read it without locking.
type trieNode struct {
	children  map[Key]*trieNode
	action    *commands.Command
	source    Source
	origin    string
	showInBar bool
	opensMenu bool
}

// LookupResult is what Lookup returns. It is a value type so callers
// cannot mutate the trie through the returned reference.
//
// Action is the resolved Command pointer (nil for interior nodes). For a
// `<nop>` leaf, Action == commands.NopCommand.
//
// Source / Origin are populated for leaves (interior nodes carry the
// zero values). IsLeaf == (Action != nil); HasChildren reports whether
// the matched node has continuations.
//
// Found is true when the supplied sequence corresponds to a node in the
// trie (leaf OR interior). Lookup of the empty sequence returns
// Found=true with the root node.
type LookupResult struct {
	Action      *commands.Command
	Source      Source
	Origin      string
	IsLeaf      bool
	HasChildren bool
	Found       bool

	// ShowInBar / OpensMenu are leaf-only cosmetic flags lifted from the
	// originating ChordBinding. Interior-node results carry the zero
	// value. Consumed by the options-bar collector (dlp.12) and reserved
	// for the menu-routing flow.
	ShowInBar bool
	OpensMenu bool
}

// ChordTrie holds the chord prefix tree for a single (Mode, Scope)
// pair. It is built once by TrieBuilder and never mutated again, so
// concurrent readers (Matcher + cheatsheet walker) require no
// synchronisation.
type ChordTrie struct {
	root *trieNode
}

// Lookup walks seq from the root. Per AC dlp.4:
//   - Lookup([]) returns Found=true on the root with IsLeaf=false and
//     HasChildren reflecting whether the trie has any bindings.
//   - Lookup of an unknown prefix returns Found=false (all other fields
//     zero).
//   - Lookup of an interior prefix returns Found=true with IsLeaf=false.
//   - Lookup of a leaf returns Found=true with IsLeaf=true and the
//     resolved Action / Source / Origin populated.
func (t *ChordTrie) Lookup(seq []Key) LookupResult {
	node := t.root
	for _, k := range seq {
		next, ok := node.children[k]
		if !ok {
			return LookupResult{}
		}
		node = next
	}
	res := LookupResult{
		Action:      node.action,
		Source:      node.source,
		Origin:      node.origin,
		IsLeaf:      node.action != nil,
		HasChildren: len(node.children) > 0,
		Found:       true,
		ShowInBar:   node.showInBar,
		OpensMenu:   node.opensMenu,
	}
	return res
}

// RootKeys returns the set of top-level (root child) Keys in
// deterministic order. The orchestrator (dlp.8c) uses this to install
// one per-key SetKeybinding shim per top-level chord prefix on
// non-editable views. Empty trie → empty slice.
func (t *ChordTrie) RootKeys() []Key {
	if t == nil || t.root == nil {
		return nil
	}
	return sortedKeys(t.root.children)
}

// ReachableKeys returns every distinct Key that appears at ANY depth in
// the trie, in deterministic order. The orchestrator (dbsavvy-tro.7)
// uses this to install one SetKeybinding shim per key reachable in any
// chord — not just root keys. Without this, chord-trailing keys (e.g.
// the `q` in `<leader>q`) are never delivered to the Matcher because
// gocui silently swallows keystrokes that have no registered binding.
// Empty trie → empty slice.
func (t *ChordTrie) ReachableKeys() []Key {
	if t == nil || t.root == nil {
		return nil
	}
	set := map[Key]struct{}{}
	collectReachableKeys(t.root, set)
	out := make([]Key, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	// Reuse the same total order as sortedKeys for stable test output.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].String() < out[j-1].String(); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func collectReachableKeys(node *trieNode, set map[Key]struct{}) {
	for k, child := range node.children {
		set[k] = struct{}{}
		collectReachableKeys(child, set)
	}
}

// ChildrenAt returns the immediate children of the node at prefix,
// sorted by Key.String() for determinism. The popup renderer
// (WhichKeyContext) consumes the result to draw one row per child.
//
// Returns (nil, false) when prefix does not exist in the trie. Returns
// (empty, true) when prefix resolves to a leaf with no continuations.
// For leaf children, Label is the resolved Command.Description (the
// <nop> command carries "(unbound)"). For interior children, Label is
// empty — the caller decides how to format an interior continuation.
func (t *ChordTrie) ChildrenAt(prefix []Key) ([]ChildRow, bool) {
	node := t.root
	for _, k := range prefix {
		next, ok := node.children[k]
		if !ok {
			return nil, false
		}
		node = next
	}
	keys := sortedKeys(node.children)
	out := make([]ChildRow, 0, len(keys))
	for _, k := range keys {
		child := node.children[k]
		row := ChildRow{
			Key:    k,
			IsLeaf: child.action != nil,
			Source: child.source,
		}
		if child.action != nil {
			row.Label = child.action.Description
		}
		out = append(out, row)
	}
	return out, true
}

// Walk visits every LEAF in t (interior nodes are skipped) in
// deterministic DFS order. fn is called with the resolved Sequence (a
// fresh slice for each leaf) plus a LookupResult describing the leaf.
//
// Used by the cheatsheet generator (dlp.10) and the completeness test
// (dlp.11). Walk on the empty trie is a no-op.
func (t *ChordTrie) Walk(fn func(seq []Key, leaf LookupResult)) {
	if t == nil || t.root == nil {
		return
	}
	walkNode(t.root, nil, fn)
}

func walkNode(node *trieNode, prefix []Key, fn func([]Key, LookupResult)) {
	if node.action != nil {
		seq := make([]Key, len(prefix))
		copy(seq, prefix)
		fn(seq, LookupResult{
			Action:      node.action,
			Source:      node.source,
			Origin:      node.origin,
			IsLeaf:      true,
			HasChildren: len(node.children) > 0,
			Found:       true,
			ShowInBar:   node.showInBar,
			OpensMenu:   node.opensMenu,
		})
	}
	// Deterministic ordering: sort children by stringified Key so test
	// output is stable. The trie is built infrequently so this is fine.
	keys := sortedKeys(node.children)
	for _, k := range keys {
		walkNode(node.children[k], append(prefix, k), fn)
	}
}

func sortedKeys(m map[Key]*trieNode) []Key {
	out := make([]Key, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Sort by String(): cheap, total order, stable for testing.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].String() < out[j-1].String(); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// TrieBuilder accumulates ChordBindings into one ChordTrie. The two
// insertion entry points enforce the layering rule from D8:
// InsertDefault inserts first (Source=ShippedDefault); InsertUser then
// overlays (Source=UserOverride / CustomCmd, last-wins on conflict).
//
// Build is idempotent — call it once. Subsequent inserts after Build are
// safe but pointless (the snapshot has already been returned).
type TrieBuilder struct {
	root     *trieNode
	warnings []Warning
}

// NewTrieBuilder constructs an empty builder.
func NewTrieBuilder() *TrieBuilder {
	return &TrieBuilder{root: &trieNode{children: map[Key]*trieNode{}}}
}

// InsertDefault inserts cb as a shipped default. If a leaf already
// exists at the same Sequence with a DIFFERENT action, a Warning of
// Code="collision" is emitted and the LATER write wins (matches the
// vim/lazygit convention).
//
// cb.Source is overwritten to ShippedDefault to enforce the layering
// rule. cb.Sequence MUST NOT contain KeyLeader / KeyLocalLeader (the
// caller is responsible for expansion).
//
// cmd MUST be the *commands.Command this binding resolves to (the
// service has already looked it up against the Registry). A nil cmd
// indicates an orphan-action skip and InsertDefault returns early — it
// is the service's responsibility to emit the orphan_action warning.
func (b *TrieBuilder) InsertDefault(cb *ChordBinding, cmd *commands.Command) {
	if cmd == nil {
		return
	}
	cb.Source = ShippedDefault
	b.insert(cb, cmd)
}

// InsertUser inserts cb on top of any existing default. Source is set
// from cb (UserOverride for `action:` shorthand, CustomCmd for
// `command:` shorthand) — the caller MUST populate cb.Source before
// calling.
//
// User inserts overwrite the leaf unconditionally; no collision warning
// is emitted (overlaying defaults is the whole point). User-vs-user
// collisions (two cfg entries on the same Sequence) DO emit a collision
// warning — the second cfg entry wins, mirroring InsertDefault.
func (b *TrieBuilder) InsertUser(cb *ChordBinding, cmd *commands.Command) {
	if cmd == nil {
		return
	}
	b.insertUser(cb, cmd)
}

// RemoveLeafByAction deletes every leaf whose resolved Command.ID equals
// actionID from this builder's node graph, pruning now-childless interior
// nodes left behind. It is the inverse of insert and is ActionID-keyed so
// a motion remap can FREE the shipped-default key (e.g. after j→n, the
// j / dj leaves must go inert — R3). The trie is otherwise add-only;
// this is the sole removal path.
//
// Returns true if at least one leaf was removed. Safe to call before
// Build; no-op on an empty builder.
func (b *TrieBuilder) RemoveLeafByAction(actionID string) bool {
	if actionID == "" || b.root == nil {
		return false
	}
	removed := removeLeafByAction(b.root, actionID)
	return removed
}

// removeLeafByAction recursively clears any leaf action matching actionID
// and prunes children that become empty (no action, no children).
// Returns true if anything was removed in this subtree.
func removeLeafByAction(node *trieNode, actionID string) bool {
	removed := false
	for k, child := range node.children {
		if removeLeafByAction(child, actionID) {
			removed = true
		}
		if child.action != nil && child.action.ID == actionID {
			child.action = nil
			child.source = 0
			child.origin = ""
			child.showInBar = false
			child.opensMenu = false
			removed = true
		}
		if child.action == nil && len(child.children) == 0 {
			delete(node.children, k)
		}
	}
	return removed
}

// Build finalises the trie. It walks the root looking for ambiguous
// prefixes (an interior node that ALSO carries a leaf action) and emits
// one Warning per finding. The returned ChordTrie and warning slice are
// independent of the Builder — further inserts are not propagated.
func (b *TrieBuilder) Build() (*ChordTrie, []Warning) {
	b.detectAmbiguousPrefixes(b.root, nil)
	warns := b.warnings
	b.warnings = nil
	return &ChordTrie{root: b.root}, warns
}

func (b *TrieBuilder) insert(cb *ChordBinding, cmd *commands.Command) {
	if len(cb.Sequence) == 0 {
		return
	}
	node := b.root
	for _, k := range cb.Sequence {
		if k.IsLeaderPlaceholder() {
			b.warnings = append(b.warnings, Warning{
				Level:   WarnLevel,
				Code:    "unexpanded_leader",
				Message: fmt.Sprintf("binding %q contains an unexpanded %s placeholder", SequenceString(cb.Sequence), k.String()),
				Origin:  cb.Origin,
			})
			return
		}
		child, ok := node.children[k]
		if !ok {
			child = &trieNode{children: map[Key]*trieNode{}}
			node.children[k] = child
		}
		node = child
	}
	// Leaf assignment. Collision only fires when the existing action
	// differs from the incoming one; identical inserts (same cmd) are
	// idempotent and silent.
	if node.action != nil && node.action != cmd {
		b.warnings = append(b.warnings, Warning{
			Level: WarnLevel,
			Code:  "collision",
			Message: fmt.Sprintf(
				"binding %q collides at (mode=0x%x, scope=%s): %q overwrites %q",
				SequenceString(cb.Sequence), uint32(cb.Mode), cb.Scope, cmd.ID, node.action.ID,
			),
			Origin: cb.Origin,
		})
	}
	node.action = cmd
	node.source = cb.Source
	node.origin = cb.Origin
	node.showInBar = cb.ShowInBar
	node.opensMenu = cb.OpensMenu
}

// insertUser is the user-tier path. It runs the same collision-detection
// rules as insert but only when the existing leaf is itself a user
// entry (UserOverride / CustomCmd) — overwriting a default with a user
// binding is the intended overlay, NOT a collision.
func (b *TrieBuilder) insertUser(cb *ChordBinding, cmd *commands.Command) {
	if len(cb.Sequence) == 0 {
		return
	}
	node := b.root
	for _, k := range cb.Sequence {
		if k.IsLeaderPlaceholder() {
			b.warnings = append(b.warnings, Warning{
				Level:   WarnLevel,
				Code:    "unexpanded_leader",
				Message: fmt.Sprintf("binding %q contains an unexpanded %s placeholder", SequenceString(cb.Sequence), k.String()),
				Origin:  cb.Origin,
			})
			return
		}
		child, ok := node.children[k]
		if !ok {
			child = &trieNode{children: map[Key]*trieNode{}}
			node.children[k] = child
		}
		node = child
	}
	if node.action != nil && node.action != cmd && (node.source == UserOverride || node.source == CustomCmd) {
		b.warnings = append(b.warnings, Warning{
			Level: WarnLevel,
			Code:  "collision",
			Message: fmt.Sprintf(
				"binding %q collides at (mode=0x%x, scope=%s): %q overwrites %q",
				SequenceString(cb.Sequence), uint32(cb.Mode), cb.Scope, cmd.ID, node.action.ID,
			),
			Origin: cb.Origin,
		})
	}
	node.action = cmd
	node.source = cb.Source
	node.origin = cb.Origin
	node.showInBar = cb.ShowInBar
	node.opensMenu = cb.OpensMenu
}

func (b *TrieBuilder) detectAmbiguousPrefixes(node *trieNode, prefix []Key) {
	if node.action != nil && len(node.children) > 0 && node.action != commands.NopCommand {
		// Collect descendant leaf origins for an actionable warning.
		var descOrigins []string
		collectLeafOrigins(node, &descOrigins, 4)
		b.warnings = append(b.warnings, Warning{
			Level: WarnLevel,
			Code:  "ambiguous_prefix",
			Message: fmt.Sprintf(
				"sequence %q is a leaf AND a prefix of longer sequences (descendants: %v); vim timeoutlen rule applies",
				SequenceString(prefix), descOrigins,
			),
			Origin: node.origin,
		})
	}
	keys := sortedKeys(node.children)
	for _, k := range keys {
		detectChildPrefix := append(prefix, k)
		b.detectAmbiguousPrefixes(node.children[k], detectChildPrefix)
	}
}

func collectLeafOrigins(node *trieNode, out *[]string, cap int) {
	if len(*out) >= cap {
		return
	}
	keys := sortedKeys(node.children)
	for _, k := range keys {
		child := node.children[k]
		if child.action != nil {
			if len(*out) >= cap {
				return
			}
			*out = append(*out, child.origin)
		}
		collectLeafOrigins(child, out, cap)
	}
}
