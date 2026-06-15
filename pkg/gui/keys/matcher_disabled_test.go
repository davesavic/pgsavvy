package keys

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// recordingToastSpy captures every toast message the Matcher emits via
// its configured ToastFunc, with mutex-protected snapshot access.
type recordingToastSpy struct {
	mu   sync.Mutex
	msgs []string
}

func (r *recordingToastSpy) toast(m string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, m)
}

func (r *recordingToastSpy) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.msgs))
	copy(out, r.msgs)
	return out
}

// shortMatcherWithToaster mirrors shortMatcher (matcher_test.go) but
// installs a ToastFunc so disabled-dispatch toasts can be asserted.
func shortMatcherWithToaster(t *testing.T, ts *TrieSet, scope types.ContextKey, mode types.Mode, toaster ToastFunc) *Matcher {
	t.Helper()
	store := NewModeStore()
	store.Set(scope, mode)
	m, err := NewMatcher(ts, MatcherConfig{
		Modes:         store,
		TimeoutLen:    30 * time.Millisecond,
		TtimeoutLen:   10 * time.Millisecond,
		WhichKeyDelay: 0,
		Toaster:       toaster,
	})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	return m
}

// TestMatcher_DisabledStatic_EmitsToastAndSkipsHandler proves the AC:
// when a leaf's Command has DisabledReasonStatic set, the Handler is
// NOT invoked, Dispatched is returned, and the toaster sees a message
// containing the reason text.
func TestMatcher_DisabledStatic_EmitsToastAndSkipsHandler(t *testing.T) {
	var handlerCalls int
	var mu sync.Mutex
	cmd := &commands.Command{
		ID:                   "demo.bad",
		Description:          "Demo Bad",
		Handler:              func(commands.ExecCtx) error { mu.Lock(); handlerCalls++; mu.Unlock(); return nil },
		DisabledReasonStatic: "driver lacks live cancel",
	}
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeNormal, types.QUERY_EDITOR, []Key{keyOf('q')}, cmd},
	})
	spy := &recordingToastSpy{}
	m := shortMatcherWithToaster(t, ts, types.QUERY_EDITOR, types.ModeNormal, spy.toast)

	res, err := m.Dispatch(types.QUERY_EDITOR, keyOf('q'))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if res != Dispatched {
		t.Errorf("res = %v, want Dispatched (disabled commands still consume the key)", res)
	}

	mu.Lock()
	calls := handlerCalls
	mu.Unlock()
	if calls != 0 {
		t.Errorf("handler invoked %d times, want 0 (disabled commands must not run)", calls)
	}

	msgs := spy.snapshot()
	if len(msgs) != 1 {
		t.Fatalf("toaster received %d messages (%v), want 1", len(msgs), msgs)
	}
	if !strings.Contains(msgs[0], "driver lacks live cancel") {
		t.Errorf("toast %q does not contain the disable reason", msgs[0])
	}
	if !strings.Contains(msgs[0], "Demo Bad") {
		t.Errorf("toast %q does not name the action (Description)", msgs[0])
	}
}

// TestMatcher_DisabledDynamic_PredicateConsulted confirms a non-nil
// GetDisabled wins over the static field and its reason reaches the
// toaster.
func TestMatcher_DisabledDynamic_PredicateConsulted(t *testing.T) {
	var handlerCalls int
	var mu sync.Mutex
	cmd := &commands.Command{
		ID:          "demo.dynamic",
		Description: "Demo Dynamic",
		Handler:     func(commands.ExecCtx) error { mu.Lock(); handlerCalls++; mu.Unlock(); return nil },
		GetDisabled: func(commands.ExecCtx) (string, bool) {
			return "dynamically refused", true
		},
		// Static field intentionally set to a different value — must
		// NOT appear in the toast because GetDisabled takes precedence.
		DisabledReasonStatic: "stale static reason",
	}
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeNormal, types.QUERY_EDITOR, []Key{keyOf('q')}, cmd},
	})
	spy := &recordingToastSpy{}
	m := shortMatcherWithToaster(t, ts, types.QUERY_EDITOR, types.ModeNormal, spy.toast)

	res, _ := m.Dispatch(types.QUERY_EDITOR, keyOf('q'))
	if res != Dispatched {
		t.Errorf("res = %v, want Dispatched", res)
	}
	mu.Lock()
	calls := handlerCalls
	mu.Unlock()
	if calls != 0 {
		t.Errorf("handler invoked %d times, want 0", calls)
	}
	msgs := spy.snapshot()
	if len(msgs) != 1 {
		t.Fatalf("toast count = %d (%v), want 1", len(msgs), msgs)
	}
	if !strings.Contains(msgs[0], "dynamically refused") {
		t.Errorf("toast %q missing dynamic reason", msgs[0])
	}
	if strings.Contains(msgs[0], "stale static reason") {
		t.Errorf("toast %q leaked the static reason (GetDisabled must win)", msgs[0])
	}
}

// TestMatcher_DisabledEmptyReason_FallsBackToGeneric proves the
// edge-case AC: an empty reason string still produces a toast, with
// a generic "disabled" fallback.
func TestMatcher_DisabledEmptyReason_FallsBackToGeneric(t *testing.T) {
	cmd := &commands.Command{
		ID:          "demo.empty",
		Description: "Demo Empty",
		Handler:     func(commands.ExecCtx) error { return nil },
		GetDisabled: func(commands.ExecCtx) (string, bool) {
			return "", true
		},
	}
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeNormal, types.QUERY_EDITOR, []Key{keyOf('q')}, cmd},
	})
	spy := &recordingToastSpy{}
	m := shortMatcherWithToaster(t, ts, types.QUERY_EDITOR, types.ModeNormal, spy.toast)

	_, _ = m.Dispatch(types.QUERY_EDITOR, keyOf('q'))
	msgs := spy.snapshot()
	if len(msgs) != 1 {
		t.Fatalf("toast count = %d (%v), want 1", len(msgs), msgs)
	}
	if !strings.Contains(msgs[0], "disabled") {
		t.Errorf("toast %q missing generic 'disabled' fallback", msgs[0])
	}
}

// TestMatcher_EnabledBindingsUnchanged is the regression guard: a
// Command with no Disabled fields dispatches its Handler normally and
// emits no toast.
func TestMatcher_EnabledBindingsUnchanged(t *testing.T) {
	var handlerCalls int
	var mu sync.Mutex
	cmd := &commands.Command{
		ID:      "demo.ok",
		Handler: func(commands.ExecCtx) error { mu.Lock(); handlerCalls++; mu.Unlock(); return nil },
	}
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeNormal, types.QUERY_EDITOR, []Key{keyOf('j')}, cmd},
	})
	spy := &recordingToastSpy{}
	m := shortMatcherWithToaster(t, ts, types.QUERY_EDITOR, types.ModeNormal, spy.toast)

	res, _ := m.Dispatch(types.QUERY_EDITOR, keyOf('j'))
	if res != Dispatched {
		t.Errorf("res = %v, want Dispatched", res)
	}
	mu.Lock()
	calls := handlerCalls
	mu.Unlock()
	if calls != 1 {
		t.Errorf("handler invoked %d times, want 1", calls)
	}
	if msgs := spy.snapshot(); len(msgs) != 0 {
		t.Errorf("toaster received %d messages (%v) for an enabled binding, want 0", len(msgs), msgs)
	}
}

// TestMatcher_DisabledPanicRecovered confirms that a GetDisabled that
// panics is contained: the Matcher reports Dispatched, no handler is
// run, and the toast carries the canonical "<internal error>" reason.
func TestMatcher_DisabledPanicRecovered(t *testing.T) {
	var handlerCalls int
	var mu sync.Mutex
	cmd := &commands.Command{
		ID:          "demo.panic",
		Description: "Demo Panic",
		Handler:     func(commands.ExecCtx) error { mu.Lock(); handlerCalls++; mu.Unlock(); return nil },
		GetDisabled: func(commands.ExecCtx) (string, bool) { panic("boom") },
	}
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeNormal, types.QUERY_EDITOR, []Key{keyOf('q')}, cmd},
	})
	spy := &recordingToastSpy{}
	m := shortMatcherWithToaster(t, ts, types.QUERY_EDITOR, types.ModeNormal, spy.toast)

	res, err := m.Dispatch(types.QUERY_EDITOR, keyOf('q'))
	if err != nil {
		t.Fatalf("Dispatch: %v (panic must not propagate)", err)
	}
	if res != Dispatched {
		t.Errorf("res = %v, want Dispatched", res)
	}
	mu.Lock()
	calls := handlerCalls
	mu.Unlock()
	if calls != 0 {
		t.Errorf("handler invoked after panicking predicate; want 0, got %d", calls)
	}
	msgs := spy.snapshot()
	if len(msgs) != 1 {
		t.Fatalf("toast count = %d (%v), want 1", len(msgs), msgs)
	}
	if !strings.Contains(msgs[0], "<internal error>") {
		t.Errorf("toast %q missing '<internal error>' canonical reason", msgs[0])
	}
}
