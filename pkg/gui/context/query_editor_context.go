package context

import (
	"github.com/davesavic/dbsavvy/pkg/gui/editor"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// QueryEditorContext is the real top-right MAIN_CONTEXT pane that
// hosts the vim-style SQL editor (epic dbsavvy-wwd). dbsavvy-wwd.1
// promotes it from StubContext to a live BaseContext-embedding type
// so subsequent child tasks (wwd.2..wwd.10) have a stable handoff:
// they read/write the *editor.Buffer + *editor.RepeatStore exposed
// through Buffer() / Repeat() accessors.
//
// Focus wiring:
//   - HandleFocus flips ModeStore[QUERY_EDITOR] to ModeNormal so the
//     Matcher routes printable runes through Normal-mode dispatch
//     until wwd.10 wires Insert-mode entries (i/a/o/...).
//   - HandleFocusLost is the inverse: clear Visual selection, reset
//     ModeStore, cancel any half-built chord in the Matcher, and
//     dispatch a buffer save when Dirty. wwd.1 ships call-site stubs
//     for ExitVisual (wwd.7) and SaveBuffer (wwd.9); the order of the
//     four operations is the wwd contract.
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
// dependency bag (carried for parity with sibling constructors —
// wwd.4+ will consume GuiDriver from it). modes and matcher may be
// nil in test wiring; every focus hook nil-checks before calling.
//
// The *editor.Buffer / *editor.RepeatStore returned by Buffer() /
// Repeat() are always non-nil — Buffer uses editor.NewBuffer so
// Marks and Jumps are initialised before any wwd.5 motion handler
// can call buf.Jumps.Push or editor.SetMark; RepeatStore stays a
// zero-value shell until wwd.9 fills it.
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
// editor pane. Always non-nil. wwd.2 fills the body of *editor.Buffer.
func (c *QueryEditorContext) Buffer() *editor.Buffer { return c.buf }

// Repeat returns the per-context `.`-repeat state. Always non-nil.
// wwd.9 fills the body of *editor.RepeatStore.
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

// HandleFocusLost runs the four-step departure protocol the wwd epic
// freezes (Architecture Decisions 3, 4, 6):
//
//  1. exitVisualIfActive   — wwd.7 wires editor.ExitVisual(c.buf) so
//     Selection never persists across a focus change (and therefore
//     never lands on disk in wwd.9).
//  2. modes.Reset            — drop the per-context Mode entry so a
//     subsequent re-focus starts from ModeNormal.
//  3. matcher.Cancel         — abort any half-built chord / pending
//     count + register state and hide WhichKey.
//  4. saveBufferIfDirty      — wwd.9 dispatches the SaveBuffer worker
//     when Dirty; the stub here is a no-op and returns nil.
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
	return c.saveBufferIfDirty()
}

// exitVisualIfActive is the wwd.7 call-site stub. It safely tolerates
// a nil Buffer (the constructor never produces one, but tests may
// substitute) and is a no-op until wwd.7 lands editor.ExitVisual.
func (c *QueryEditorContext) exitVisualIfActive() {
	if c.buf == nil {
		return
	}
	// wwd.7: editor.ExitVisual(c.buf)
}

// saveBufferIfDirty is the wwd.9 call-site stub. wwd.9 fills it with
// the OnWorker(SaveBuffer) dispatch keyed off c.buf.Dirty; until then
// it is a no-op returning nil so HandleFocusLost's contract holds.
func (c *QueryEditorContext) saveBufferIfDirty() error {
	if c.buf == nil {
		return nil
	}
	// wwd.9: if c.buf.Dirty { deps.OnWorker(SaveBuffer(c.buf.LinesCopy())) }
	return nil
}

// GetKind overrides BaseContext.GetKind to publish MAIN_CONTEXT. The
// embedded BaseContext was constructed with kind=MAIN_CONTEXT already,
// but the override keeps the contract explicit at the receiver so
// later refactors that drop the explicit kind in setup.go stay sound.
func (c *QueryEditorContext) GetKind() types.ContextKind { return types.MAIN_CONTEXT }
