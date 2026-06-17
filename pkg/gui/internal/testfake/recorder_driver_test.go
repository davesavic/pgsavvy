package testfake_test

import (
	"errors"
	"testing"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/pgsavvy/pkg/gui/keys"
	"github.com/davesavic/pgsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
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

// makeView creates the named view in the recorder so SetViewTabs /
// SetViewTabColors find it (they look the view up like the real driver).
func makeView(t *testing.T, r *testfake.RecorderGuiDriver, name string) {
	t.Helper()
	if _, err := r.SetView(name, 0, 0, 10, 5, 0); err != nil && !errors.Is(err, gocui.ErrUnknownView) {
		t.Fatalf("SetView(%q): %v", name, err)
	}
}

func TestSetViewTabs(t *testing.T) {
	r := testfake.NewRecorderGuiDriver()
	makeView(t, r, "results")

	labels := []string{"q1", "q2", "q3"}
	if err := r.SetViewTabs("results", labels, 1); err != nil {
		t.Fatalf("SetViewTabs: %v", err)
	}

	calls := r.AllSetViewTabsCalls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	c := calls[0]
	if c.Name != "results" {
		t.Errorf("Name = %q, want results", c.Name)
	}
	if len(c.Labels) != 3 || c.Labels[0] != "q1" || c.Labels[2] != "q3" {
		t.Errorf("Labels = %v, want [q1 q2 q3]", c.Labels)
	}
	if c.ActiveIdx != 1 {
		t.Errorf("ActiveIdx = %d, want 1", c.ActiveIdx)
	}
}

func TestSetViewTabs_UnknownViewErrors(t *testing.T) {
	r := testfake.NewRecorderGuiDriver()
	err := r.SetViewTabs("nope", []string{"a"}, 0)
	if !errors.Is(err, gocui.ErrUnknownView) {
		t.Fatalf("err = %v, want ErrUnknownView", err)
	}
	if got := len(r.AllSetViewTabsCalls()); got != 0 {
		t.Errorf("recorded %d calls for unknown view, want 0", got)
	}
}

func TestSetViewTabs_EmptyLabels(t *testing.T) {
	r := testfake.NewRecorderGuiDriver()
	makeView(t, r, "v")
	if err := r.SetViewTabs("v", nil, 5); err != nil {
		t.Fatalf("SetViewTabs: %v", err)
	}
	calls := r.AllSetViewTabsCalls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	if len(calls[0].Labels) != 0 {
		t.Errorf("Labels = %v, want empty", calls[0].Labels)
	}
	if calls[0].ActiveIdx != 0 {
		t.Errorf("ActiveIdx = %d, want 0 (clamped for empty)", calls[0].ActiveIdx)
	}
}

func TestSetViewTabs_OutOfRangeActiveIdxClamps(t *testing.T) {
	r := testfake.NewRecorderGuiDriver()
	makeView(t, r, "v")

	if err := r.SetViewTabs("v", []string{"a", "b"}, 99); err != nil {
		t.Fatalf("SetViewTabs high: %v", err)
	}
	if err := r.SetViewTabs("v", []string{"a", "b"}, -3); err != nil {
		t.Fatalf("SetViewTabs low: %v", err)
	}
	calls := r.AllSetViewTabsCalls()
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(calls))
	}
	if calls[0].ActiveIdx != 1 {
		t.Errorf("high idx clamp = %d, want 1", calls[0].ActiveIdx)
	}
	if calls[1].ActiveIdx != 0 {
		t.Errorf("low idx clamp = %d, want 0", calls[1].ActiveIdx)
	}
}

func TestSetTabClick(t *testing.T) {
	r := testfake.NewRecorderGuiDriver()

	got := -1
	if err := r.SetTabClickBinding("results", func(idx int) error {
		got = idx
		return nil
	}); err != nil {
		t.Fatalf("SetTabClickBinding: %v", err)
	}

	if err := r.FeedTabClick("results", 2); err != nil {
		t.Fatalf("FeedTabClick: %v", err)
	}
	if got != 2 {
		t.Errorf("handler fired with idx %d, want 2", got)
	}
}

func TestSetTabClick_UnknownViewErrors(t *testing.T) {
	r := testfake.NewRecorderGuiDriver()
	if err := r.FeedTabClick("nope", 0); err == nil {
		t.Fatal("FeedTabClick on unregistered view: want error, got nil")
	}
}

func TestSetViewTabColors(t *testing.T) {
	r := testfake.NewRecorderGuiDriver()
	makeView(t, r, "results")

	active := gocui.Attribute(42)
	inactive := gocui.Attribute(7)
	if err := r.SetViewTabColors("results", active, inactive); err != nil {
		t.Fatalf("SetViewTabColors: %v", err)
	}

	calls := r.AllSetViewTabColorsCalls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	c := calls[0]
	if c.Name != "results" {
		t.Errorf("Name = %q, want results", c.Name)
	}
	if c.ActiveFg != active {
		t.Errorf("ActiveFg = %v, want %v", c.ActiveFg, active)
	}
	if c.InactiveFg != inactive {
		t.Errorf("InactiveFg = %v, want %v", c.InactiveFg, inactive)
	}
}

func TestSetViewTabColors_UnknownViewErrors(t *testing.T) {
	r := testfake.NewRecorderGuiDriver()
	err := r.SetViewTabColors("nope", gocui.Attribute(1), gocui.Attribute(2))
	if !errors.Is(err, gocui.ErrUnknownView) {
		t.Fatalf("err = %v, want ErrUnknownView", err)
	}
	if got := len(r.AllSetViewTabColorsCalls()); got != 0 {
		t.Errorf("recorded %d calls for unknown view, want 0", got)
	}
}
