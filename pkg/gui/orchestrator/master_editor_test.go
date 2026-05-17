package orchestrator_test

import (
	"sync"
	"testing"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// editorTestRig bundles the moving parts a master-editor test needs.
type editorTestRig struct {
	matcher *keys.Matcher
	editor  gocui.Editor
	disp    orchestrator.Dispatcher
}

func buildSingleBindingTrieSet(seq []keys.Key, mode types.Mode, scope types.ContextKey, cmd *commands.Command) *keys.TrieSet {
	b := keys.NewTrieBuilder()
	b.InsertDefault(&keys.ChordBinding{
		Sequence: seq,
		Mode:     mode,
		Scope:    scope,
		ActionID: cmd.ID,
		Source:   keys.ShippedDefault,
		Origin:   "test",
	}, cmd)
	t, _ := b.Build()
	ts := keys.NewTrieSet()
	ts.Set(mode, scope, t)
	return ts
}

func newRig(t *testing.T, trieSet *keys.TrieSet, scope types.ContextKey, mode types.Mode, tlen, ttlen time.Duration) *editorTestRig {
	t.Helper()
	store := keys.NewModeStore()
	store.Set(scope, mode)
	m, err := keys.NewMatcher(trieSet, keys.MatcherConfig{
		Modes:       store,
		TimeoutLen:  tlen,
		TtimeoutLen: ttlen,
	})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	ed := orchestrator.NewMasterEditor(nil, m, scope)
	disp, ok := ed.(orchestrator.Dispatcher)
	if !ok {
		t.Fatalf("master editor does not implement orchestrator.Dispatcher")
	}
	return &editorTestRig{matcher: m, editor: ed, disp: disp}
}

func TestMasterEditor_Dispatched(t *testing.T) {
	var fired []string
	var mu sync.Mutex
	cmd := &commands.Command{
		ID: "down",
		Handler: func(_ commands.ExecCtx) error {
			mu.Lock()
			fired = append(fired, "down")
			mu.Unlock()
			return nil
		},
	}
	ts := buildSingleBindingTrieSet([]keys.Key{{Code: 'j'}}, types.ModeNormal, types.QUERY_EDITOR, cmd)
	rig := newRig(t, ts, types.QUERY_EDITOR, types.ModeNormal, 50*time.Millisecond, 5*time.Millisecond)

	res, err := rig.disp.Dispatch(nil, gocui.NewKeyRune('j'))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if res != keys.Dispatched {
		t.Fatalf("res = %v, want Dispatched", res)
	}
	mu.Lock()
	got := append([]string(nil), fired...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "down" {
		t.Errorf("fired = %v, want [down]", got)
	}
}

func TestMasterEditor_Pending_InsertModeBuffersRune(t *testing.T) {
	cmd := &commands.Command{ID: "noop", Handler: func(commands.ExecCtx) error { return nil }}
	ts := buildSingleBindingTrieSet([]keys.Key{{Code: 'j'}, {Code: 'k'}}, types.ModeInsert, types.QUERY_EDITOR, cmd)
	rig := newRig(t, ts, types.QUERY_EDITOR, types.ModeInsert, 200*time.Millisecond, 5*time.Millisecond)

	res, err := rig.disp.Dispatch(nil, gocui.NewKeyRune('j'))
	if err != nil {
		t.Fatalf("Dispatch j: %v", err)
	}
	if res != keys.Pending {
		t.Fatalf("res = %v, want Pending", res)
	}
	// Now finish the chord — must fire and clear pending.
	res, err = rig.disp.Dispatch(nil, gocui.NewKeyRune('k'))
	if err != nil {
		t.Fatalf("Dispatch k: %v", err)
	}
	if res != keys.Dispatched {
		t.Fatalf("res after k = %v, want Dispatched", res)
	}
}

func TestMasterEditor_PassthroughInsertModeDelegates(t *testing.T) {
	// Empty trieset so any printable rune in insert mode is Passthrough.
	rig := newRig(t, keys.NewTrieSet(), types.QUERY_EDITOR, types.ModeInsert, 50*time.Millisecond, 5*time.Millisecond)

	v := gocui.NewView("test", 0, 0, 10, 10, gocui.OutputNormal)
	res, err := rig.disp.Dispatch(v, gocui.NewKeyRune('x'))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if res != keys.Passthrough {
		t.Fatalf("res = %v, want Passthrough", res)
	}
	// DefaultEditor should have typed 'x' into the TextArea.
	if got := v.TextArea.GetContent(); got != "x" {
		t.Errorf("TextArea content = %q, want %q", got, "x")
	}
}

func TestMasterEditor_PassthroughNormalModeDropped(t *testing.T) {
	rig := newRig(t, keys.NewTrieSet(), types.QUERY_EDITOR, types.ModeNormal, 50*time.Millisecond, 5*time.Millisecond)

	v := gocui.NewView("test", 0, 0, 10, 10, gocui.OutputNormal)
	// In ModeNormal a printable rune with no binding yields FellThrough
	// (Passthrough is reserved for Insert/Command). Either way the
	// boolean must be false.
	res, _ := rig.disp.Dispatch(v, gocui.NewKeyRune('x'))
	if res == keys.Passthrough {
		t.Fatalf("Normal-mode unbound key emitted Passthrough; should be FellThrough")
	}
	if got := v.TextArea.GetContent(); got != "" {
		t.Errorf("TextArea content = %q, want empty", got)
	}
}

func TestMasterEditor_FellThrough(t *testing.T) {
	rig := newRig(t, keys.NewTrieSet(), types.QUERY_EDITOR, types.ModeNormal, 50*time.Millisecond, 5*time.Millisecond)

	// A special non-rune key in normal mode without any binding falls
	// through.
	res, err := rig.disp.Dispatch(nil, gocui.NewKeyName(gocui.KeyF1))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if res != keys.FellThrough {
		t.Fatalf("res = %v, want FellThrough", res)
	}
}

// TestMasterEditor_FlushOnInsertTimeout verifies that an unresolved
// Insert-mode partial in the master editor causes the Matcher's
// insert-pending-flush callback to fire. The master editor's own
// flushRunes performs the actual TextArea write inside gocui.Update on
// the production path; here we observe the upstream signal (which
// proves the wiring) using a sync-protected sink. Verifying the
// TextArea write directly would race against gocui.TextArea's
// non-concurrency-safe internal state.
func TestMasterEditor_FlushOnInsertTimeout(t *testing.T) {
	cmd := &commands.Command{ID: "noop", Handler: func(commands.ExecCtx) error { return nil }}
	ts := buildSingleBindingTrieSet([]keys.Key{{Code: 'j'}, {Code: 'k'}}, types.ModeInsert, types.QUERY_EDITOR, cmd)
	rig := newRig(t, ts, types.QUERY_EDITOR, types.ModeInsert, 15*time.Millisecond, 5*time.Millisecond)

	// Replace the master editor's flush callback with a sync-protected
	// sink BEFORE driving any keys. NewMasterEditor already registered
	// its own callback; overriding it here is fine for the test since
	// we are observing the matcher's signal, not the editor's write.
	flushed := make(chan []rune, 1)
	rig.matcher.OnInsertPendingFlush(func(scope types.ContextKey, runes []rune) {
		if scope != types.QUERY_EDITOR {
			return
		}
		cp := append([]rune(nil), runes...)
		select {
		case flushed <- cp:
		default:
		}
	})

	res, err := rig.disp.Dispatch(nil, gocui.NewKeyRune('j'))
	if err != nil {
		t.Fatalf("Dispatch j: %v", err)
	}
	if res != keys.Pending {
		t.Fatalf("res = %v, want Pending", res)
	}

	select {
	case got := <-flushed:
		if len(got) != 1 || got[0] != 'j' {
			t.Fatalf("flushed runes = %v, want ['j']", got)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("matcher never invoked insert-pending-flush within 300ms")
	}
}

func TestMasterEditor_ConcurrentDispatchAndFlushNoRace(t *testing.T) {
	// Validates the master editor's own internal state (pendingRunes /
	// captured view) is goroutine-safe across racing Dispatch calls and
	// the flush timer. We pass nil for *gocui.View because gocui's
	// TextArea is itself not concurrency-safe — production only ever
	// drives a single TextArea from the MainLoop goroutine, so racing
	// the TextArea from this test would prove nothing about the master
	// editor.
	cmd := &commands.Command{ID: "noop", Handler: func(commands.ExecCtx) error { return nil }}
	ts := buildSingleBindingTrieSet([]keys.Key{{Code: 'j'}, {Code: 'k'}}, types.ModeInsert, types.QUERY_EDITOR, cmd)
	rig := newRig(t, ts, types.QUERY_EDITOR, types.ModeInsert, 5*time.Millisecond, 2*time.Millisecond)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_, _ = rig.disp.Dispatch(nil, gocui.NewKeyRune('j'))
				time.Sleep(time.Microsecond)
			}
		}()
	}
	wg.Wait()
	// Give the timer one last chance to fire so the goroutine exits
	// cleanly before the test returns (avoids leaking the timer).
	time.Sleep(20 * time.Millisecond)
}
