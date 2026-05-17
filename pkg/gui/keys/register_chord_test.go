package keys_test

import (
	"errors"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// dlp.8a §AC scenario "existing quit handler keeps working":
// RegisterChord on a single-key ChordBinding flattens to driver.SetKeybinding
// with the gocui-converted Key/Modifier and the binding's Handler closure.
func TestRegisterChordSingleRuneCallsDriver(t *testing.T) {
	d := &fakeDriver{}
	fired := false
	b := &types.ChordBinding{
		ViewName:    "global",
		Sequence:    []types.ChordKey{{Code: 'q'}},
		Handler:     func() error { fired = true; return nil },
		Description: "Quit",
	}
	if err := keys.RegisterChord(d, nil, b); err != nil {
		t.Fatalf("RegisterChord: %v", err)
	}
	if len(d.calls) != 1 {
		t.Fatalf("SetKeybinding calls = %d, want 1", len(d.calls))
	}
	if d.calls[0].View != "global" {
		t.Fatalf("View = %q, want %q", d.calls[0].View, "global")
	}
	if !d.calls[0].Key.Equals(gocui.NewKeyRune('q')) {
		t.Fatalf("Key = %v, want NewKeyRune('q')", d.calls[0].Key)
	}
	if d.calls[0].Mod != gocui.ModNone {
		t.Fatalf("Mod = %v, want ModNone", d.calls[0].Mod)
	}
	// The handler must be passed through verbatim — RegisterChord does
	// not invoke it itself.
	if fired {
		t.Fatal("RegisterChord must not invoke Handler synchronously")
	}
}

// AC scenario "multi-key sequence rejected": RegisterChord refuses
// bindings whose Sequence length is greater than 1; the orchestrator's
// wiring loop logs and skips them.
func TestRegisterChordMultiKeyReturnsErrSequenceTooLong(t *testing.T) {
	d := &fakeDriver{}
	b := &types.ChordBinding{
		Sequence: []types.ChordKey{{Code: ' '}, {Code: 'q'}},
		Handler:  func() error { return nil },
	}
	err := keys.RegisterChord(d, nil, b)
	if !errors.Is(err, keys.ErrSequenceTooLong) {
		t.Fatalf("err = %v, want ErrSequenceTooLong", err)
	}
	if len(d.calls) != 0 {
		t.Fatalf("driver.SetKeybinding should NOT be called for multi-key bindings; got %d calls", len(d.calls))
	}
}

// AC negative path: a nil Handler is a wiring bug; RegisterChord
// surfaces it with ErrNilHandler so the orchestrator can log + skip.
func TestRegisterChordNilHandlerReturnsErrNilHandler(t *testing.T) {
	d := &fakeDriver{}
	b := &types.ChordBinding{
		Sequence: []types.ChordKey{{Code: 'q'}},
		Handler:  nil,
	}
	err := keys.RegisterChord(d, nil, b)
	if !errors.Is(err, keys.ErrNilHandler) {
		t.Fatalf("err = %v, want ErrNilHandler", err)
	}
	if len(d.calls) != 0 {
		t.Fatalf("driver.SetKeybinding should NOT be called for nil-handler bindings; got %d calls", len(d.calls))
	}
}

// AC negative path: nil *ChordBinding is rejected (defensive).
func TestRegisterChordNilBindingReturnsError(t *testing.T) {
	if err := keys.RegisterChord(&fakeDriver{}, nil, nil); err == nil {
		t.Fatal("RegisterChord(nil) returned nil, want error")
	}
}

// RegisterChord on a special-key ChordKey converts to gocui.KeyName.
func TestRegisterChordSpecialKeyConvertsToGocuiKeyName(t *testing.T) {
	d := &fakeDriver{}
	b := &types.ChordBinding{
		ViewName: "menu",
		Sequence: []types.ChordKey{{Special: types.KeyEsc}},
		Handler:  func() error { return nil },
	}
	if err := keys.RegisterChord(d, nil, b); err != nil {
		t.Fatalf("RegisterChord: %v", err)
	}
	if len(d.calls) != 1 {
		t.Fatalf("calls=%d, want 1", len(d.calls))
	}
	if !d.calls[0].Key.Equals(gocui.NewKeyName(gocui.KeyEsc)) {
		t.Fatalf("Key = %v, want NewKeyName(KeyEsc)", d.calls[0].Key)
	}
}

// RegisterChord with modifier bits sets the gocui modifier.
func TestRegisterChordCtrlModifierMapsToGocuiModCtrl(t *testing.T) {
	d := &fakeDriver{}
	b := &types.ChordBinding{
		Sequence: []types.ChordKey{{Code: 'c', Mod: types.ChordModCtrl}},
		Handler:  func() error { return nil },
	}
	if err := keys.RegisterChord(d, nil, b); err != nil {
		t.Fatalf("RegisterChord: %v", err)
	}
	if d.calls[0].Mod != gocui.ModCtrl {
		t.Fatalf("Mod = %v, want ModCtrl", d.calls[0].Mod)
	}
}

// Nil driver is a silent no-op (matches keys.Register's contract) so
// controller-attach unit tests do not need to build a fake driver.
func TestRegisterChordNilDriverIsNoop(t *testing.T) {
	b := &types.ChordBinding{
		Sequence: []types.ChordKey{{Code: 'q'}},
		Handler:  func() error { return nil },
	}
	if err := keys.RegisterChord(nil, nil, b); err != nil {
		t.Fatalf("RegisterChord(nil driver): %v", err)
	}
}
