package keys

import (
	"errors"
	"fmt"

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
