package context

import (
	"sync"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// RelationshipPanelContext renders the <leader>gr right-docked FK-
// exploration sidebar (DISPLAY_CONTEXT kind). It is a pure render target:
// the RelationshipPanelController computes the body text off the focused
// grid row (raw FK metadata, ZERO DB queries) and pushes it here via
// SetBody; HandleRender writes the latest snapshot to the panel view.
//
// The grid retains keyboard focus while the panel is open (the
// orchestrator's Tier-4 focus pass keeps the underlying result-tab view
// current for the RELATIONSHIP_PANEL key), so this context never accepts
// input bindings — AddKeybindingsFn is a no-op, mirroring the which-key /
// limit DISPLAY_CONTEXT chrome.
type RelationshipPanelContext struct {
	BaseContext

	deps depsAlias

	mu   sync.RWMutex
	body string
}

// NewRelationshipPanelContext builds the context bound to
// RELATIONSHIP_PANEL. The body starts empty; HandleRender renders the
// empty-state placeholder until the controller pushes a snapshot.
func NewRelationshipPanelContext(base BaseContext, deps depsAlias) *RelationshipPanelContext {
	return &RelationshipPanelContext{BaseContext: base, deps: deps}
}

// SetBody installs the latest rendered body text. Called from the
// controller's debounced live-follow repaint (on the UI thread). The
// stored value is read back by HandleRender on the next paint.
func (p *RelationshipPanelContext) SetBody(body string) {
	p.mu.Lock()
	p.body = body
	p.mu.Unlock()
}

// Body returns the current snapshot. Exposed for tests + the controller's
// idempotent repaint guard.
func (p *RelationshipPanelContext) Body() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.body
}

// HandleRender writes the current body snapshot to the panel view. A nil
// GuiDriver is a silent no-op (unit tests / partial wiring).
func (p *RelationshipPanelContext) HandleRender() error {
	body := p.Body()
	viewName := p.GetViewName()
	writeView(p.deps, func() error {
		return p.deps.GuiDriver.SetContent(viewName, body)
	})
	return nil
}

// AddKeybindingsFn drops the contributor — DISPLAY_CONTEXT views are
// read-only chrome and never receive input bindings (the grid keeps
// focus). Overrides BaseContext to make the no-op explicit.
func (p *RelationshipPanelContext) AddKeybindingsFn(_ types.KeybindingsFn) {}
