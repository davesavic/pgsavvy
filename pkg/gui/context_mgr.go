package gui

import (
	"errors"
	"log/slog"
	"slices"
	"sync"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/logs"
)

// ErrPopAtBottom is returned by ContextTree.Pop when the stack contains
// a single context (the root cannot be popped).
var ErrPopAtBottom = errors.New("gui: cannot pop the root context")

// ContextTree serializes stack access with an internal mutex: mutations
// (Push/Pop/Replace) happen on the MainLoop, but background goroutines
// (the spinner drain reading Current via promptOnTop) race them, and test
// drivers execute Update callbacks inline on the calling goroutine.
// Lifecycle hooks (HandleFocus/HandleFocusLost) and swap hooks always run
// AFTER the mutex is released — they may re-enter the tree (e.g. the
// result-tabs swap hook calls Current).
//
// Push/Pop/Replace semantics mirror DESIGN.md §8 lines 596-604:
//   - Pushing a SIDE_CONTEXT wipes the stack and installs the new
//     context as root.
//   - Pushing a MAIN_CONTEXT removes any existing MAIN_CONTEXT from the
//     stack before pushing on top (popups above main are preserved).
//   - Pushing a TEMPORARY_POPUP first pops a top-of-stack
//     TEMPORARY_POPUP if present.
//   - Pushing PERSISTENT_POPUP, EXTRAS_CONTEXT, GLOBAL_CONTEXT, or
//     DISPLAY_CONTEXT just appends without disturbing the rest of the
//     stack.
//   - Pushing the same key already on top is a no-op (no lifecycle hooks
//     fire).
//   - Replace swaps the top entry without firing pop/push lifecycle
//     hooks.
//
// SwapHooks are functions invoked by Push/Pop/Replace whenever the stack
// composition changes (specifically: after a successful Push that did not
// short-circuit on duplicate-top, after Pop, and after Replace). Hooks
// receive no arguments and are intended for cross-cutting cancellation
// concerns (e.g. keys.OneshotArm cancels any pending arm on context
// switch). Keeps the OneshotArm cancel path
// simple without polling on every keypress.
type ContextTree struct {
	// mu guards stack, swapHooks, and evictedMain. It is never held
	// while lifecycle or swap hooks run (they may re-enter the tree).
	mu         sync.Mutex
	stack      []types.IBaseContext
	swapHooks  []func()
	sessionLog *slog.Logger
	// evictedMain holds the MAIN_CONTEXT most recently displaced by
	// removeMain (nil when the displacing push found no main to evict).
	// The connection-manager close path consumes it via TakeEvictedMain to
	// restore the pane the modal covered.
	evictedMain types.IBaseContext
}

// NewContextTree returns an empty ContextTree. Callers are expected to
// Push a root context immediately; Pop refuses to drop the final entry.
func NewContextTree() *ContextTree {
	return &ContextTree{}
}

// SetSessionLog installs the per-session logger used by Push/Pop/
// Replace/wipeStack/removeMain to emit cat=input ctx_* events. nil
// disables emission. Wired by the orchestrator at bootstrap; the
// nil-default keeps test fixtures that never call this method silent.
func (t *ContextTree) SetSessionLog(l *slog.Logger) {
	t.sessionLog = l
}

// kindLabel renders a ContextKind as a short stable string for log
// events. Falls back to a kind(<int>) form for unknown values so a new
// kind never blows up logging.
func kindLabel(k types.ContextKind) string {
	switch k {
	case types.SIDE_CONTEXT:
		return "side"
	case types.MAIN_CONTEXT:
		return "main"
	case types.PERSISTENT_POPUP:
		return "persistent_popup"
	case types.TEMPORARY_POPUP:
		return "temporary_popup"
	case types.EXTRAS_CONTEXT:
		return "extras"
	case types.GLOBAL_CONTEXT:
		return "global"
	case types.DISPLAY_CONTEXT:
		return "display"
	case types.STUB:
		return "stub"
	default:
		return "kind"
	}
}

// Push installs c on top of the stack per the kind-specific rules
// documented on ContextTree. Returns nil on success.
func (t *ContextTree) Push(c types.IBaseContext) error {
	// Contexts whose HandleFocusLost must fire for this push (wiped
	// stack entries, an evicted MAIN_CONTEXT, a displaced
	// TEMPORARY_POPUP), collected bottom-to-top. They fire after the
	// mutex is released, in the same relative order the unguarded code
	// fired them.
	var lost []types.IBaseContext
	t.mu.Lock()
	if top := t.peekLocked(); top != nil && top.GetKey() == c.GetKey() {
		t.mu.Unlock()
		return nil
	}

	depthBefore := len(t.stack)
	switch c.GetKind() {
	case types.SIDE_CONTEXT:
		lost = t.wipeStackLocked()
		t.stack = append(t.stack, c)
	case types.MAIN_CONTEXT:
		if evicted := t.removeMainLocked(); evicted != nil {
			lost = append(lost, evicted)
		}
		t.stack = append(t.stack, c)
	case types.TEMPORARY_POPUP:
		if top := t.peekLocked(); top != nil && top.GetKind() == types.TEMPORARY_POPUP {
			if popped := t.popOneLocked(); popped != nil {
				lost = append(lost, popped)
			}
		}
		t.stack = append(t.stack, c)
	default:
		t.stack = append(t.stack, c)
	}
	depthAfter := len(t.stack)
	t.mu.Unlock()

	for _, v := range slices.Backward(lost) {
		_ = v.HandleFocusLost(types.OnFocusLostOpts{})
	}

	logs.Event(t.sessionLog, "input", "ctx_push",
		slog.String("key", string(c.GetKey())),
		slog.String("kind", kindLabel(c.GetKind())),
		slog.Int("stack_depth_before", depthBefore),
		slog.Int("stack_depth_after", depthAfter),
	)

	if err := c.HandleFocus(types.OnFocusOpts{NewContextKey: c.GetKey()}); err != nil {
		return err
	}
	t.fireSwapHooks()
	return nil
}

// Pop removes the top context, fires HandleFocusLost on it and
// HandleFocus on the new top. Returns ErrPopAtBottom if the stack has
// only the root entry.
func (t *ContextTree) Pop() error {
	t.mu.Lock()
	if len(t.stack) <= 1 {
		t.mu.Unlock()
		return ErrPopAtBottom
	}
	depthBefore := len(t.stack)
	popped := t.stack[len(t.stack)-1]
	t.stack = t.stack[:len(t.stack)-1]
	newTop := t.stack[len(t.stack)-1]
	depthAfter := len(t.stack)
	t.mu.Unlock()
	logs.Event(t.sessionLog, "input", "ctx_pop",
		slog.String("key", string(popped.GetKey())),
		slog.String("kind", kindLabel(popped.GetKind())),
		slog.Int("stack_depth_before", depthBefore),
		slog.Int("stack_depth_after", depthAfter),
	)
	if err := popped.HandleFocusLost(types.OnFocusLostOpts{NewContextKey: newTop.GetKey()}); err != nil {
		return err
	}
	if err := newTop.HandleFocus(types.OnFocusOpts{NewContextKey: newTop.GetKey()}); err != nil {
		return err
	}
	t.fireSwapHooks()
	return nil
}

// PopIfTop pops the stack only when the top context's key matches key.
// If the top is something else (e.g. a dialog pushed by the ex handler),
// the pop is skipped and nil is returned. This prevents a deferred pop
// from accidentally dismissing a context pushed during command execution.
func (t *ContextTree) PopIfTop(key types.ContextKey) error {
	t.mu.Lock()
	top := t.peekLocked()
	skip := top == nil || top.GetKey() != key
	t.mu.Unlock()
	if skip {
		return nil
	}
	return t.Pop()
}

// Replace swaps the top entry with c without firing pop/push lifecycle
// hooks. Used for tab switches within a single window slot.
func (t *ContextTree) Replace(c types.IBaseContext) error {
	t.mu.Lock()
	if len(t.stack) == 0 {
		depthBefore := 0
		t.stack = append(t.stack, c)
		depthAfter := len(t.stack)
		t.mu.Unlock()
		logs.Event(t.sessionLog, "input", "ctx_replace",
			slog.String("key", string(c.GetKey())),
			slog.String("kind", kindLabel(c.GetKind())),
			slog.Int("stack_depth_before", depthBefore),
			slog.Int("stack_depth_after", depthAfter),
		)
		t.fireSwapHooks()
		return nil
	}
	depthBefore := len(t.stack)
	t.stack[len(t.stack)-1] = c
	depthAfter := len(t.stack)
	t.mu.Unlock()
	logs.Event(t.sessionLog, "input", "ctx_replace",
		slog.String("key", string(c.GetKey())),
		slog.String("kind", kindLabel(c.GetKind())),
		slog.Int("stack_depth_before", depthBefore),
		slog.Int("stack_depth_after", depthAfter),
	)
	t.fireSwapHooks()
	return nil
}

// RegisterSwapHook appends fn to the list of callbacks invoked when the
// stack composition changes (Push that actually pushed, Pop, Replace). A
// nil fn is silently dropped. Hooks are called in registration order on
// the same goroutine that performed the mutation (the MainLoop in
// production). Used by keys.OneshotArm to cancel any pending arm when
// the active context switches.
func (t *ContextTree) RegisterSwapHook(fn func()) {
	if fn == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.swapHooks = append(t.swapHooks, fn)
}

// fireSwapHooks invokes every registered swap hook in registration
// order. The hook list is snapshotted under the mutex and the hooks run
// with the mutex released — hooks may re-enter the tree (e.g. the
// result-tabs swap hook calls Current). Hooks panicking is treated as a
// programming error and will propagate; that matches the rest of
// pkg/gui's MainLoop-only contract.
func (t *ContextTree) fireSwapHooks() {
	t.mu.Lock()
	hooks := make([]func(), len(t.swapHooks))
	copy(hooks, t.swapHooks)
	t.mu.Unlock()
	for _, fn := range hooks {
		fn()
	}
}

// Current returns the top context, or nil if the stack is empty.
func (t *ContextTree) Current() types.IBaseContext {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.peekLocked()
}

// CurrentKind returns the top context's kind. The zero value
// (SIDE_CONTEXT) is returned when the stack is empty; callers needing to
// distinguish must consult Current().
func (t *ContextTree) CurrentKind() types.ContextKind {
	t.mu.Lock()
	defer t.mu.Unlock()
	top := t.peekLocked()
	if top == nil {
		return types.SIDE_CONTEXT
	}
	return top.GetKind()
}

// Stack returns a copy of the current stack from bottom to top.
func (t *ContextTree) Stack() []types.IBaseContext {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]types.IBaseContext, len(t.stack))
	copy(out, t.stack)
	return out
}

// peekLocked returns the top context; the caller must hold t.mu.
func (t *ContextTree) peekLocked() types.IBaseContext {
	if len(t.stack) == 0 {
		return nil
	}
	return t.stack[len(t.stack)-1]
}

// wipeStackLocked clears the stack; the caller must hold t.mu. Returns
// the wiped contexts bottom-to-top; the caller fires HandleFocusLost on
// them (top-to-bottom) after releasing the mutex — lifecycle hooks must
// not run under t.mu because they may re-enter the tree.
func (t *ContextTree) wipeStackLocked() []types.IBaseContext {
	depthBefore := len(t.stack)
	wiped := make([]types.IBaseContext, len(t.stack))
	copy(wiped, t.stack)
	t.stack = t.stack[:0]
	logs.Event(t.sessionLog, "input", "ctx_wipe",
		slog.String("key", ""),
		slog.String("kind", ""),
		slog.Int("stack_depth_before", depthBefore),
		slog.Int("stack_depth_after", len(t.stack)),
	)
	return wiped
}

// removeMainLocked drops the first MAIN_CONTEXT found in the stack (there
// is at most one); the caller must hold t.mu. The removed context is
// recorded in evictedMain (cleared to nil when no main is present) so the
// connection-manager close path can restore the covered pane. Returns the
// evicted context (nil when none); the caller fires its HandleFocusLost
// after releasing the mutex.
func (t *ContextTree) removeMainLocked() types.IBaseContext {
	t.evictedMain = nil
	for i, c := range t.stack {
		if c.GetKind() == types.MAIN_CONTEXT {
			depthBefore := len(t.stack)
			t.stack = append(t.stack[:i], t.stack[i+1:]...)
			t.evictedMain = c
			logs.Event(t.sessionLog, "input", "ctx_remove_main",
				slog.String("key", string(c.GetKey())),
				slog.String("kind", kindLabel(c.GetKind())),
				slog.Int("stack_depth_before", depthBefore),
				slog.Int("stack_depth_after", len(t.stack)),
			)
			return c
		}
	}
	return nil
}

// TakeEvictedMain returns and clears the MAIN_CONTEXT most recently
// displaced by removeMain (nil when none). The connection-manager close
// path uses it to re-push the pane the modal covered so focus returns
// where the user was.
func (t *ContextTree) TakeEvictedMain() types.IBaseContext {
	t.mu.Lock()
	defer t.mu.Unlock()
	c := t.evictedMain
	t.evictedMain = nil
	return c
}

// popOneLocked removes the top entry; the caller must hold t.mu. Returns
// the removed context (nil when the stack is empty); the caller fires its
// HandleFocusLost after releasing the mutex.
func (t *ContextTree) popOneLocked() types.IBaseContext {
	if len(t.stack) == 0 {
		return nil
	}
	popped := t.stack[len(t.stack)-1]
	t.stack = t.stack[:len(t.stack)-1]
	return popped
}
