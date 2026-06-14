package context

import (
	"github.com/davesavic/dbsavvy/pkg/gui/editor"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// QueryEditorContext is the real top-right MAIN_CONTEXT pane that
// hosts the vim-style SQL editor. It is a live BaseContext-embedding
// type so subsequent child tasks have a stable handoff:
// they read/write the *editor.Buffer + *editor.RepeatStore exposed
// through Buffer() / Repeat() accessors.
//
// Focus wiring:
//   - HandleFocus flips ModeStore[QUERY_EDITOR] to ModeNormal so the
//     Matcher routes printable runes through Normal-mode dispatch
//     before Insert-mode entries (i/a/o/...) take over.
//   - HandleFocusLost is the inverse: clear Visual selection, reset
//     ModeStore, cancel any half-built chord in the Matcher, and
//     dispatch a buffer save when Dirty. The order of the four
//     operations is contractual.
type QueryEditorContext struct {
	BaseContext
	deps    depsAlias
	modes   types.ModeSetter
	matcher types.MatcherCanceller
	buf     *editor.Buffer
	repeat  *editor.RepeatStore
}

// Compile-time assertion that the live type satisfies the lifecycle
// contract. Keeps refactors honest without paying a runtime cost.
var _ types.IBaseContext = (*QueryEditorContext)(nil)

// NewQueryEditorContext constructs the live QUERY_EDITOR context.
// base supplies key/view/window/kind; deps is the standard context
// dependency bag (carried for parity with sibling constructors, which
// consume GuiDriver from it). modes and matcher may be
// nil in test wiring; every focus hook nil-checks before calling.
//
// The *editor.Buffer / *editor.RepeatStore returned by Buffer() /
// Repeat() are always non-nil — Buffer uses editor.NewBuffer so
// Jumps is initialised before any motion handler can call
// buf.Jumps.Push.
func NewQueryEditorContext(
	base BaseContext,
	deps depsAlias,
	modes types.ModeSetter,
	matcher types.MatcherCanceller,
) *QueryEditorContext {
	return &QueryEditorContext{
		BaseContext: base,
		deps:        deps,
		modes:       modes,
		matcher:     matcher,
		buf:         editor.NewBuffer(),
		repeat:      &editor.RepeatStore{},
	}
}

// Buffer returns the canonical text/cursor/undo state for this query
// editor pane. Always non-nil.
func (c *QueryEditorContext) Buffer() *editor.Buffer { return c.buf }

// ViewFrame reports the editor viewport (top visible buffer line +
// visible row count) for the view-relative motions (H/M/L). It reads
// the live gocui view's vertical origin — pinned every frame by
// layout.go's FocusPoint call, with Wrap=false keeping buffer line ==
// view row — and its inner height. Returns a zero ViewFrame (which the
// motions treat as "viewport unavailable") when the GuiDriver or view
// is not yet wired, e.g. headless test rigs.
func (c *QueryEditorContext) ViewFrame() editor.ViewFrame {
	if c.deps.GuiDriver == nil {
		return editor.ViewFrame{}
	}
	v, err := c.deps.GuiDriver.ViewByName(c.GetViewName())
	if err != nil || v == nil {
		return editor.ViewFrame{}
	}
	return editor.ViewFrame{Top: v.OriginY(), Height: v.InnerHeight()}
}

// SetBuffer replaces the live *editor.Buffer with the supplied one.
// connectInvoker calls this post-Connect after LoadBuffer hydrates a
// persisted buffer from disk; SetBuffer keeps the per-context
// RepeatStore intact so a `.`-repeat survives a buffer reload (vim
// semantics — the last-edit replay is buffer-local but not file-local).
// A nil buf is rejected silently so callers can pass LoadBuffer's
// fallback unconditionally.
func (c *QueryEditorContext) SetBuffer(buf *editor.Buffer) {
	if buf == nil {
		return
	}
	c.buf = buf
}

// Repeat returns the per-context `.`-repeat state. Always non-nil.
func (c *QueryEditorContext) Repeat() *editor.RepeatStore { return c.repeat }

// HandleFocus flips ModeStore[QUERY_EDITOR] to ModeNormal so the
// Matcher uses Normal-mode dispatch for incoming keys. A nil modes
// setter (test wiring) is a no-op.
func (c *QueryEditorContext) HandleFocus(_ types.OnFocusOpts) error {
	if c.modes != nil {
		c.modes.Set(types.QUERY_EDITOR, types.ModeNormal)
	}
	return nil
}

// HandleFocusLost runs the four-step departure protocol
// (Architecture Decisions 3, 4, 6):
//
//  1. exitVisualIfActive   — editor.ExitVisual(c.buf) so
//     Selection never persists across a focus change (and therefore
//     never lands on disk).
//  2. modes.Reset            — drop the per-context Mode entry so a
//     subsequent re-focus starts from ModeNormal.
//  3. matcher.Cancel         — abort any half-built chord / pending
//     count + register state and hide WhichKey.
//  4. saveBufferIfDirty      — dispatches the SaveBuffer worker
//     when Dirty.
//
// Each step is nil-safe on its own; the sequence is idempotent across
// repeated focus/blur cycles.
func (c *QueryEditorContext) HandleFocusLost(_ types.OnFocusLostOpts) error {
	c.exitVisualIfActive()
	if c.modes != nil {
		c.modes.Reset(types.QUERY_EDITOR)
	}
	if c.matcher != nil {
		c.matcher.Cancel()
	}
	// drop any half-typed operator stash so a focus blur during
	// op-pending can't strand the next refocus in OperatorPending.
	// matcher.Cancel() above handles the Matcher's pending count/register
	// state; RepeatStore.PendingOpID is the action-handler-owned slot.
	if c.repeat != nil {
		c.repeat.PendingOpID = ""
	}
	return c.saveBufferIfDirty()
}

// exitVisualIfActive clears any live Selection on c.buf via
// editor.ExitVisual, then resets the mode entry so HandleFocusLost
// leaves QUERY_EDITOR in ModeNormal. Safe to call on a nil Buffer.
func (c *QueryEditorContext) exitVisualIfActive() {
	if c.buf == nil {
		return
	}
	editor.ExitVisual(c.buf)
}

// SetMode flips ModeStore[QUERY_EDITOR] to m. VimEditorController calls
// this from the v / V / <c-v> / <esc> handlers so the
// Matcher routes subsequent keys via the new mode mask. A nil modes
// setter (test wiring) is a no-op so test fakes can omit it.
func (c *QueryEditorContext) SetMode(m types.Mode) {
	if c.modes == nil {
		return
	}
	c.modes.Set(types.QUERY_EDITOR, m)
}

// saveBufferIfDirty dispatches a buffer save via deps.SaveBuffer when
// the live *editor.Buffer is Dirty. The buffer's String() snapshot is
// taken on the MainLoop (cheap — Buffer.String holds RLock for the
// duration) so the worker the orchestrator-bound SaveBuffer dispatches
// receives an immutable string and never touches Buffer state. After
// dispatch the Dirty flag is cleared so a focus-blur cycle without an
// intervening edit doesn't re-fire the save.
//
// Missing inputs (nil buf, nil hook, empty ConnectionID/UUID) make the
// call a silent no-op so test wiring without a Common.Fs / StateDir
// stays correct.
func (c *QueryEditorContext) saveBufferIfDirty() error {
	if c.buf == nil {
		return nil
	}
	if !c.buf.Dirty {
		return nil
	}
	if c.deps.SaveBuffer == nil {
		return nil
	}
	connID := c.buf.ConnectionID
	uuid := c.buf.UUID
	if connID == "" || uuid == "" {
		return nil
	}
	content := c.buf.String()
	c.deps.SaveBuffer(connID, uuid, content)
	c.buf.Dirty = false
	return nil
}

// GetKind overrides BaseContext.GetKind to publish MAIN_CONTEXT. The
// embedded BaseContext was constructed with kind=MAIN_CONTEXT already,
// but the override keeps the contract explicit at the receiver so
// later refactors that drop the explicit kind in setup.go stay sound.
func (c *QueryEditorContext) GetKind() types.ContextKind { return types.MAIN_CONTEXT }
