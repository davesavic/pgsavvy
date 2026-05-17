package types

// Mode is a bitmask of editor/keymap modes. Future iota slots (visual,
// insert, chord-pending, etc.) will be added by epic E5; only ModeNormal
// is meaningful today.
type Mode uint32

const (
	// ModeNormal is the default mode and the zero value of Mode.
	ModeNormal Mode = 0
)
