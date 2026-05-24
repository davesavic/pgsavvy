package context

import (
	"github.com/davesavic/dbsavvy/pkg/gui/popup"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// FKReversePickerContextKey aliases types.FK_REVERSE_PICKER. Z1
// (dbsavvy-bwq.23 Phase A) promoted the canonical ContextKey into
// pkg/gui/types/context.go; this alias is retained so existing callers
// (controllers, tests) keep compiling without a wider rename. New code
// should reference types.FK_REVERSE_PICKER directly.
const FKReversePickerContextKey = types.FK_REVERSE_PICKER

// FKReversePickerContext renders the gD reverse FK picker as a TabbedPopup
// (one tab per inbound FK). TEMPORARY_POPUP kind — the orchestrator's
// layout pass gates rendering on focus-stack membership, so HandleRender
// is only invoked while the popup is on top.
//
// The context is a thin state holder: state is owned by a
// *popup.TabbedPopup installed via SetState. The controller owns key
// dispatch (tab cycling, <CR> selection, close). Pattern mirrors
// TableInspectContext (dbsavvy-3vf). dbsavvy-bwq.17 (B6).
type FKReversePickerContext struct {
	BaseContext

	deps Deps

	state *popup.TabbedPopup
}

// NewFKReversePickerContext builds a context bound to the supplied
// BaseContext (the caller sets Key / ViewName / Kind via BaseContextOpts).
func NewFKReversePickerContext(base BaseContext, deps Deps) *FKReversePickerContext {
	return &FKReversePickerContext{BaseContext: base, deps: deps}
}

// SetState installs the TabbedPopup that supplies the rendered body.
// Nil is permitted: HandleRender emits an empty body when state is unset.
func (c *FKReversePickerContext) SetState(s *popup.TabbedPopup) { c.state = s }

// State returns the installed TabbedPopup or nil.
func (c *FKReversePickerContext) State() *popup.TabbedPopup { return c.state }

// HandleRender writes the popup body into the gocui view. Panels are
// responsible for SafeText-ing DB-supplied leaves (AD-17); the context
// does NOT re-strip the composed body — that would destroy the active-tab
// color escape and the inter-row newlines.
func (c *FKReversePickerContext) HandleRender() error {
	body := ""
	if c.state != nil {
		body = c.state.Body()
	}
	viewName := c.GetViewName()
	writeView(c.deps, func() error {
		return c.deps.GuiDriver.SetContent(viewName, body)
	})
	return nil
}
