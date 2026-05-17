package keys_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/gui"
	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

const shortTTL = 30 * time.Millisecond

// waitUntil polls cond up to 200ms; fails the test on timeout.
func waitUntil(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("waitUntil timeout: %s", msg)
}

func TestArmExpiresAfterTTL(t *testing.T) {
	o := keys.NewOneshotArm(shortTTL)
	if err := o.Arm("<space>", map[rune]keys.Handler{'q': func() error { return nil }}, "global"); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	if !o.IsArmed() {
		t.Fatalf("IsArmed = false immediately after Arm; want true")
	}
	waitUntil(t, func() bool { return !o.IsArmed() }, "arm should auto-clear after TTL")
}

func TestArmCancelledByContextSwitch(t *testing.T) {
	o := keys.NewOneshotArm(time.Second) // long ttl so only the swap hook can cancel
	tree := gui.NewContextTree()
	tree.RegisterSwapHook(o.Cancel)

	if err := o.Arm("<space>", map[rune]keys.Handler{'q': func() error { return nil }}, "global"); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	if !o.IsArmed() {
		t.Fatalf("precondition: arm should be live")
	}

	if err := tree.Push(&minimalCtx{key: types.CONNECTIONS, kind: types.SIDE_CONTEXT}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if o.IsArmed() {
		t.Fatalf("IsArmed = true after context switch; want false (swap hook should cancel)")
	}
}

func TestSecondArmCancelsFirst(t *testing.T) {
	o := keys.NewOneshotArm(time.Second)
	called := 0
	first := map[rune]keys.Handler{'a': func() error { called++; return nil }}
	second := map[rune]keys.Handler{'b': func() error { return nil }}

	if err := o.Arm("<space>", first, "global"); err != nil {
		t.Fatalf("Arm 1: %v", err)
	}
	if err := o.Arm("<space>", second, "global"); err != nil {
		t.Fatalf("Arm 2: %v", err)
	}
	matched, err := o.Dispatch('a')
	if err != nil {
		t.Fatalf("Dispatch('a'): %v", err)
	}
	if matched {
		t.Fatalf("Dispatch('a') matched after second Arm; want false")
	}
	if called != 0 {
		t.Fatalf("first arm handler invoked %d times; want 0", called)
	}
}

func TestArmedSuffixDispatches(t *testing.T) {
	o := keys.NewOneshotArm(time.Second)
	hits := 0
	if err := o.Arm("<space>", map[rune]keys.Handler{'H': func() error { hits++; return nil }}, "schemas"); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	matched, err := o.Dispatch('H')
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !matched {
		t.Fatalf("matched = false; want true")
	}
	if hits != 1 {
		t.Fatalf("handler hits = %d; want 1", hits)
	}
	if o.IsArmed() {
		t.Fatalf("arm survived a matching dispatch; want cleared")
	}
}

func TestUnknownSuffixCancelsSilently(t *testing.T) {
	o := keys.NewOneshotArm(time.Second)
	if err := o.Arm("<space>", map[rune]keys.Handler{'H': func() error { return nil }}, "schemas"); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	matched, err := o.Dispatch('x')
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if matched {
		t.Fatalf("matched = true for unknown suffix; want false")
	}
	if o.IsArmed() {
		t.Fatalf("unknown suffix did not clear the arm")
	}
}

func TestArmCancelledByMouseEvent(t *testing.T) {
	// A mouse event is just a Cancel() call from the mouse helper's wrapper.
	o := keys.NewOneshotArm(time.Second)
	_ = o.Arm("<space>", map[rune]keys.Handler{'H': func() error { return nil }}, "schemas")
	if !o.IsArmed() {
		t.Fatalf("precondition: arm should be live")
	}
	o.Cancel()
	if o.IsArmed() {
		t.Fatalf("Cancel() did not clear the arm")
	}
}

func TestDispatchPropagatesHandlerError(t *testing.T) {
	o := keys.NewOneshotArm(time.Second)
	want := errors.New("boom")
	_ = o.Arm("<space>", map[rune]keys.Handler{'q': func() error { return want }}, "global")
	matched, err := o.Dispatch('q')
	if !matched {
		t.Fatalf("matched = false; want true")
	}
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func TestDispatchNoArmIsNoop(t *testing.T) {
	o := keys.NewOneshotArm(time.Second)
	matched, err := o.Dispatch('q')
	if matched || err != nil {
		t.Fatalf("Dispatch with no arm: matched=%v err=%v; want false,nil", matched, err)
	}
}

func TestNewOneshotArmDefaultsTTL(t *testing.T) {
	o := keys.NewOneshotArm(0)
	if o == nil {
		t.Fatalf("nil helper")
	}
	if err := o.Arm("<space>", map[rune]keys.Handler{'q': func() error { return nil }}, "global"); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	if matched, _ := o.Dispatch('q'); !matched {
		t.Fatalf("default-ttl arm not dispatchable")
	}
}

func TestArmConcurrentArmRace(t *testing.T) {
	// Stress: rapidly Arm/Dispatch from N goroutines.
	o := keys.NewOneshotArm(time.Second)
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			_ = o.Arm("<space>", map[rune]keys.Handler{'q': func() error { return nil }}, "global")
			_, _ = o.Dispatch('q')
		})
	}
	wg.Wait()
	if o.IsArmed() {
		t.Fatalf("IsArmed at quiesce; want false")
	}
}

// minimalCtx is a no-op IBaseContext used to exercise ContextTree.Push.
type minimalCtx struct {
	key  types.ContextKey
	kind types.ContextKind
}

func (m *minimalCtx) GetKey() types.ContextKey                      { return m.key }
func (m *minimalCtx) GetViewName() string                           { return string(m.key) }
func (m *minimalCtx) GetWindowName() string                         { return string(m.key) }
func (m *minimalCtx) GetKind() types.ContextKind                    { return m.kind }
func (m *minimalCtx) HandleFocus(_ types.OnFocusOpts) error         { return nil }
func (m *minimalCtx) HandleFocusLost(_ types.OnFocusLostOpts) error { return nil }
func (m *minimalCtx) HandleRender() error                           { return nil }
func (m *minimalCtx) HandleRenderToMain() error                     { return nil }
func (m *minimalCtx) HandleQuit() error                             { return nil }
func (m *minimalCtx) NeedsRerenderOnHeightChange() bool             { return false }
func (m *minimalCtx) NeedsRerenderOnWidthChange() bool              { return false }
func (m *minimalCtx) AddKeybindingsFn(_ types.KeybindingsFn)        {}
func (m *minimalCtx) GetKeybindings(_ types.KeybindingsOpts) []*types.KeyBinding {
	return nil
}

func (m *minimalCtx) GetMouseKeybindings(_ types.KeybindingsOpts) []types.MouseBinding {
	return nil
}
