package types

import "fmt"

// Mode is a bitmask of editor/keymap modes. ModeNormal is the zero
// sentinel (not a bit flag); every other mode is a distinct bit so a
// Mode value can carry composite state (e.g. visual + operator-pending).
type Mode uint32

const (
	// ModeNormal is the default mode and the zero value of Mode. It is a
	// sentinel — not a bit flag — and represents "no modal state".
	ModeNormal Mode = 0

	// ModeInsert is entered when the user is typing free-form text into an
	// editable buffer (e.g. query editor, prompt input).
	//
	// Bit-value note: this is the second ConstSpec in the block, so iota == 1
	// here and ModeInsert == 2 (not 1). Every subsequent mode below is a
	// distinct higher bit (4, 8, 16, …). ModeNormal stays as the zero
	// sentinel.
	ModeInsert Mode = 1 << iota
	// ModeVisual is entered when the user starts a character-wise
	// selection over the current view's content.
	ModeVisual
	// ModeVisualLine is entered when the user starts a line-wise selection
	// (whole rows at a time).
	ModeVisualLine
	// ModeVisualBlock is entered when the user starts a block / column
	// selection (rectangular region).
	ModeVisualBlock
	// ModeOperatorPending is entered after an operator key (d, y, c, …)
	// while the keymap waits for a motion to complete the verb.
	ModeOperatorPending
	// ModeCommand is entered when the colon command line is active and
	// accepting an ex-style command.
	ModeCommand
	// ModeReplace is entered when keypresses overwrite existing characters
	// instead of inserting before the cursor.
	ModeReplace
)

// Has reports whether m carries the bits in other. As a special case,
// Has(ModeNormal) returns true only when m is exactly ModeNormal — since
// ModeNormal is the zero sentinel rather than a bit flag, the usual
// bitwise test would always succeed.
func (m Mode) Has(other Mode) bool {
	if other == ModeNormal {
		return m == ModeNormal
	}
	return m&other != 0
}

// Is reports whether m is exactly equal to other (no extra bits).
func (m Mode) Is(other Mode) bool {
	return m == other
}

// String returns the canonical short lowercase name of m. Composite or
// unknown values return a deterministic "mode(0x<hex>)" form.
func (m Mode) String() string {
	switch m {
	case ModeNormal:
		return "normal"
	case ModeInsert:
		return "insert"
	case ModeVisual:
		return "visual"
	case ModeVisualLine:
		return "visual-line"
	case ModeVisualBlock:
		return "visual-block"
	case ModeOperatorPending:
		return "operator-pending"
	case ModeCommand:
		return "command"
	case ModeReplace:
		return "replace"
	default:
		return fmt.Sprintf("mode(0x%x)", uint32(m))
	}
}
