package editor

// EditKind enumerates the three Edit operations. Insert/Delete are
// each other's inverses; Replace is an atomic Delete+Insert with a
// single validation gate and a single undo step. The zero value (0)
// is intentionally invalid so Edit{}.Kind catches mis-constructed
// edits in tests.
type EditKind int

const (
	EditKindInsert EditKind = iota + 1
	EditKindDelete
	EditKindReplace
)

// Edit is the diff Buffer.Apply commits to its UndoTree. Each Edit
// carries its own reverse (populated by Buffer.Apply, captured
// against the pre-mutate snapshot). UndoTree.Undo replays e.Reverse()
// without re-recording so the tree's cursor stays authoritative over
// branching history.
//
// Construct with Kind + Range + Text; Apply fills in the unexported
// reverse field. Reverse() returns the zero Edit when called on an
// Edit that has not been recorded yet — callers in test/debug code
// should treat EditKind == 0 as "not invertible without buffer
// context".
type Edit struct {
	Kind    EditKind
	Range   Range
	Text    string
	reverse *Edit
}

// Reverse returns the inverse Edit captured by Buffer.Apply. The
// receiver's reverse is nil only when the Edit has not yet been
// recorded into a Buffer; in that case Reverse returns the zero
// Edit (Kind == 0).
func (e Edit) Reverse() Edit {
	if e.reverse == nil {
		return Edit{}
	}
	return *e.reverse
}
