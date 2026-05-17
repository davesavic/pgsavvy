package keys

import (
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// ChordBinding mirrors types.ChordBinding. dlp.8a moved the canonical
// definition into pkg/gui/types so the IBaseContext interface can
// reference it without a package import cycle; this alias keeps the
// keys-package call sites (and existing tests) compiling unchanged.
type ChordBinding = types.ChordBinding

// Key is the chord-keystroke struct. Aliased to types.ChordKey (the
// canonical definition) — the in-package alias keeps the existing
// keys-package code (Matcher, Builder, sequence_parser, etc.) compiling
// with bare identifiers (Key, Modifier, ModCtrl, …).
type Key = types.ChordKey

// Modifier is the chord-modifier bitmask, aliased from types.ChordModifier.
// Distinct from types.Modifier (the gocui modifier type) — the chord
// modifier is internal to the trie and matcher.
type Modifier = types.ChordModifier

// SpecialKey enumerates non-rune chord keys; aliased from types.SpecialKey.
type SpecialKey = types.SpecialKey

// Source identifies which layer produced a binding; aliased from
// types.Source.
type Source = types.Source

// Chord modifier constants (aliased from types).
const (
	ModCtrl  = types.ChordModCtrl
	ModAlt   = types.ChordModAlt
	ModShift = types.ChordModShift
	ModMeta  = types.ChordModMeta
)

// Special-key constants (aliased from types). Listed individually so
// callers can keep using the unqualified `keys.KeyEsc` etc. names.
const (
	KeyNone        = types.KeyNone
	KeyEsc         = types.KeyEsc
	KeyEnter       = types.KeyEnter
	KeyTab         = types.KeyTab
	KeyBs          = types.KeyBs
	KeySpace       = types.KeySpace
	KeyUp          = types.KeyUp
	KeyDown        = types.KeyDown
	KeyLeft        = types.KeyLeft
	KeyRight       = types.KeyRight
	KeyHome        = types.KeyHome
	KeyEnd         = types.KeyEnd
	KeyPgUp        = types.KeyPgUp
	KeyPgDn        = types.KeyPgDn
	KeyIns         = types.KeyIns
	KeyDel         = types.KeyDel
	KeyF1          = types.KeyF1
	KeyF2          = types.KeyF2
	KeyF3          = types.KeyF3
	KeyF4          = types.KeyF4
	KeyF5          = types.KeyF5
	KeyF6          = types.KeyF6
	KeyF7          = types.KeyF7
	KeyF8          = types.KeyF8
	KeyF9          = types.KeyF9
	KeyF10         = types.KeyF10
	KeyF11         = types.KeyF11
	KeyF12         = types.KeyF12
	KeyLeader      = types.KeyLeader
	KeyLocalLeader = types.KeyLocalLeader
)

// Source constants (aliased from types).
const (
	ShippedDefault = types.ShippedDefault
	UserOverride   = types.UserOverride
	CustomCmd      = types.CustomCmd
)

// SequenceString joins seq with no separator (matching shorthand
// syntax). Thin wrapper over types.SequenceString preserved for
// in-package callers.
func SequenceString(seq []Key) string {
	return types.SequenceString(seq)
}
