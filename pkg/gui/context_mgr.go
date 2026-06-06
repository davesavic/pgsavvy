package gui

import (
	"errors"
	"log/slog"
	"slices"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/logs"
)

// ErrPopAtBottom is returned by ContextTree.Pop when the stack contains
// a single context (the root cannot be popped).
var ErrPopAtBottom = errors.New("gui: cannot pop the root context")

// ContextTree is NOT goroutine-safe; all Push/Pop/Replace/Current calls
// happen on the MainLoop. Background goroutines marshal via
// driver.Update.
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
// switch). Added per dbsavvy-zro T7b — keeps the OneshotArm cancel path
// simple without polling on every keypress.
type ContextTree struct {
	stack      []types.IBaseContext
	swapHooks  []func()
	sessionLog *slog.Logger
	// evictedMain holds the MAIN_CONTEXT most recently displaced by
	// removeMain (nil when the displacing push found no main to evict).
	// The connection-manager close path consumes it via TakeEvictedMain to
	// restore the pane the modal covered (dbsavvy-yea).
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
	if top := t.peek(); top != nil && top.GetKey() == c.GetKey() {
		return nil
	}

	depthBefore := len(t.stack)
	switch c.GetKind() {
	case types.SIDE_CONTEXT:
		t.wipeStack()
		t.stack = append(t.stack, c)
	case types.MAIN_CONTEXT:
		t.removeMain()
		t.stack = append(t.stack, c)
	case types.TEMPORARY_POPUP:
		if top := t.peek(); top != nil && top.GetKind() == types.TEMPORARY_POPUP {
			t.popOne()
		}
		t.stack = append(t.stack, c)
	default:
		t.stack = append(t.stack, c)
	}

	logs.Event(t.sessionLog, "input", "ctx_push",
		slog.String("key", string(c.GetKey())),
		slog.String("kind", kindLabel(c.GetKind())),
		slog.Int("stack_depth_before", depthBefore),
		slog.Int("stack_depth_after", len(t.stack)),
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
	if len(t.stack) <= 1 {
		return ErrPopAtBottom
	}
	depthBefore := len(t.stack)
	popped := t.stack[len(t.stack)-1]
	t.stack = t.stack[:len(t.stack)-1]
	newTop := t.stack[len(t.stack)-1]
	logs.Event(t.sessionLog, "input", "ctx_pop",
		slog.String("key", string(popped.GetKey())),
		slog.String("kind", kindLabel(popped.GetKind())),
		slog.Int("stack_depth_before", depthBefore),
		slog.Int("stack_depth_after", len(t.stack)),
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
	top := t.peek()
	if top == nil || top.GetKey() != key {
		return nil
	}
	return t.Pop()
}

// Replace swaps the top entry with c without firing pop/push lifecycle
// hooks. Used for tab switches within a single window slot.
func (t *ContextTree) Replace(c types.IBaseContext) error {
	if len(t.stack) == 0 {
		depthBefore := 0
		t.stack = append(t.stack, c)
		logs.Event(t.sessionLog, "input", "ctx_replace",
			slog.String("key", string(c.GetKey())),
			slog.String("kind", kindLabel(c.GetKind())),
			slog.Int("stack_depth_before", depthBefore),
			slog.Int("stack_depth_after", len(t.stack)),
		)
		t.fireSwapHooks()
		return nil
	}
	depthBefore := len(t.stack)
	t.stack[len(t.stack)-1] = c
	logs.Event(t.sessionLog, "input", "ctx_replace",
		slog.String("key", string(c.GetKey())),
		slog.String("kind", kindLabel(c.GetKind())),
		slog.Int("stack_depth_before", depthBefore),
		slog.Int("stack_depth_after", len(t.stack)),
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
	t.swapHooks = append(t.swapHooks, fn)
}

// fireSwapHooks invokes every registered swap hook in registration
// order. Hooks panicking is treated as a programming error and will
// propagate; that matches the rest of pkg/gui's MainLoop-only contract.
func (t *ContextTree) fireSwapHooks() {
	for _, fn := range t.swapHooks {
		fn()
	}
}

// Current returns the top context, or nil if the stack is empty.
func (t *ContextTree) Current() types.IBaseContext {
	return t.peek()
}

// CurrentKind returns the top context's kind. The zero value
// (SIDE_CONTEXT) is returned when the stack is empty; callers needing to
// distinguish must consult Current().
func (t *ContextTree) CurrentKind() types.ContextKind {
	top := t.peek()
	if top == nil {
		return types.SIDE_CONTEXT
	}
	return top.GetKind()
}

// Stack returns a copy of the current stack from bottom to top.
func (t *ContextTree) Stack() []types.IBaseContext {
	out := make([]types.IBaseContext, len(t.stack))
	copy(out, t.stack)
	return out
}

func (t *ContextTree) peek() types.IBaseContext {
	if len(t.stack) == 0 {
		return nil
	}
	return t.stack[len(t.stack)-1]
}

// wipeStack pops every context, firing HandleFocusLost from top to
// bottom. Errors from individual hooks are ignored so the stack always
// ends up empty.
func (t *ContextTree) wipeStack() {
	depthBefore := len(t.stack)
	for _, v := range slices.Backward(t.stack) {
		_ = v.HandleFocusLost(types.OnFocusLostOpts{})
	}
	t.stack = t.stack[:0]
	logs.Event(t.sessionLog, "input", "ctx_wipe",
		slog.String("key", ""),
		slog.String("kind", ""),
		slog.Int("stack_depth_before", depthBefore),
		slog.Int("stack_depth_after", len(t.stack)),
	)
}

// removeMain drops the first MAIN_CONTEXT found in the stack (there is
// at most one), firing HandleFocusLost on it. The removed context is
// recorded in evictedMain (cleared to nil when no main is present) so
// the connection-manager close path can restore the covered pane.
func (t *ContextTree) removeMain() {
	t.evictedMain = nil
	for i, c := range t.stack {
		if c.GetKind() == types.MAIN_CONTEXT {
			depthBefore := len(t.stack)
			_ = c.HandleFocusLost(types.OnFocusLostOpts{})
			t.stack = append(t.stack[:i], t.stack[i+1:]...)
			t.evictedMain = c
			logs.Event(t.sessionLog, "input", "ctx_remove_main",
				slog.String("key", string(c.GetKey())),
				slog.String("kind", kindLabel(c.GetKind())),
				slog.Int("stack_depth_before", depthBefore),
				slog.Int("stack_depth_after", len(t.stack)),
			)
			return
		}
	}
}

// TakeEvictedMain returns and clears the MAIN_CONTEXT most recently
// displaced by removeMain (nil when none). The connection-manager close
// path uses it to re-push the pane the modal covered so focus returns
// where the user was (dbsavvy-yea).
func (t *ContextTree) TakeEvictedMain() types.IBaseContext {
	c := t.evictedMain
	t.evictedMain = nil
	return c
}

// popOne removes the top entry, firing HandleFocusLost on it.
func (t *ContextTree) popOne() {
	if len(t.stack) == 0 {
		return
	}
	popped := t.stack[len(t.stack)-1]
	t.stack = t.stack[:len(t.stack)-1]
	_ = popped.HandleFocusLost(types.OnFocusLostOpts{})
}
