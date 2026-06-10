package testfake_test

import (
	"errors"
	"testing"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

type stubEditor struct{}

func (stubEditor) Edit(_ *gocui.View, _ gocui.Key) bool { return false }

func TestRecorder_SetMasterEditor_RecordsAndIsIdempotent(t *testing.T) {
	r := testfake.NewRecorderGuiDriver()
	first := stubEditor{}
	second := stubEditor{}
	if err := r.SetMasterEditor("query_editor", first); err != nil {
		t.Fatalf("SetMasterEditor: %v", err)
	}
	if err := r.SetMasterEditor("query_editor", second); err != nil {
		t.Fatalf("SetMasterEditor 2: %v", err)
	}
	installed := r.InstalledEditors()
	if got := len(installed); got != 1 {
		t.Fatalf("InstalledEditors len = %d, want 1", got)
	}
	if installed["query_editor"] != second {
		t.Errorf("second SetMasterEditor did not replace first")
	}
}

func TestRecorder_InstalledEditors_DefensiveCopy(t *testing.T) {
	r := testfake.NewRecorderGuiDriver()
	_ = r.SetMasterEditor("v1", stubEditor{})
	out := r.InstalledEditors()
	delete(out, "v1")
	if again := r.InstalledEditors(); len(again) != 1 {
		t.Errorf("mutating returned map affected recorder: len=%d", len(again))
	}
}

// TestRecorder_FeedKey_FirstRegisteredWins asserts the recorder models real
// gocui: when two handlers are registered for the SAME (view, key, mod),
// FeedKey fires the FIRST-registered one. gocui's SetKeybinding appends
// (gocui gui.go:551) and execKeybindings forward-scans + returns on the
// first view match (gocui gui.go:1546) — first-registered-wins, NOT last.
func TestRecorder_FeedKey_FirstRegisteredWins(t *testing.T) {
	r := testfake.NewRecorderGuiDriver()
	key := gocui.NewKeyRune('c')
	mod := types.Modifier(gocui.ModCtrl)

	fired := ""
	first := func() error { fired = "first"; return nil }
	second := func() error { fired = "second"; return nil }

	if err := r.SetKeybinding("v", key, mod, first); err != nil {
		t.Fatalf("SetKeybinding first: %v", err)
	}
	if err := r.SetKeybinding("v", key, mod, second); err != nil {
		t.Fatalf("SetKeybinding second: %v", err)
	}

	if err := r.FeedKey("v", key, mod); err != nil {
		t.Fatalf("FeedKey: %v", err)
	}
	if fired != "first" {
		t.Fatalf("FeedKey fired %q handler, want %q (first-registered must win)", fired, "first")
	}
}

func TestRecorder_FeedChord_DrivesThroughMasterEditor(t *testing.T) {
	// Install a real master editor backed by a Matcher with [j,k] in normal mode.
	cmd := &commands.Command{ID: "fire", Handler: func(commands.ExecCtx) error { return nil }}
	b := keys.NewTrieBuilder()
	b.InsertDefault(&keys.ChordBinding{
		Sequence: []keys.Key{{Code: 'j'}, {Code: 'k'}},
		Mode:     types.ModeNormal,
		Scope:    types.QUERY_EDITOR,
		ActionID: cmd.ID,
		Source:   keys.ShippedDefault,
		Origin:   "test",
	}, cmd)
	trie, _ := b.Build()
	ts := keys.NewTrieSet()
	ts.Set(types.ModeNormal, types.QUERY_EDITOR, trie)

	store := keys.NewModeStore()
	store.Set(types.QUERY_EDITOR, types.ModeNormal)
	m, err := keys.NewMatcher(ts, keys.MatcherConfig{
		Modes:       store,
		TimeoutLen:  50 * time.Millisecond,
		TtimeoutLen: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	ed := orchestrator.NewMasterEditor(nil, m, types.QUERY_EDITOR)

	r := testfake.NewRecorderGuiDriver()
	if err := r.SetMasterEditor("query_editor", ed); err != nil {
		t.Fatalf("SetMasterEditor: %v", err)
	}

	res, err := r.FeedChord("query_editor", []keys.Key{{Code: 'j'}, {Code: 'k'}})
	if err != nil {
		t.Fatalf("FeedChord: %v", err)
	}
	if res != keys.Dispatched {
		t.Errorf("FeedChord final result = %v, want Dispatched", res)
	}
}

func TestRecorder_FeedChord_UnknownViewReturnsErrNoEditor(t *testing.T) {
	r := testfake.NewRecorderGuiDriver()
	_, err := r.FeedChord("nope", []keys.Key{{Code: 'a'}})
	if !errors.Is(err, testfake.ErrNoEditor) {
		t.Fatalf("err = %v, want ErrNoEditor", err)
	}
}

func TestRecorder_FeedChord_NonDispatcherEditor(t *testing.T) {
	r := testfake.NewRecorderGuiDriver()
	_ = r.SetMasterEditor("v", stubEditor{})
	_, err := r.FeedChord("v", []keys.Key{{Code: 'a'}})
	if !errors.Is(err, testfake.ErrEditorNotDispatcher) {
		t.Fatalf("err = %v, want ErrEditorNotDispatcher", err)
	}
}
