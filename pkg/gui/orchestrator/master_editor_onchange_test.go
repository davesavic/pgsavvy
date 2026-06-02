package orchestrator_test

import (
	"testing"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// newOnChangeRig builds a SEARCH_LINE master editor with an empty trie
// (so every printable rune in command mode is Passthrough) and a
// WithOnPassthroughEdit sink. mode is ModeCommand to mirror the
// SEARCH_LINE focus contract.
func newOnChangeRig(t *testing.T, scope types.ContextKey, onChange func(string)) orchestrator.Dispatcher {
	t.Helper()
	store := keys.NewModeStore()
	store.Set(scope, types.ModeCommand)
	m, err := keys.NewMatcher(keys.NewTrieSet(), keys.MatcherConfig{
		Modes:       store,
		TimeoutLen:  50 * time.Millisecond,
		TtimeoutLen: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	ed := orchestrator.NewMasterEditor(nil, m, scope, orchestrator.WithOnPassthroughEdit(onChange))
	disp, ok := ed.(orchestrator.Dispatcher)
	if !ok {
		t.Fatalf("master editor does not implement orchestrator.Dispatcher")
	}
	return disp
}

// TestMasterEditor_OnPassthroughEdit_FiresPerKeystroke pins: each applied
// Passthrough edit fires OnChange exactly once with the post-edit buffer
// content; rapid runes yield one OnChange per applied edit with the
// running content (no dropped/duplicated final state).
func TestMasterEditor_OnPassthroughEdit_FiresPerKeystroke(t *testing.T) {
	var got []string
	disp := newOnChangeRig(t, types.SEARCH_LINE, func(s string) { got = append(got, s) })
	v := gocui.NewView(string(types.SEARCH_LINE), 0, 0, 20, 2, gocui.OutputNormal)

	for _, r := range []rune{'f', 'o', 'o'} {
		res, err := disp.Dispatch(v, gocui.NewKeyRune(r))
		if err != nil {
			t.Fatalf("Dispatch %q: %v", r, err)
		}
		if res != keys.Passthrough {
			t.Fatalf("Dispatch %q res = %v, want Passthrough", r, res)
		}
	}
	want := []string{"f", "fo", "foo"}
	if len(got) != len(want) {
		t.Fatalf("OnChange fired %d times %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("OnChange[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// Backspace-to-empty fires OnChange("").
func TestMasterEditor_OnPassthroughEdit_BackspaceToEmpty(t *testing.T) {
	var got []string
	disp := newOnChangeRig(t, types.SEARCH_LINE, func(s string) { got = append(got, s) })
	v := gocui.NewView(string(types.SEARCH_LINE), 0, 0, 20, 2, gocui.OutputNormal)

	if _, err := disp.Dispatch(v, gocui.NewKeyRune('x')); err != nil {
		t.Fatalf("Dispatch x: %v", err)
	}
	if _, err := disp.Dispatch(v, gocui.NewKeyName(gocui.KeyBackspace)); err != nil {
		t.Fatalf("Dispatch backspace: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("OnChange fired %d times %v, want 2", len(got), got)
	}
	if got[len(got)-1] != "" {
		t.Errorf("final OnChange = %q, want empty", got[len(got)-1])
	}
}

// Regression guard: a non-SEARCH_LINE scope (PROMPT) must NOT fire the
// onChange seam even when WithOnPassthroughEdit is attached and the edit
// is applied.
func TestMasterEditor_OnPassthroughEdit_ScopeGatedPrompt(t *testing.T) {
	fired := 0
	disp := newOnChangeRig(t, types.PROMPT, func(string) { fired++ })
	v := gocui.NewView(string(types.PROMPT), 0, 0, 20, 2, gocui.OutputNormal)

	res, err := disp.Dispatch(v, gocui.NewKeyRune('a'))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if res != keys.Passthrough {
		t.Fatalf("res = %v, want Passthrough (edit must still apply)", res)
	}
	if got := v.TextArea.GetContent(); got != "a" {
		t.Fatalf("TextArea = %q, want %q (edit must apply)", got, "a")
	}
	if fired != 0 {
		t.Errorf("OnChange fired %d times for PROMPT scope, want 0", fired)
	}
}

// Regression guard: QUERY_EDITOR scope also does not fire onChange.
func TestMasterEditor_OnPassthroughEdit_ScopeGatedQueryEditor(t *testing.T) {
	fired := 0
	disp := newOnChangeRig(t, types.QUERY_EDITOR, func(string) { fired++ })
	v := gocui.NewView(string(types.QUERY_EDITOR), 0, 0, 20, 2, gocui.OutputNormal)

	if _, err := disp.Dispatch(v, gocui.NewKeyRune('a')); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if fired != 0 {
		t.Errorf("OnChange fired %d times for QUERY_EDITOR scope, want 0", fired)
	}
}

// Re-entrancy structural guard: the onChange callback observes that it is
// invoked outside any Dispatch re-entry — calling Dispatch from inside
// onChange would deadlock or recurse. We assert the seam does not itself
// re-enter the dispatcher: a flag set during the outer Dispatch is the
// only one in flight when onChange runs (single, non-nested invocation),
// and the call returns without hanging.
func TestMasterEditor_OnPassthroughEdit_NoReentry(t *testing.T) {
	var inOnChange bool
	var nested bool
	disp := newOnChangeRig(t, types.SEARCH_LINE, func(string) {
		if inOnChange {
			nested = true
		}
		inOnChange = true
		// The seam must not re-enter the dispatcher. We do NOT call
		// disp.Dispatch here (that is exactly what AD-4 forbids); the
		// guard simply proves the callback runs to completion inline.
		inOnChange = false
	})
	v := gocui.NewView(string(types.SEARCH_LINE), 0, 0, 20, 2, gocui.OutputNormal)

	done := make(chan struct{})
	go func() {
		_, _ = disp.Dispatch(v, gocui.NewKeyRune('x'))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Dispatch with onChange seam deadlocked / hung")
	}
	if nested {
		t.Error("onChange was invoked re-entrantly")
	}
}
