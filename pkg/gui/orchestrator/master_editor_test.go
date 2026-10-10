package orchestrator_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"
	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/keys"
	"github.com/davesavic/pgsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/query"
)

// editorTestRig bundles the moving parts a master-editor test needs.
type editorTestRig struct {
	matcher *keys.Matcher
	editor  gocui.Editor
	disp    orchestrator.Dispatcher
}

func reviewAmbiguousTrie(mode types.Mode, scope types.ContextKey, leaf *commands.Command) *keys.TrieSet {
	b := keys.NewTrieBuilder()
	for i, seq := range [][]keys.Key{{{Code: 'z'}}, {{Code: 'z'}, {Code: 'x'}}} {
		cmd := leaf
		if i == 1 {
			cmd = &commands.Command{ID: "review.long", Handler: func(commands.ExecCtx) error { return nil }}
		}
		b.InsertDefault(&keys.ChordBinding{Sequence: seq, Mode: mode, Scope: scope, ActionID: cmd.ID, Source: keys.ShippedDefault}, cmd)
	}
	trie, _ := b.Build()
	ts := keys.NewTrieSet()
	ts.Set(mode, scope, trie)
	return ts
}

func TestReview_timers_wired_close(t *testing.T) {
	for _, closeBeforeDrain := range []bool{false, true} {
		t.Run(fmt.Sprint(closeBeforeDrain), func(t *testing.T) {
			fs := afero.NewMemMapFs()
			c := common.NewCommon(slog.New(slog.DiscardHandler), i18n.EnglishTranslationSet(), config.GetDefaultConfig(), &common.AppState{}, fs)
			// Isolate timeout delivery: the hint redraw must not be the
			// first queued update this test treats as matcher expiry.
			g := orchestrator.NewGui(orchestrator.Deps{
				Common: c, Store: common.NewAppStateStore(fs, "/state.yml", common.DefaultClock()),
				ConnectionsProvider: func() []models.Connection { return nil },
				HistoryProvider:     func() (*query.History, error) { return nil, nil },
			}, orchestrator.WithKeyDelays(20*time.Millisecond, time.Millisecond, time.Second))
			d := newQueueingDriver()
			if err := g.UseDriverForTest(d); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = g.Close() })
			d.drainPending()
			var calls atomic.Int32
			g.Matcher().SwapTrieSet(reviewAmbiguousTrie(types.ModeNormal, types.QUERY_EDITOR, &commands.Command{ID: "review.wired", Handler: func(commands.ExecCtx) error { calls.Add(1); return nil }}))
			if _, err := g.Matcher().Dispatch(types.QUERY_EDITOR, keys.Key{Code: 'z'}); err != nil {
				t.Fatal(err)
			}
			var expiry func() error
			select {
			case expiry = <-d.updates:
			case <-time.After(3 * time.Second):
				t.Fatalf("production matcher did not enqueue timeout (handler calls=%d)", calls.Load())
			}
			if calls.Load() != 0 {
				t.Fatal("production timeout handler ran before UI drain")
			}
			want := int32(1)
			if closeBeforeDrain {
				if err := g.Close(); err != nil {
					t.Fatal(err)
				}
				want = 0
			}
			if err := expiry(); err != nil || calls.Load() != want {
				t.Fatalf("timeout delivery err=%v calls=%d, want %d", err, calls.Load(), want)
			}
		})
	}
}

func TestReview_timers_native(t *testing.T) {
	for _, mode := range []string{"quit", "tab", "text", "broken"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReview_timers_native_process$", "-test.v")
			cmd.Env = append(os.Environ(), "PGSAVVY_TIMER_PROBE="+mode)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("native %s: %v (deadline=%v)\n%s", mode, err, ctx.Err(), out)
			}
			t.Logf("%s", out)
		})
	}
}

func TestReview_timers_native_process(t *testing.T) {
	probe := os.Getenv("PGSAVVY_TIMER_PROBE")
	if probe == "" {
		return
	}
	g, err := gocui.NewGui(gocui.NewGuiOpts{Headless: true, Width: 80, Height: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	scope := types.COMMAND_LINE
	mode := types.ModeCommand
	store := keys.NewModeStore()
	store.Set(scope, mode)
	toasts := 0
	calls := 0
	rail := guicontext.NewContextTree(types.ContextTreeDeps{}).SchemaRail
	ts := buildSingleBindingTrieSet([]keys.Key{{Code: '界'}, {Code: 'x'}}, mode, scope, &commands.Command{ID: "review.text", Handler: func(commands.ExecCtx) error { return nil }})
	if probe == "quit" || probe == "tab" {
		ts = reviewAmbiguousTrie(mode, scope, &commands.Command{ID: "review.quit", Handler: func(commands.ExecCtx) error {
			calls++
			if probe == "tab" {
				rail.SetActiveTab(guicontext.SchemaRailTabTables)
			}
			return gocui.ErrQuit
		}})
	}
	m, err := keys.NewMatcher(ts, keys.MatcherConfig{
		Modes: store, TimeoutLen: 10 * time.Millisecond, Toaster: func(string) { toasts++ },
		OnTimeout: func(fn func() error) {
			g.Update(func(g *gocui.Gui) error {
				err := fn()
				if probe != "quit" && probe != "tab" {
					v, viewErr := g.View(string(scope))
					if viewErr != nil {
						return viewErr
					}
					if text := v.TextArea.GetContent(); text != "界" {
						return fmt.Errorf("timeout text=%q, want 界 in the same UI delivery", text)
					}
					return gocui.ErrQuit
				}
				return err
			})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Cancel()
	ed := orchestrator.NewMasterEditor(g, m, scope)
	disp := ed.(orchestrator.Dispatcher)
	started := false
	g.SetManagerFunc(func(g *gocui.Gui) error {
		v, err := g.SetView(string(scope), 0, 0, 60, 10, 0)
		if err != nil && !errors.Is(err, gocui.ErrUnknownView) {
			return err
		}
		if started {
			return nil
		}
		started = true
		if probe == "quit" || probe == "tab" {
			_, err := m.Dispatch(scope, keys.Key{Code: 'z'})
			return err
		}
		if _, err := disp.Dispatch(v, gocui.NewKeyRune('界')); err != nil {
			return err
		}
		if probe == "broken" {
			if _, err := disp.Dispatch(v, gocui.NewKeyRune('o')); err != nil {
				return err
			}
			if text := v.TextArea.GetContent(); text != "界o" {
				return fmt.Errorf("broken-prefix text=%q, want 界o", text)
			}
			return gocui.ErrQuit
		}
		return nil
	})
	if err := g.MainLoop(); !errors.Is(err, gocui.ErrQuit) {
		t.Fatalf("MainLoop: %v", err)
	}
	if (probe == "quit" || probe == "tab") && (calls != 1 || toasts != 0) {
		t.Fatalf("timed quit calls=%d toasts=%d", calls, toasts)
	}
	if probe == "tab" && rail.ActiveTab() != guicontext.SchemaRailTabTables {
		t.Fatal("native timed tab switch was not delivered")
	}
	t.Logf("native %s: MainLoop exited with ErrQuit; text/error ownership verified", probe)
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
// insert-pending-flush callback to fire. This standalone nil-GUI rig observes
// the upstream signal using a sync-protected sink. The native timer probes
// separately verify the actual TextArea write on the scheduled UI lane.
func TestMasterEditor_FlushOnInsertTimeout(t *testing.T) {
	cmd := &commands.Command{ID: "noop", Handler: func(commands.ExecCtx) error { return nil }}
	ts := buildSingleBindingTrieSet([]keys.Key{{Code: 'j'}, {Code: 'k'}}, types.ModeInsert, types.QUERY_EDITOR, cmd)
	rig := newRig(t, ts, types.QUERY_EDITOR, types.ModeInsert, 15*time.Millisecond, 5*time.Millisecond)

	// Replace the master editor's flush callback with a sync-protected
	// sink BEFORE driving any keys. NewMasterEditor already registered
	// its own callback; overriding it here is fine for the test since
	// we are observing the matcher's signal, not the editor's write.
	flushed := make(chan []rune, 1)
	rig.matcher.OnInsertPendingFlush(types.QUERY_EDITOR, func(scope types.ContextKey, runes []rune) {
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

// TestMasterEditor_PasteSuppressesCommitChord reproduces pgsavvy-v511:
// a bracketed paste streams keys into an editable popup while
// gocui.IsPasting is true. A newline in the pasted text arrives as
// KeyEnter, which the CELL_EDITOR insert-mode trie binds to
// cell.edit.commit — firing it mid-paste commits a partial value,
// pops the popup, and lets the remaining pasted keys dispatch as
// normal-mode actions. While the paste is in flight the master editor
// must swallow Enter/Esc instead of dispatching; once the paste ends
// the same key must dispatch normally.
func TestMasterEditor_PasteSuppressesCommitChord(t *testing.T) {
	var fired atomic.Int32
	cmd := &commands.Command{
		ID: "cell.edit.commit",
		Handler: func(_ commands.ExecCtx) error {
			fired.Add(1)
			return nil
		},
	}
	ts := buildSingleBindingTrieSet([]keys.Key{{Special: keys.KeyEnter}}, types.ModeInsert, types.CELL_EDITOR, cmd)

	pasting := true
	store := keys.NewModeStore()
	store.Set(types.CELL_EDITOR, types.ModeInsert)
	m, err := keys.NewMatcher(ts, keys.MatcherConfig{
		Modes:       store,
		TimeoutLen:  50 * time.Millisecond,
		TtimeoutLen: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	ed := orchestrator.NewMasterEditor(nil, m, types.CELL_EDITOR, orchestrator.WithPasteState(func() bool { return pasting }))
	disp, ok := ed.(orchestrator.Dispatcher)
	if !ok {
		t.Fatalf("master editor does not implement orchestrator.Dispatcher")
	}

	// Paste in flight: Enter must be swallowed, the commit handler must
	// NOT fire, and the Matcher must not retain pending state.
	res, err := disp.Dispatch(nil, gocui.NewKeyName(gocui.KeyEnter))
	if err != nil {
		t.Fatalf("Dispatch Enter during paste: %v", err)
	}
	if res != keys.Swallowed {
		t.Fatalf("res during paste = %v, want Swallowed", res)
	}
	if got := fired.Load(); got != 0 {
		t.Fatalf("commit fired %d times during paste, want 0", got)
	}
	if m.IsPartial() {
		t.Fatal("matcher holds pending state after paste-swallowed Enter")
	}

	// Escape is the other commit chord in the cell editor — also inert
	// while pasting.
	res, err = disp.Dispatch(nil, gocui.NewKeyName(gocui.KeyEsc))
	if err != nil {
		t.Fatalf("Dispatch Esc during paste: %v", err)
	}
	if res != keys.Swallowed {
		t.Fatalf("res for Esc during paste = %v, want Swallowed", res)
	}

	// Paste ends: the same Enter now dispatches the commit binding.
	pasting = false
	res, err = disp.Dispatch(nil, gocui.NewKeyName(gocui.KeyEnter))
	if err != nil {
		t.Fatalf("Dispatch Enter after paste: %v", err)
	}
	if res != keys.Dispatched {
		t.Fatalf("res after paste = %v, want Dispatched", res)
	}
	if got := fired.Load(); got != 1 {
		t.Fatalf("commit fired %d times after paste, want 1", got)
	}
}

// TestMasterEditor_PastePrintableRunesStillInsert verifies the paste
// guard is narrow: while pasting, printable runes must still reach
// the TextArea via the normal passthrough path.
func TestMasterEditor_PastePrintableRunesStillInsert(t *testing.T) {
	store := keys.NewModeStore()
	store.Set(types.CELL_EDITOR, types.ModeInsert)
	m, err := keys.NewMatcher(keys.NewTrieSet(), keys.MatcherConfig{
		Modes:       store,
		TimeoutLen:  50 * time.Millisecond,
		TtimeoutLen: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	ed := orchestrator.NewMasterEditor(nil, m, types.CELL_EDITOR, orchestrator.WithPasteState(func() bool { return true }))
	disp, ok := ed.(orchestrator.Dispatcher)
	if !ok {
		t.Fatalf("master editor does not implement orchestrator.Dispatcher")
	}
	v := gocui.NewView("test", 0, 0, 10, 10, gocui.OutputNormal)
	res, err := disp.Dispatch(v, gocui.NewKeyRune('x'))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if res != keys.Passthrough {
		t.Fatalf("res = %v, want Passthrough", res)
	}
	if got := v.TextArea.GetContent(); got != "x" {
		t.Errorf("TextArea content = %q, want %q", got, "x")
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
	for range 4 {
		wg.Go(func() {
			for range 50 {
				_, _ = rig.disp.Dispatch(nil, gocui.NewKeyRune('j'))
				time.Sleep(time.Microsecond)
			}
		})
	}
	wg.Wait()
	// Give the timer one last chance to fire so the goroutine exits
	// cleanly before the test returns (avoids leaking the timer).
	time.Sleep(20 * time.Millisecond)
}
