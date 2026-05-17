package keys

import (
	"strings"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// Source identifies which layer produced a binding. The Matcher and the
// cheatsheet glyph renderer (dlp.10) read this field; the trie itself
// stores it per leaf.
type Source uint8

const (
	// ShippedDefault is a binding contributed by a controller's
	// AllDefaultBindings list (dlp.8c). Inserted FIRST during Build.
	ShippedDefault Source = iota
	// UserOverride is a binding from cfg.Keybindings that names an
	// existing Action via the `action:` shorthand. Overlays defaults.
	UserOverride
	// CustomCmd is a binding from cfg.Keybindings that runs a user-typed
	// shell `command:` (machinery shipped by E11; dlp.4 only records the
	// tag so the cheatsheet can paint the ★ glyph).
	CustomCmd
)

// Modifier is a bitmask of pressed keyboard modifiers.
//
// Modifiers are folded into the Key value itself (not stored on the trie
// edge) so two keystrokes with different modifier sets are distinct trie
// keys.
type Modifier uint8

const (
	ModCtrl Modifier = 1 << iota
	ModAlt
	ModShift
	ModMeta
)

// SpecialKey enumerates non-rune keys. KeyLeader / KeyLocalLeader are
// compile-time-only sentinels: SequenceFromShorthand emits them when a
// `<leader>` / `<localleader>` token is parsed, and the Build pipeline
// expands them into the configured rune BEFORE inserting into the trie.
// They MUST NOT appear on any trie node — the orphan-prefix tests assert
// this invariant.
type SpecialKey uint8

const (
	// KeyNone is the zero value and identifies a bare-rune Key (Code is
	// the rune; Special is unused).
	KeyNone SpecialKey = iota
	KeyEsc
	KeyEnter
	KeyTab
	KeyBs
	KeySpace
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyHome
	KeyEnd
	KeyPgUp
	KeyPgDn
	KeyIns
	KeyDel
	KeyF1
	KeyF2
	KeyF3
	KeyF4
	KeyF5
	KeyF6
	KeyF7
	KeyF8
	KeyF9
	KeyF10
	KeyF11
	KeyF12
	KeyLeader      // compile-time-only; expanded out before trie insert
	KeyLocalLeader // compile-time-only; expanded out before trie insert
)

// Key is one element of a chord Sequence.
//
// For bare-rune keys: Code is the rune, Special is KeyNone.
// For specials (Esc, F1, …): Code is 0, Special names the key.
// Mod carries any held modifiers (Ctrl/Alt/Shift/Meta) for either form.
type Key struct {
	Code    rune
	Special SpecialKey
	Mod     Modifier
}

// IsLeaderPlaceholder reports whether k is a compile-time `<leader>` or
// `<localleader>` sentinel. Such keys must be replaced before trie
// insert.
func (k Key) IsLeaderPlaceholder() bool {
	return k.Special == KeyLeader || k.Special == KeyLocalLeader
}

// String returns a stable human-readable label for k (mirrors the input
// shorthand syntax). Used by cheatsheet rendering, warnings, and tests.
func (k Key) String() string {
	var b strings.Builder
	bracketed := k.Special != KeyNone || k.Mod != 0
	if bracketed {
		b.WriteByte('<')
	}
	if k.Mod&ModCtrl != 0 {
		b.WriteString("c-")
	}
	if k.Mod&ModAlt != 0 {
		b.WriteString("a-")
	}
	if k.Mod&ModShift != 0 {
		b.WriteString("s-")
	}
	if k.Mod&ModMeta != 0 {
		b.WriteString("m-")
	}
	switch k.Special {
	case KeyNone:
		if k.Code != 0 {
			b.WriteRune(k.Code)
		}
	case KeyEsc:
		b.WriteString("esc")
	case KeyEnter:
		b.WriteString("cr")
	case KeyTab:
		b.WriteString("tab")
	case KeyBs:
		b.WriteString("bs")
	case KeySpace:
		b.WriteString("space")
	case KeyUp:
		b.WriteString("up")
	case KeyDown:
		b.WriteString("down")
	case KeyLeft:
		b.WriteString("left")
	case KeyRight:
		b.WriteString("right")
	case KeyHome:
		b.WriteString("home")
	case KeyEnd:
		b.WriteString("end")
	case KeyPgUp:
		b.WriteString("pgup")
	case KeyPgDn:
		b.WriteString("pgdn")
	case KeyIns:
		b.WriteString("ins")
	case KeyDel:
		b.WriteString("del")
	case KeyF1:
		b.WriteString("f1")
	case KeyF2:
		b.WriteString("f2")
	case KeyF3:
		b.WriteString("f3")
	case KeyF4:
		b.WriteString("f4")
	case KeyF5:
		b.WriteString("f5")
	case KeyF6:
		b.WriteString("f6")
	case KeyF7:
		b.WriteString("f7")
	case KeyF8:
		b.WriteString("f8")
	case KeyF9:
		b.WriteString("f9")
	case KeyF10:
		b.WriteString("f10")
	case KeyF11:
		b.WriteString("f11")
	case KeyF12:
		b.WriteString("f12")
	case KeyLeader:
		b.WriteString("leader")
	case KeyLocalLeader:
		b.WriteString("localleader")
	}
	if bracketed {
		b.WriteByte('>')
	}
	return b.String()
}

// SequenceString joins seq with no separator (matching shorthand syntax).
func SequenceString(seq []Key) string {
	var b strings.Builder
	for _, k := range seq {
		b.WriteString(k.String())
	}
	return b.String()
}

// ChordBinding is one shipped or user-defined chord-to-action mapping.
//
// Sequence carries the parsed Key tokens. KeyLeader / KeyLocalLeader
// MAY appear in a freshly-constructed binding (e.g. from
// SequenceFromShorthand) but MUST be expanded out before Build inserts
// the binding into the trie.
//
// Mode is a bitmask. Per the design every bit corresponds to one
// applicable mode; Build expands a multi-bit Mode into one trie entry
// per bit.
//
// Scope identifies the owning ContextKey (or the GLOBAL pseudo-context).
// The literal scope tokens `"all"` (non-popup contexts) and `"global"`
// are resolved by Build into concrete ContextKeys.
//
// ActionID is the stable identifier resolved against
// commands.Registry at Build time. The trie node carries the resolved
// *commands.Command directly; ChordBinding has NO Handler field per
// D17 (single source of truth lives in the registry).
//
// Description / Tag / ShowInBar / OpensMenu are cosmetic metadata
// consumed by the cheatsheet, options bar, and which-key popup.
//
// Source is set by the caller before Build: callers passing controller
// defaults set ShippedDefault; the cfg-to-binding lifter (in
// sequence_parser.go) sets UserOverride or CustomCmd as appropriate.
//
// Origin is the human-readable provenance (file:line, or controller
// name) shown in warnings and cheatsheet tooltips.
type ChordBinding struct {
	Sequence    []Key
	Mode        types.Mode
	Scope       types.ContextKey
	ActionID    string
	Description string
	Tag         string
	ShowInBar   bool
	OpensMenu   bool
	Source      Source
	Origin      string
}
