package orchestrator

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/gui"
	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/data"
	uihelpers "github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// minimalRootCtx mirrors the package-local minimalCtx used by the ui
// helper tests — a no-op IBaseContext used solely to root the focus
// stack so a popup Pop() does not hit ErrPopAtBottom.
type minimalRootCtx struct {
	key  types.ContextKey
	kind types.ContextKind
}

func (m *minimalRootCtx) GetKey() types.ContextKey                      { return m.key }
func (m *minimalRootCtx) GetViewName() string                           { return string(m.key) }
func (m *minimalRootCtx) GetWindowName() string                         { return string(m.key) }
func (m *minimalRootCtx) GetKind() types.ContextKind                    { return m.kind }
func (m *minimalRootCtx) GetTitle() string                              { return "" }
func (m *minimalRootCtx) HandleFocus(_ types.OnFocusOpts) error         { return nil }
func (m *minimalRootCtx) HandleFocusLost(_ types.OnFocusLostOpts) error { return nil }
func (m *minimalRootCtx) HandleRender() error                           { return nil }
func (m *minimalRootCtx) HandleRenderToMain() error                     { return nil }
func (m *minimalRootCtx) HandleQuit() error                             { return nil }
func (m *minimalRootCtx) NeedsRerenderOnHeightChange() bool             { return false }
func (m *minimalRootCtx) NeedsRerenderOnWidthChange() bool              { return false }
func (m *minimalRootCtx) AddKeybindingsFn(_ types.KeybindingsFn)        {}
func (m *minimalRootCtx) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	return nil
}

func (m *minimalRootCtx) GetMouseKeybindings(_ types.KeybindingsOpts) []types.MouseBinding {
	return nil
}

// pushRoot installs a SIDE_CONTEXT root underneath any popup so Pop()
// does not hit ErrPopAtBottom.
func pushRootCh(t *testing.T, tree *gui.ContextTree) {
	t.Helper()
	root := &minimalRootCtx{key: types.CONNECTIONS, kind: types.SIDE_CONTEXT}
	if err := tree.Push(root); err != nil {
		t.Fatalf("push root: %v", err)
	}
}

func newPromptCtxCh() *guicontext.PromptContext {
	base := guicontext.NewBaseContext(guicontext.BaseContextOpts{
		Key:      types.PROMPT,
		ViewName: string(types.PROMPT),
		Kind:     types.TEMPORARY_POPUP,
	})
	return guicontext.NewPromptContext(base, guicontext.Deps{})
}

func newSelectionCtxCh() *guicontext.SelectionContext {
	base := guicontext.NewBaseContext(guicontext.BaseContextOpts{
		Key:      types.SELECTION,
		ViewName: string(types.SELECTION),
		Kind:     types.TEMPORARY_POPUP,
	})
	return guicontext.NewSelectionContext(base, guicontext.Deps{})
}

// uiExecutor serializes onUIThread submissions onto a single dedicated
// goroutine. This mirrors the real gocui MainLoop contract (every
// helper mutation runs on one thread) so we never race the user
// goroutine's Submit/Cancel against the adapter's Prompt re-push.
//
// runOnUI is what the test goroutine uses to deliver Submit/Cancel
// "user actions" to the same serialized lane the adapter pushes
// through — exactly what production does (the gocui keybinding
// handlers also run on the MainLoop).
type uiExecutor struct {
	mu      sync.Mutex
	jobs    chan func() error
	done    chan struct{}
	stopped bool
}

func newUIExecutor() *uiExecutor {
	e := &uiExecutor{
		jobs: make(chan func() error, 64),
		done: make(chan struct{}),
	}
	go func() {
		for fn := range e.jobs {
			_ = fn()
		}
		close(e.done)
	}()
	return e
}

// submit is no-op once the executor has been shut down. This lets
// late-firing ctx-watcher goroutines (e.g. from a still-running
// PromptString call whose ctx fires after the test body returns)
// safely race with t.Cleanup without panicking on a closed channel.
func (e *uiExecutor) submit(fn func() error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopped {
		return
	}
	e.jobs <- fn
}

// runOnUI submits fn and waits for it to complete. Used by the test
// goroutine to simulate user Submit/Cancel actions on the UI lane.
func (e *uiExecutor) runOnUI(fn func() error) error {
	doneCh := make(chan error, 1)
	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		return nil
	}
	e.jobs <- func() error {
		doneCh <- fn()
		return nil
	}
	e.mu.Unlock()
	return <-doneCh
}

func (e *uiExecutor) shutdown() {
	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		return
	}
	e.stopped = true
	close(e.jobs)
	e.mu.Unlock()
	<-e.done
}

// newTestAdapter builds an adapter wired to real helpers + a fresh
// focus tree + a single-threaded UI executor. The executor lifetime is
// managed via t.Cleanup.
//
// Returns the adapter, the helpers (for driving the user actions), a
// counter that tracks how many times onUIThread was invoked, and the
// executor so callers can route user actions onto the same UI lane.
func newTestAdapter(t *testing.T) (
	*chainedPrompterAdapter,
	*uihelpers.PromptHelper,
	*uihelpers.ChoiceHelper,
	*int32,
	*uiExecutor,
) {
	t.Helper()
	tree := gui.NewContextTree()
	pushRootCh(t, tree)
	ph := uihelpers.NewPromptHelper(tree, newPromptCtxCh())
	ch := uihelpers.NewChoiceHelper(tree, newSelectionCtxCh())
	exec := newUIExecutor()
	t.Cleanup(exec.shutdown)
	var calls int32
	onUI := func(fn func() error) {
		atomic.AddInt32(&calls, 1)
		exec.submit(fn)
	}
	return newChainedPrompterAdapter(ph, ch, onUI), ph, ch, &calls, exec
}

// waitForPromptActive blocks until the prompt helper reports Active(),
// up to timeout. Returns true if active, false on timeout.
func waitForPromptActive(ph *uihelpers.PromptHelper, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ph.Active() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

func waitForChoiceActive(ch *uihelpers.ChoiceHelper, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ch.Active() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

func TestChainedPrompterAdapter_PromptString_ValidateSucceedsOnSecondTry(t *testing.T) {
	adapter, ph, _, calls, exec := newTestAdapter(t)

	validate := func(v string) error {
		if v == "" {
			return errors.New("name must not be empty")
		}
		return nil
	}

	type res struct {
		v   string
		err error
	}
	done := make(chan res, 1)
	go func() {
		v, err := adapter.PromptString(context.Background(), "Name", "Connection name", validate)
		done <- res{v, err}
	}()

	if !waitForPromptActive(ph, 200*time.Millisecond) {
		t.Fatal("prompt did not become active")
	}
	// First submit: empty → validate fails → re-push.
	if err := exec.runOnUI(func() error { return ph.Submit("") }); err != nil {
		t.Fatalf("Submit(\"\"): %v", err)
	}
	// After the re-push (scheduled via onUIThread), the helper should
	// be active again with the error embedded in the label and initial
	// preserved as the raw submitted value ("").
	if !waitForPromptActive(ph, 200*time.Millisecond) {
		t.Fatal("prompt did not re-activate after validate failure")
	}
	if got := ph.Initial(); got != "" {
		t.Fatalf("Initial after re-push = %q; want \"\" (raw input preserved)", got)
	}
	if lbl := ph.Label(); !strings.Contains(lbl, "Connection name") || !strings.Contains(lbl, "name must not be empty") {
		t.Fatalf("re-push Label = %q; want it to contain both original label and validate error", lbl)
	}

	// Second submit: valid.
	if err := exec.runOnUI(func() error { return ph.Submit("alice") }); err != nil {
		t.Fatalf("Submit(\"alice\"): %v", err)
	}
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("PromptString err = %v", r.err)
		}
		if r.v != "alice" {
			t.Fatalf("PromptString value = %q; want alice", r.v)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("PromptString did not return after valid submit")
	}

	// onUIThread invocation count: initial push + re-push on validate
	// fail = 2 minimum. (The ctx watcher fires onUIThread only on
	// ctx-cancel, which did not happen here.)
	if c := atomic.LoadInt32(calls); c < 2 {
		t.Fatalf("onUIThread call count = %d; want >= 2 (initial push + validate-fail re-push)", c)
	}
}

func TestChainedPrompterAdapter_PromptString_CtxCancelMidPrompt(t *testing.T) {
	adapter, ph, _, _, _ := newTestAdapter(t)
	ctx, cancel := context.WithCancel(context.Background())

	type res struct {
		v   string
		err error
	}
	done := make(chan res, 1)
	go func() {
		v, err := adapter.PromptString(ctx, "Name", "label", nil)
		done <- res{v, err}
	}()

	if !waitForPromptActive(ph, 200*time.Millisecond) {
		t.Fatal("prompt did not become active")
	}
	cancel()

	select {
	case r := <-done:
		if !errors.Is(r.err, context.Canceled) {
			t.Fatalf("err = %v; want context.Canceled", r.err)
		}
		if r.v != "" {
			t.Fatalf("value = %q; want empty", r.v)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("PromptString did not return within 100ms of ctx cancel")
	}

	// The ctx watcher's onUIThread Cancel should have driven the
	// helper inactive. Allow a tiny grace window for the watcher
	// goroutine to land.
	for range 50 {
		if !ph.Active() {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if ph.Active() {
		t.Fatal("prompt still Active after ctx cancel; want helper.Cancel to have fired")
	}
}

func TestChainedPrompterAdapter_PromptString_CancelBeforeTyping(t *testing.T) {
	adapter, ph, _, _, exec := newTestAdapter(t)

	type res struct {
		v   string
		err error
	}
	done := make(chan res, 1)
	go func() {
		v, err := adapter.PromptString(context.Background(), "T", "L", nil)
		done <- res{v, err}
	}()

	if !waitForPromptActive(ph, 200*time.Millisecond) {
		t.Fatal("prompt did not become active")
	}
	if err := exec.runOnUI(func() error { return ph.Cancel() }); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	select {
	case r := <-done:
		if !errors.Is(r.err, data.PromptCanceledErr()) {
			t.Fatalf("err = %v; want PromptCanceledErr", r.err)
		}
		if r.v != "" {
			t.Fatalf("value = %q; want empty", r.v)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("PromptString did not return after Cancel")
	}
}

func TestChainedPrompterAdapter_PromptString_TrimmedOnSuccess(t *testing.T) {
	adapter, ph, _, _, exec := newTestAdapter(t)

	// validate accepts any non-empty trimmed input; this also lets us
	// observe that lastValue (= raw input) is what would be used for a
	// re-push, while the returned value is trimmed.
	validate := func(v string) error {
		if v == "" {
			return errors.New("empty")
		}
		return nil
	}

	type res struct {
		v   string
		err error
	}
	done := make(chan res, 1)
	go func() {
		v, err := adapter.PromptString(context.Background(), "Name", "L", validate)
		done <- res{v, err}
	}()

	if !waitForPromptActive(ph, 200*time.Millisecond) {
		t.Fatal("prompt did not become active")
	}
	if err := exec.runOnUI(func() error { return ph.Submit("  alice  ") }); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("err = %v", r.err)
		}
		if r.v != "alice" {
			t.Fatalf("value = %q; want trimmed \"alice\"", r.v)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("PromptString did not return")
	}
}

func TestChainedPrompterAdapter_PromptString_LastValuePreservedOnReprompt(t *testing.T) {
	adapter, ph, _, _, exec := newTestAdapter(t)

	// validate always fails on the first call, succeeds on the second
	// (regardless of value). The point of this test is to assert that
	// the re-push uses the RAW input as the new Initial value (per AD
	// #2), not the trimmed value.
	calls := 0
	validate := func(v string) error {
		calls++
		if calls == 1 {
			return errors.New("nope")
		}
		return nil
	}

	type res struct {
		v   string
		err error
	}
	done := make(chan res, 1)
	go func() {
		v, err := adapter.PromptString(context.Background(), "T", "L", validate)
		done <- res{v, err}
	}()

	if !waitForPromptActive(ph, 200*time.Millisecond) {
		t.Fatal("prompt did not become active")
	}
	// Submit raw input with surrounding whitespace; validate fails on
	// the FIRST call so this triggers a re-push.
	if err := exec.runOnUI(func() error { return ph.Submit("  bob  ") }); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if !waitForPromptActive(ph, 200*time.Millisecond) {
		t.Fatal("prompt did not re-activate")
	}
	if got := ph.Initial(); got != "  bob  " {
		t.Fatalf("Initial after re-push = %q; want \"  bob  \" (raw input preserved)", got)
	}
	// Second submit: any value succeeds.
	if err := exec.runOnUI(func() error { return ph.Submit("anything") }); err != nil {
		t.Fatalf("Submit 2: %v", err)
	}
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("err = %v", r.err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("PromptString did not return")
	}
}

func TestChainedPrompterAdapter_PromptChoice_Valid(t *testing.T) {
	adapter, _, ch, _, exec := newTestAdapter(t)

	type res struct {
		v   string
		err error
	}
	done := make(chan res, 1)
	go func() {
		v, err := adapter.PromptChoice(context.Background(), "Driver", "Pick", []string{"postgres", "mysql"})
		done <- res{v, err}
	}()

	if !waitForChoiceActive(ch, 200*time.Millisecond) {
		t.Fatal("choice popup did not become active")
	}
	if err := exec.runOnUI(func() error { return ch.Submit(1) }); err != nil {
		t.Fatalf("Submit(1): %v", err)
	}
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("err = %v", r.err)
		}
		if r.v != "mysql" {
			t.Fatalf("value = %q; want mysql", r.v)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("PromptChoice did not return")
	}
}

func TestChainedPrompterAdapter_PromptChoice_Cancel(t *testing.T) {
	adapter, _, ch, _, exec := newTestAdapter(t)

	type res struct {
		v   string
		err error
	}
	done := make(chan res, 1)
	go func() {
		v, err := adapter.PromptChoice(context.Background(), "Driver", "Pick", []string{"a", "b"})
		done <- res{v, err}
	}()

	if !waitForChoiceActive(ch, 200*time.Millisecond) {
		t.Fatal("choice popup did not become active")
	}
	if err := exec.runOnUI(func() error { return ch.Cancel() }); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	select {
	case r := <-done:
		if !errors.Is(r.err, data.PromptCanceledErr()) {
			t.Fatalf("err = %v; want PromptCanceledErr", r.err)
		}
		if r.v != "" {
			t.Fatalf("value = %q; want empty", r.v)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("PromptChoice did not return")
	}
}

func TestChainedPrompterAdapter_PromptString_RepromptLabelIncludesError(t *testing.T) {
	adapter, ph, _, _, exec := newTestAdapter(t)

	sentinel := "this-is-the-validate-error"
	validate := func(v string) error {
		if v == "x" {
			return nil
		}
		return errors.New(sentinel)
	}

	type res struct {
		v   string
		err error
	}
	done := make(chan res, 1)
	go func() {
		v, err := adapter.PromptString(context.Background(), "MyTitle", "MyLabel", validate)
		done <- res{v, err}
	}()

	if !waitForPromptActive(ph, 200*time.Millisecond) {
		t.Fatal("prompt did not become active")
	}
	if err := exec.runOnUI(func() error { return ph.Submit("nope") }); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if !waitForPromptActive(ph, 200*time.Millisecond) {
		t.Fatal("prompt did not re-activate")
	}
	lbl := ph.Label()
	if !strings.Contains(lbl, "MyLabel") {
		t.Errorf("re-push label %q does not contain original label", lbl)
	}
	if !strings.Contains(lbl, sentinel) {
		t.Errorf("re-push label %q does not contain validate error %q", lbl, sentinel)
	}

	if err := exec.runOnUI(func() error { return ph.Submit("x") }); err != nil {
		t.Fatalf("Submit x: %v", err)
	}
	<-done
}

// fakeChoicePopup is a hand-rolled choicePopup that captures the
// onSubmit closure from Choose so the test can drive it with arbitrary
// idx values — including the out-of-range case the real
// *ui.ChoiceHelper.Submit rejects before the closure runs. The adapter's
// defensive re-push at the inner onSubmit closure is only reachable via
// such a fake.
type fakeChoicePopup struct {
	mu           sync.Mutex
	chooseCalls  int
	lastLabel    string
	lastChoices  []string
	lastOnSubmit func(idx int) error
	lastOnCancel func() error
	active       bool
}

func (f *fakeChoicePopup) Choose(label string, choices []string, onSubmit func(idx int) error, onCancel func() error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chooseCalls++
	f.lastLabel = label
	f.lastChoices = choices
	f.lastOnSubmit = onSubmit
	f.lastOnCancel = onCancel
	f.active = true
	return nil
}

func (f *fakeChoicePopup) Submit(idx int) error {
	f.mu.Lock()
	cb := f.lastOnSubmit
	f.active = false
	f.lastOnSubmit = nil
	f.lastOnCancel = nil
	f.mu.Unlock()
	if cb == nil {
		return nil
	}
	return cb(idx)
}

func (f *fakeChoicePopup) Cancel() error {
	f.mu.Lock()
	cb := f.lastOnCancel
	f.active = false
	f.lastOnSubmit = nil
	f.lastOnCancel = nil
	f.mu.Unlock()
	if cb == nil {
		return nil
	}
	return cb()
}

func (f *fakeChoicePopup) Active() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active
}

func (f *fakeChoicePopup) chooseCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.chooseCalls
}

func (f *fakeChoicePopup) capturedOnSubmit() func(idx int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastOnSubmit
}

// TestChainedPrompterAdapter_PromptChoice_OutOfRangeRePushes drives the
// adapter's inner onSubmit closure directly with idx=-1 / idx=len, which
// the real *ui.ChoiceHelper.Submit would reject BEFORE invoking the
// closure. The defensive re-push at prompt_chain_adapter.go:onSubmit
// guard is only reachable via this path.
func TestChainedPrompterAdapter_PromptChoice_OutOfRangeRePushes(t *testing.T) {
	fake := &fakeChoicePopup{}
	exec := newUIExecutor()
	t.Cleanup(exec.shutdown)
	onUI := func(fn func() error) { exec.submit(fn) }
	adapter := &chainedPrompterAdapter{
		promptHelp: nil, // not exercised in this test
		choiceHelp: fake,
		onUIThread: onUI,
	}

	type res struct {
		v   string
		err error
	}
	done := make(chan res, 1)
	choices := []string{"postgres", "mysql"}
	go func() {
		v, err := adapter.PromptChoice(context.Background(), "Driver", "Pick", choices)
		done <- res{v, err}
	}()

	// Wait for the initial Choose to land.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if fake.chooseCallCount() >= 1 && fake.capturedOnSubmit() != nil {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if fake.chooseCallCount() != 1 {
		t.Fatalf("Choose call count after initial = %d; want 1", fake.chooseCallCount())
	}

	// Invoke onSubmit with an out-of-range idx on the UI lane (same
	// serialization the real helper uses). This must trigger a
	// re-push, NOT push a value onto the result channel.
	firstOnSubmit := fake.capturedOnSubmit()
	if firstOnSubmit == nil {
		t.Fatal("captured onSubmit is nil")
	}
	if err := exec.runOnUI(func() error { return firstOnSubmit(len(choices)) }); err != nil {
		t.Fatalf("onSubmit(out-of-range): %v", err)
	}

	// Wait for the second Choose (the re-push) to land.
	deadline = time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if fake.chooseCallCount() >= 2 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if fake.chooseCallCount() != 2 {
		t.Fatalf("Choose call count after out-of-range submit = %d; want 2 (re-push)", fake.chooseCallCount())
	}

	// PromptChoice must still be blocked.
	select {
	case r := <-done:
		t.Fatalf("PromptChoice returned prematurely: %+v", r)
	case <-time.After(20 * time.Millisecond):
		// expected
	}

	// Now drive a valid submit through the second captured closure.
	secondOnSubmit := fake.capturedOnSubmit()
	if secondOnSubmit == nil {
		t.Fatal("captured onSubmit after re-push is nil")
	}
	if err := exec.runOnUI(func() error { return secondOnSubmit(0) }); err != nil {
		t.Fatalf("onSubmit(0): %v", err)
	}
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("err = %v", r.err)
		}
		if r.v != "postgres" {
			t.Fatalf("value = %q; want postgres", r.v)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("PromptChoice did not return after valid submit")
	}
}

// TestChainedPrompterAdapter_PromptString_OverlappingCallsRejectedWithErrPromptBusy
// proves the busy-gate contract for PromptString: while a call is in
// flight, a second PromptString returns ("", ErrPromptBusy) immediately
// without touching the helper, and the in-flight call proceeds
// undisturbed. The helper's label stays on the first call's text — no
// clobber — and the first call still completes cleanly on submit.
func TestChainedPrompterAdapter_PromptString_OverlappingCallsRejectedWithErrPromptBusy(t *testing.T) {
	adapter, ph, _, _, exec := newTestAdapter(t)

	type res struct {
		v   string
		err error
	}
	done1 := make(chan res, 1)
	go func() {
		v, err := adapter.PromptString(context.Background(), "First", "F", nil)
		done1 <- res{v, err}
	}()

	if !waitForPromptActive(ph, 200*time.Millisecond) {
		t.Fatal("first prompt did not become active")
	}
	// Snapshot the live label so we can prove it never changes when
	// the second call is rejected.
	firstLabel := ph.Label()
	if !strings.Contains(firstLabel, "First") {
		t.Fatalf("first label = %q; want it to contain \"First\"", firstLabel)
	}

	// Second call MUST return ErrPromptBusy immediately. No helper
	// IO, no clobber, no goroutine wait.
	v, err := adapter.PromptString(context.Background(), "Second", "S", nil)
	if !errors.Is(err, ErrPromptBusy) {
		t.Fatalf("second call err = %v; want ErrPromptBusy", err)
	}
	if v != "" {
		t.Fatalf("second call value = %q; want empty string", v)
	}

	// Helper label still reflects the first call — no clobber.
	if got := ph.Label(); got != firstLabel {
		t.Fatalf("helper label after ErrPromptBusy = %q; want unchanged %q", got, firstLabel)
	}
	if !ph.Active() {
		t.Fatal("helper unexpectedly inactive after ErrPromptBusy reject")
	}

	// Complete the first call cleanly.
	if err := exec.runOnUI(func() error { return ph.Submit("ok") }); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	select {
	case r := <-done1:
		if r.err != nil {
			t.Fatalf("first call err = %v", r.err)
		}
		if r.v != "ok" {
			t.Fatalf("first call value = %q; want ok", r.v)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("first PromptString did not return after submit")
	}
}

// TestChainedPrompterAdapter_RejectsConcurrentCalls covers the
// PromptChoice side of the busy gate AND the cross-method case (a
// PromptString in flight blocks PromptChoice and vice versa) since both
// methods share the same `busy` flag.
func TestChainedPrompterAdapter_RejectsConcurrentCalls(t *testing.T) {
	adapter, ph, ch, _, exec := newTestAdapter(t)

	t.Run("PromptChoice rejects overlapping PromptChoice", func(t *testing.T) {
		type res struct {
			v   string
			err error
		}
		done := make(chan res, 1)
		go func() {
			v, err := adapter.PromptChoice(context.Background(), "Driver", "Pick", []string{"postgres", "mysql"})
			done <- res{v, err}
		}()
		if !waitForChoiceActive(ch, 200*time.Millisecond) {
			t.Fatal("first choice popup did not become active")
		}

		v, err := adapter.PromptChoice(context.Background(), "Other", "Pick", []string{"a", "b"})
		if !errors.Is(err, ErrPromptBusy) {
			t.Fatalf("overlapping PromptChoice err = %v; want ErrPromptBusy", err)
		}
		if v != "" {
			t.Fatalf("overlapping PromptChoice value = %q; want empty", v)
		}

		// Cancel the in-flight call to clean up before the next subtest.
		if err := exec.runOnUI(func() error { return ch.Cancel() }); err != nil {
			t.Fatalf("Cancel: %v", err)
		}
		<-done
	})

	t.Run("PromptChoice rejected while PromptString in flight", func(t *testing.T) {
		type res struct {
			v   string
			err error
		}
		done := make(chan res, 1)
		go func() {
			v, err := adapter.PromptString(context.Background(), "Name", "L", nil)
			done <- res{v, err}
		}()
		if !waitForPromptActive(ph, 200*time.Millisecond) {
			t.Fatal("prompt did not become active")
		}

		v, err := adapter.PromptChoice(context.Background(), "Driver", "Pick", []string{"a", "b"})
		if !errors.Is(err, ErrPromptBusy) {
			t.Fatalf("cross-method PromptChoice err = %v; want ErrPromptBusy", err)
		}
		if v != "" {
			t.Fatalf("cross-method PromptChoice value = %q; want empty", v)
		}

		// Clean up.
		if err := exec.runOnUI(func() error { return ph.Cancel() }); err != nil {
			t.Fatalf("Cancel: %v", err)
		}
		<-done
	})
}

// TestChainedPrompterAdapter_BusyClearsAfterCancel proves the deferred
// reset fires on the cancel path: after a cancelled PromptString
// returns, a sequential PromptString call succeeds (i.e. busy was
// cleared, not stuck).
func TestChainedPrompterAdapter_BusyClearsAfterCancel(t *testing.T) {
	adapter, ph, _, _, exec := newTestAdapter(t)

	type res struct {
		v   string
		err error
	}

	// First call: user cancels.
	done1 := make(chan res, 1)
	go func() {
		v, err := adapter.PromptString(context.Background(), "T1", "L1", nil)
		done1 <- res{v, err}
	}()
	if !waitForPromptActive(ph, 200*time.Millisecond) {
		t.Fatal("first prompt did not become active")
	}
	if err := exec.runOnUI(func() error { return ph.Cancel() }); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	select {
	case <-done1:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("first PromptString did not return after Cancel")
	}

	// Second call (sequential): must succeed — busy must have cleared.
	done2 := make(chan res, 1)
	go func() {
		v, err := adapter.PromptString(context.Background(), "T2", "L2", nil)
		done2 <- res{v, err}
	}()
	if !waitForPromptActive(ph, 200*time.Millisecond) {
		t.Fatal("second prompt did not become active — busy may not have cleared on cancel path")
	}
	if err := exec.runOnUI(func() error { return ph.Submit("ok") }); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	select {
	case r := <-done2:
		if r.err != nil {
			t.Fatalf("second call err = %v", r.err)
		}
		if r.v != "ok" {
			t.Fatalf("second call value = %q; want ok", r.v)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("second PromptString did not return")
	}
}

// fakePromptPopup is the prompt-side analogue of fakeChoicePopup: it
// captures the onSubmit/onCancel closures the adapter installs and
// counts Cancel invocations so a test can detect a spurious watcher
// Cancel after a successful Submit.
type fakePromptPopup struct {
	mu           sync.Mutex
	promptCalls  int
	cancelCalls  int
	lastLabel    string
	lastInitial  string
	lastOnSubmit func(value string) error
	lastOnCancel func() error
	active       bool
}

func (f *fakePromptPopup) Prompt(label, initial string, onSubmit func(value string) error, onCancel func() error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.promptCalls++
	f.lastLabel = label
	f.lastInitial = initial
	f.lastOnSubmit = onSubmit
	f.lastOnCancel = onCancel
	f.active = true
	return nil
}

func (f *fakePromptPopup) Submit(value string) error {
	f.mu.Lock()
	cb := f.lastOnSubmit
	f.active = false
	f.lastOnSubmit = nil
	f.lastOnCancel = nil
	f.mu.Unlock()
	if cb == nil {
		return nil
	}
	return cb(value)
}

func (f *fakePromptPopup) Cancel() error {
	f.mu.Lock()
	f.cancelCalls++
	cb := f.lastOnCancel
	f.active = false
	f.lastOnSubmit = nil
	f.lastOnCancel = nil
	f.mu.Unlock()
	if cb == nil {
		return nil
	}
	return cb()
}

func (f *fakePromptPopup) Active() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active
}

func (f *fakePromptPopup) cancelCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cancelCalls
}

func (f *fakePromptPopup) capturedOnSubmit() func(value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastOnSubmit
}

// TestChainedPrompterAdapter_PromptString_CtxCancelAfterSubmitIsSafe
// exercises the exact race the Active() guard prevents: the caller
// goroutine's select picks ctx.Done() over the (already-populated)
// result channel after a successful Submit. Without the guard, the
// watcher's helper.Cancel would fire on an inactive helper — which on
// the real *ui.PromptHelper performs an unconditional tree.Pop() and
// would corrupt the focus stack. We use a fake popup that counts
// Cancel invocations so the assertion is direct.
func TestChainedPrompterAdapter_PromptString_CtxCancelAfterSubmitIsSafe(t *testing.T) {
	fake := &fakePromptPopup{}
	exec := newUIExecutor()
	t.Cleanup(exec.shutdown)
	onUI := func(fn func() error) { exec.submit(fn) }
	adapter := &chainedPrompterAdapter{
		promptHelp: fake,
		choiceHelp: nil,
		onUIThread: onUI,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type res struct {
		v   string
		err error
	}
	done := make(chan res, 1)
	go func() {
		v, err := adapter.PromptString(ctx, "T", "L", nil)
		done <- res{v, err}
	}()

	// Wait for the initial Prompt to land + the watcher goroutine to
	// be parked on its select.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if fake.capturedOnSubmit() != nil {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if fake.capturedOnSubmit() == nil {
		t.Fatal("initial Prompt did not land")
	}

	// Drive Submit through the fake's Submit method (mirroring the
	// real *ui.PromptHelper.Submit path: clears active, then invokes
	// the captured onSubmit closure which sends to the result
	// channel). Then cancel ctx. The watcher's onUIThread Cancel is
	// scheduled AFTER the Submit closure has cleared active, so the
	// guard observes Active()==false and skips helper.Cancel.
	if err := exec.runOnUI(func() error { return fake.Submit("x") }); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	cancel()

	select {
	case r := <-done:
		// Either ctx.Err() or ("x", nil) is acceptable — the select
		// in the caller goroutine picks randomly. Both are valid
		// returns; the safety property we care about is "no spurious
		// Cancel" which we assert below.
		_ = r
	case <-time.After(500 * time.Millisecond):
		t.Fatal("PromptString did not return")
	}

	// The watcher goroutine MAY not have been scheduled yet when the
	// caller goroutine returned. Poll up to a generous deadline so
	// the watcher's onUIThread job (if any) has time to land. Then
	// drain via a sync barrier so any pending UI job completes before
	// we sample cancelCallCount.
	deadline2 := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline2) {
		time.Sleep(5 * time.Millisecond)
		// Force a barrier each iteration so any enqueued watcher
		// job drains. If the watcher already fired, cancelCalls
		// would now be non-zero (without the guard).
		_ = exec.runOnUI(func() error { return nil })
		if fake.cancelCallCount() > 0 {
			break
		}
	}

	if got := fake.cancelCallCount(); got != 0 {
		t.Fatalf("fake.Cancel call count = %d; want 0 (Active() guard must suppress watcher Cancel after Submit)", got)
	}
}

// Sanity: the adapter assertion compiles. Doubles as a smoke test that
// the constructor returns a value.
func TestChainedPrompterAdapter_CompileTimeAssertion(t *testing.T) {
	var _ data.ChainedPrompter = newChainedPrompterAdapter(nil, nil, func(fn func() error) { _ = fn() })
}
