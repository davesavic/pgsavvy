package keys

import (
	"errors"
	"fmt"

	"github.com/gdamore/tcell/v3"
	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// DebugLogger is the minimal logging surface keys.Register depends on.
// *logrus.Logger satisfies it without further wrapping. Defined locally
// so this package does not import pkg/common just for one method.
type DebugLogger interface {
	Debugf(format string, args ...any)
}

// Register wires a single (view, key, mod, handler) tuple onto the
// supplied GuiDriver. It is the single call site for every keyboard
// binding in the dbsavvy TUI; controllers MUST NOT call
// driver.SetKeybinding directly. Doing so bypasses the uniform debug
// log emitted here and breaks the test-recorder inventory check.
//
// description is the human-readable label used by the bindings menu and
// the options bar. Per M11i it should be sourced from Tr.Actions.*.
//
// driver may be nil during test wiring (controller-level unit tests do
// not need a live driver); in that case the call is a silent no-op so
// the controller-attach test does not need to construct a fake just to
// satisfy this seam.
//
// log may be nil; the call still registers the binding (logging is
// best-effort, not load-bearing).
//
// Errors from driver.SetKeybinding are returned verbatim — the only
// known error class is "view does not exist", which is a wiring bug the
// caller should surface loudly.
func Register(
	driver types.GuiDriver,
	log DebugLogger,
	view string,
	key types.Key,
	mod types.Modifier,
	handler func() error,
	description string,
) error {
	if driver == nil {
		return nil
	}
	if log != nil {
		log.Debugf("keys.Register: view=%q key=%v mod=%v desc=%q", view, key, mod, description)
	}
	return driver.SetKeybinding(view, key, mod, handler)
}

// ErrSequenceTooLong is returned by RegisterChord when the binding's
// Sequence has more than one ChordKey. Multi-key dispatch lands in
// dlp.8b/c via the master Editor + Matcher; RegisterChord refuses
// multi-key bindings during the shim period so the orchestrator's
// wiring loop never silently miswires a chord as a single keystroke.
var ErrSequenceTooLong = errors.New("keys: ChordBinding sequence has more than one key; multi-key dispatch is dlp.8b/c")

// ErrNilHandler is returned by RegisterChord when binding.Handler is
// nil. During the dlp.8a shim every ChordBinding produced by a
// controller MUST carry a closure; nil handlers indicate a wiring bug.
var ErrNilHandler = errors.New("keys: ChordBinding has nil Handler")

// RegisterChord is the shim wrapper that lets the existing
// keys.Register single-key wiring path consume the new *ChordBinding
// shape published by controllers in dlp.8a.
//
// It flattens a single-keystroke ChordBinding to (view, gocui.Key,
// gocui.Modifier, handler) and delegates to Register. Multi-key
// bindings are rejected with ErrSequenceTooLong; the orchestrator's
// wiring loop is expected to log + skip such bindings until the
// master Editor / Matcher (dlp.8b/c) takes over chord dispatch.
//
// driver may be nil (test wiring) — Register's nil-driver no-op
// propagates through.
func RegisterChord(driver types.GuiDriver, log DebugLogger, b *types.ChordBinding) error {
	if b == nil {
		return errors.New("keys: nil *ChordBinding")
	}
	if b.Handler == nil {
		return ErrNilHandler
	}
	if len(b.Sequence) == 0 {
		return errors.New("keys: ChordBinding has empty Sequence")
	}
	if len(b.Sequence) > 1 {
		return ErrSequenceTooLong
	}
	key, mod, err := chordKeyToGocui(b.Sequence[0])
	if err != nil {
		return err
	}
	return Register(driver, log, b.ViewName, key, mod, b.Handler, b.Description)
}

// chordKeyToGocui converts a types.ChordKey to the (gocui.Key,
// gocui.Modifier) pair the runtime's SetKeybinding accepts.
//
// Bare-rune ChordKeys map to gocui.NewKeyRune(Code). Special keys map
// via specialKeyToGocui below. Modifiers are translated through
// chordModifierToGocui.
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
