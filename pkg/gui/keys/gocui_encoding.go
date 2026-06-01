package keys

import (
	"errors"
	"fmt"

	"github.com/gdamore/tcell/v3"
	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// DebugLogger is the minimal logging surface keys helpers depend on.
// *slog.Logger satisfies it without further wrapping. Defined locally
// so this package does not import pkg/common just for one method.
type DebugLogger interface {
	Debug(msg string, args ...any)
}

// ChordKeyToGocui converts a types.ChordKey to the (gocui.Key,
// gocui.Modifier) pair the runtime's SetKeybinding accepts.
//
// Bare-rune ChordKeys map to gocui.NewKeyRune(Code). Special keys map
// via specialKeyToGocui below. Modifiers are translated through
// chordModifierToGocui.
func ChordKeyToGocui(k types.ChordKey) (types.Key, types.Modifier, error) {
	return chordKeyToGocui(k)
}

func chordKeyToGocui(k types.ChordKey) (types.Key, types.Modifier, error) {
	mod := chordModifierToGocui(k.Mod)
	if k.Special == types.KeyNone {
		if k.Code == 0 {
			return types.Key{}, 0, errors.New("keys: empty ChordKey (no Code and no Special)")
		}
		return gocui.NewKeyRune(k.Code), mod, nil
	}
	name, err := specialKeyToGocui(k.Special)
	if err != nil {
		return types.Key{}, 0, err
	}
	return gocui.NewKeyName(name), mod, nil
}

func chordModifierToGocui(m types.ChordModifier) types.Modifier {
	var out types.Modifier
	if m&types.ChordModCtrl != 0 {
		out |= gocui.ModCtrl
	}
	if m&types.ChordModAlt != 0 {
		out |= gocui.ModAlt
	}
	if m&types.ChordModShift != 0 {
		out |= gocui.ModShift
	}
	if m&types.ChordModMeta != 0 {
		// gocui has no ModMeta; fall back to ModAlt which is the
		// closest analog on most terminals.
		out |= gocui.ModAlt
	}
	return out
}

// KeyFromGocui decodes a gocui.Key into the chord-trie Key (ChordKey)
// shape. It is the inverse of chordKeyToGocui: bare-rune gocui keys
// (keyName == tcell.KeyRune) become bare-rune ChordKeys carrying the
// first rune of the key's Str(); named special keys become ChordKeys
// with the matching SpecialKey set.
//
// gocui folds the modifier mask into the Key itself, so this function
// also translates the modifier bits back into ChordModifier flags. An
// unknown special key returns the zero Key — callers (master Editor)
// treat that as "drop" by feeding it to the Matcher which will yield
// FellThrough.
func KeyFromGocui(gk gocui.Key) Key {
	mod := modifierFromGocui(gk.Mod())
	if gk.KeyName() == gocui.KeyName(tcell.KeyRune) {
		var code rune
		if s := gk.Str(); s != "" {
			for _, r := range s {
				code = r
				break
			}
		}
		return Key{Code: code, Mod: mod}
	}
	return Key{Special: specialKeyFromGocui(gk.KeyName()), Mod: mod}
}

// modifierFromGocui translates gocui.Modifier bits back to
// types.ChordModifier. Unknown bits (e.g. ModMotion) are dropped.
func modifierFromGocui(m gocui.Modifier) Modifier {
	var out Modifier
	if m&gocui.ModCtrl != 0 {
		out |= ModCtrl
	}
	if m&gocui.ModAlt != 0 {
		out |= ModAlt
	}
	if m&gocui.ModShift != 0 {
		out |= ModShift
	}
	if m&gocui.ModMeta != 0 {
		out |= ModMeta
	}
	return out
}

// specialKeyFromGocui translates a gocui.KeyName back to the matching
// types.SpecialKey. Unknown names return types.KeyNone — the caller
// gets a Key with both Code and Special zero, which Matcher.Dispatch
// will treat as a fell-through key.
func specialKeyFromGocui(n gocui.KeyName) SpecialKey {
	switch n {
	case gocui.KeyEsc:
		return KeyEsc
	case gocui.KeyEnter:
		return KeyEnter
	case gocui.KeyTab:
		return KeyTab
	case gocui.KeyBacktab:
		return KeyBacktab
	case gocui.KeyBackspace:
		return KeyBs
	case gocui.KeyArrowUp:
		return KeyUp
	case gocui.KeyArrowDown:
		return KeyDown
	case gocui.KeyArrowLeft:
		return KeyLeft
	case gocui.KeyArrowRight:
		return KeyRight
	case gocui.KeyHome:
		return KeyHome
	case gocui.KeyEnd:
		return KeyEnd
	case gocui.KeyPgup:
		return KeyPgUp
	case gocui.KeyPgdn:
		return KeyPgDn
	case gocui.KeyInsert:
		return KeyIns
	case gocui.KeyDelete:
		return KeyDel
	case gocui.KeyF1:
		return KeyF1
	case gocui.KeyF2:
		return KeyF2
	case gocui.KeyF3:
		return KeyF3
	case gocui.KeyF4:
		return KeyF4
	case gocui.KeyF5:
		return KeyF5
	case gocui.KeyF6:
		return KeyF6
	case gocui.KeyF7:
		return KeyF7
	case gocui.KeyF8:
		return KeyF8
	case gocui.KeyF9:
		return KeyF9
	case gocui.KeyF10:
		return KeyF10
	case gocui.KeyF11:
		return KeyF11
	case gocui.KeyF12:
		return KeyF12
	}
	return KeyNone
}

// SpecialKeyToGocui re-exports the SpecialKey → gocui.KeyName mapping
// for callers outside the keys package (e.g. testfake.FeedChord) that
// need to encode a chord Key back into a gocui.Key for replay.
func SpecialKeyToGocui(s types.SpecialKey) (types.KeyName, error) {
	return specialKeyToGocui(s)
}

func specialKeyToGocui(s types.SpecialKey) (types.KeyName, error) {
	switch s {
	case types.KeyEsc:
		return gocui.KeyEsc, nil
	case types.KeyEnter:
		return gocui.KeyEnter, nil
	case types.KeyTab:
		return gocui.KeyTab, nil
	case types.KeyBacktab:
		return gocui.KeyBacktab, nil
	case types.KeyBs:
		return gocui.KeyBackspace, nil
	case types.KeySpace:
		// gocui has no KeySpace name — space is the bare rune ' '.
		return 0, errors.New("keys: KeySpace must be encoded as bare rune ' ', not a SpecialKey")
	case types.KeyUp:
		return gocui.KeyArrowUp, nil
	case types.KeyDown:
		return gocui.KeyArrowDown, nil
	case types.KeyLeft:
		return gocui.KeyArrowLeft, nil
	case types.KeyRight:
		return gocui.KeyArrowRight, nil
	case types.KeyHome:
		return gocui.KeyHome, nil
	case types.KeyEnd:
		return gocui.KeyEnd, nil
	case types.KeyPgUp:
		return gocui.KeyPgup, nil
	case types.KeyPgDn:
		return gocui.KeyPgdn, nil
	case types.KeyIns:
		return gocui.KeyInsert, nil
	case types.KeyDel:
		return gocui.KeyDelete, nil
	case types.KeyF1:
		return gocui.KeyF1, nil
	case types.KeyF2:
		return gocui.KeyF2, nil
	case types.KeyF3:
		return gocui.KeyF3, nil
	case types.KeyF4:
		return gocui.KeyF4, nil
	case types.KeyF5:
		return gocui.KeyF5, nil
	case types.KeyF6:
		return gocui.KeyF6, nil
	case types.KeyF7:
		return gocui.KeyF7, nil
	case types.KeyF8:
		return gocui.KeyF8, nil
	case types.KeyF9:
		return gocui.KeyF9, nil
	case types.KeyF10:
		return gocui.KeyF10, nil
	case types.KeyF11:
		return gocui.KeyF11, nil
	case types.KeyF12:
		return gocui.KeyF12, nil
	case types.KeyLeader, types.KeyLocalLeader:
		return 0, fmt.Errorf("keys: unexpanded leader placeholder %d in chord binding", s)
	}
	return 0, fmt.Errorf("keys: unknown SpecialKey %d", s)
}
