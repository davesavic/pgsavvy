package types

import (
	"strings"
)

// Source identifies which layer produced a binding. The Matcher and the
// cheatsheet glyph renderer read this field; the trie itself
// stores it per leaf.
type Source uint8

const (
	// ShippedDefault is a binding contributed by a controller's
	// AllDefaultBindings list. Inserted FIRST during Build.
	ShippedDefault Source = iota
	// UserOverride is a binding from cfg.Keybindings that names an
	// existing Action via the `action:` shorthand. Overlays defaults.
	UserOverride
	// CustomCmd is a binding from cfg.Keybindings that runs a user-typed
	// shell `command:` (machinery shipped by E11; only the tag is
	// recorded here so the cheatsheet can paint the ★ glyph).
	CustomCmd
)

// ChordModifier is a bitmask of pressed keyboard modifiers for chord
// bindings.
//
// Modifiers are folded into the ChordKey value itself (not stored on the
// trie edge) so two keystrokes with different modifier sets are distinct
// trie keys.
//
// NOTE: This is a separate type from types.Modifier (the gocui modifier
// alias) — chord modifiers are an internal bitmask used by the chord
// trie; the gocui Modifier is the runtime-event modifier surface.
type ChordModifier uint8

const (
	ChordModCtrl ChordModifier = 1 << iota
	ChordModAlt
	ChordModShift
	ChordModMeta
)

// SpecialKey enumerates non-rune keys. KeyLeader / KeyLocalLeader are
// compile-time-only sentinels: SequenceFromShorthand emits them when a
// `<leader>` / `<localleader>` token is parsed, and the Build pipeline
// expands them into the configured rune BEFORE inserting into the trie.
// They MUST NOT appear on any trie node — the orphan-prefix tests assert
// this invariant.
type SpecialKey uint8

const (
	// KeyNone is the zero value and identifies a bare-rune ChordKey
	// (Code is the rune; Special is unused).
	KeyNone SpecialKey = iota
	KeyEsc
	KeyEnter
	KeyTab
	KeyBacktab // Shift+Tab; tcell folds it to a standalone Backtab key (ModNone)
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

// ChordKey is one element of a chord Sequence.
//
// For bare-rune keys: Code is the rune, Special is KeyNone.
// For specials (Esc, F1, …): Code is 0, Special names the key.
// Mod carries any held modifiers (Ctrl/Alt/Shift/Meta) for either form.
type ChordKey struct {
	Code    rune
	Special SpecialKey
	Mod     ChordModifier
}

// IsLeaderPlaceholder reports whether k is a compile-time `<leader>` or
// `<localleader>` sentinel. Such keys must be replaced before trie
// insert.
func (k ChordKey) IsLeaderPlaceholder() bool {
	return k.Special == KeyLeader || k.Special == KeyLocalLeader
}

// String returns a stable human-readable label for k (mirrors the input
// shorthand syntax). Used by cheatsheet rendering, warnings, and tests.
func (k ChordKey) String() string {
	var b strings.Builder
	bracketed := k.Special != KeyNone || k.Mod != 0
	if bracketed {
		b.WriteByte('<')
	}
	if k.Mod&ChordModCtrl != 0 {
		b.WriteString("c-")
	}
	if k.Mod&ChordModAlt != 0 {
		b.WriteString("a-")
	}
	if k.Mod&ChordModShift != 0 {
		b.WriteString("s-")
	}
	if k.Mod&ChordModMeta != 0 {
		b.WriteString("m-")
	}
	switch k.Special {
	case KeyNone:
		// A bare space rune (e.g. the expanded leader, default " ")
		// would otherwise render as an invisible literal space — show
		// the open-box glyph so it is legible in hints/cheatsheet.
		if k.Code == ' ' {
			b.WriteRune('␣')
		} else if k.Code != 0 {
			b.WriteRune(k.Code)
		}
	case KeyEsc:
		b.WriteString("esc")
	case KeyEnter:
		b.WriteString("cr")
	case KeyTab:
		b.WriteString("tab")
	case KeyBacktab:
		b.WriteString("backtab")
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
func SequenceString(seq []ChordKey) string {
	var b strings.Builder
	for _, k := range seq {
		b.WriteString(k.String())
	}
	return b.String()
}

// ChordBinding is one shipped or user-defined chord-to-action mapping.
//
// Sequence carries the parsed ChordKey tokens. KeyLeader / KeyLocalLeader
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
// *commands.Command directly.
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
	Sequence    []ChordKey
	Mode        Mode
	Scope       ContextKey
	ActionID    string
	Description string
	Tag         string
	ShowInBar   bool
	OpensMenu   bool
	Source      Source
	Origin      string
}

// ChildRow is one immediate child of a prefix node in a chord trie,
// shaped for the which-key popup. Label is the human description for
// leaves (Command.Description, or "(unbound)" for <nop>) and empty for
// interior children — the popup renderer decides how to present them.
//
// Lives in pkg/gui/types so pkg/gui/context can consume it without
// importing pkg/gui/keys (the keys package re-exports an alias).
type ChildRow struct {
	Key    ChordKey
	Label  string
	IsLeaf bool
	Source Source
}

// WhichKeyState is the renderer-facing surface for the which-key popup.
// Implemented by *keys.WhichKey. The Matcher-facing WhichKeyNotifier
// interface lives in pkg/gui/keys and stays distinct.
//
// Hide is exposed here (despite being mutating) so the orchestrator's
// layout pass can dismiss a notifier that flipped visible for a chord
// prefix with no trie continuations — i.e. the "empty popup" case the
// renderer would otherwise be forced to paint. Calling Hide from the
// layout pass is safe because layout already runs OUTSIDE the Matcher
// mutex (the WhichKeyNotifier contract is therefore upheld).
type WhichKeyState interface {
	Visible() bool
	Snapshot() (scope ContextKey, prefix []ChordKey, visible bool)
	Hide()
}
