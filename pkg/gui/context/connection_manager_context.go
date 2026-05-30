package context

import (
	"fmt"
	"strings"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// ConnectionManagerMode is the modal's two-state render mode (dbsavvy-1rf).
// In ModeList the body draws the connection rows (or the empty-state hint);
// in ModeConnecting it draws the 3-state connecting / error / retry body,
// mirroring ConnectingContext so the connect lifecycle renders INSIDE the
// modal instead of pushing the standalone CONNECTING screen.
type ConnectionManagerMode int

const (
	// ModeList renders the connection rows. Default mode.
	ModeList ConnectionManagerMode = iota
	// ModeConnecting renders the connecting / error body.
	ModeConnecting
)

// ConnectionManagerContext is the centered modal connection-manager box
// (dbsavvy-ig4 scaffold, dbsavvy-1rf list + in-modal connect). MAIN_CONTEXT
// kind: when top of the focus stack the layout pass paints it as a centered
// bordered box over a blank background, suppressing both the side rails and
// the QUERY_EDITOR for that frame.
//
// It embeds SideListContext for the row slice + cursor (j/k nav), and holds
// a ConnectingState that the in-modal connect lifecycle writes (the same
// connecting/error state connectInvoker drives) so ModeConnecting can render
// it without forking the connect IO.
//
// State is transient, in-memory, and NOT goroutine-safe (mirrors
// ConnectingContext / SideListContext): SetItems / SetMode / the connecting
// setters are plain setters the caller serialises onto the UI thread.
//
// Strings are hardcoded English (mirrors ConnectingContext — i18n is not
// threaded through this context).
type ConnectionManagerContext struct {
	SideListContext

	mode       ConnectionManagerMode
	connecting ConnectingState

	// onShow populates the row slice + restores the last-used cursor when the
	// modal gains focus (dbsavvy-1rf). The orchestrator owns the connection
	// provider + last-id snapshot, so it injects this closure; nil leaves the
	// list untouched (scaffold / test wiring that seeds items directly).
	onShow func()
}

// Compile-time assertion that the live type satisfies the lifecycle
// contract.
var _ types.IBaseContext = (*ConnectionManagerContext)(nil)

// NewConnectionManagerContext builds the context bound to CONNECTION_MANAGER.
func NewConnectionManagerContext(base BaseContext, deps depsAlias) *ConnectionManagerContext {
	return &ConnectionManagerContext{SideListContext: NewSideListContext(base, deps)}
}

// SetOnShow injects the populate-on-focus closure (dbsavvy-1rf). The
// orchestrator wires it to refresh the row slice + restore the last-used
// cursor.
func (c *ConnectionManagerContext) SetOnShow(fn func()) { c.onShow = fn }

// HandleFocus fires when the modal is pushed onto the focus stack. It resets
// the modal to list mode (a re-open after a prior connecting/error session
// must land on the list) and runs the populate-on-focus closure so the rows +
// cursor are fresh. Nil-safe (dbsavvy-1rf).
func (c *ConnectionManagerContext) HandleFocus(_ types.OnFocusOpts) error {
	c.mode = ModeList
	if c.onShow != nil {
		c.onShow()
	}
	return nil
}

// Mode returns the current render mode.
func (c *ConnectionManagerContext) Mode() ConnectionManagerMode { return c.mode }

// SetMode switches the render mode. Plain setter — serialised onto the UI
// thread by the caller.
func (c *ConnectionManagerContext) SetMode(m ConnectionManagerMode) { c.mode = m }

// ConnectingState returns the shared connecting/error sink the in-modal
// connect lifecycle writes (connectInvoker drives SetConnecting / SetError on
// it). The pointer is stable for the life of the context.
func (c *ConnectionManagerContext) ConnectingState() *ConnectingState { return &c.connecting }

// HandleRender writes the modal body into the CONNECTION_MANAGER view. A nil
// GuiDriver is a silent no-op (test wiring / partial bootstrap) so the call
// never panics.
func (c *ConnectionManagerContext) HandleRender() error {
	viewName := c.GetViewName()
	body := c.body()
	writeView(c.deps, func() error {
		return c.deps.GuiDriver.SetContent(viewName, body)
	})
	return nil
}

// body returns the modal text for the current mode: the 3-state connecting
// body in ModeConnecting, otherwise the connection rows (or the empty-state
// hint).
func (c *ConnectionManagerContext) body() string {
	if c.mode == ModeConnecting {
		return c.connecting.Body()
	}
	if len(c.items) == 0 {
		return "[a] add"
	}
	return c.renderRows()
}

// renderRows produces the row text for the current Items slice, mirroring
// ConnectionsContext: the cursor row is prefixed with "> ", others with "  ";
// each row is "<icon> <name>  <host>/<db>" via deps.PerRowDecorationHook +
// deps.RowSuffix (active-connection marker + parsed endpoint).
func (c *ConnectionManagerContext) renderRows() string {
	var b strings.Builder
	for i, item := range c.items {
		marker := "  "
		if i == c.cursor {
			marker = "> "
		}
		conn, ok := item.(*models.Connection)
		if !ok {
			fmt.Fprintf(&b, "%s%v\n", marker, item)
			continue
		}
		if c.deps.PerRowDecorationHook != nil {
			icon, label, _ := c.deps.PerRowDecorationHook(conn)
			if label == "" {
				label = conn.Name
			}
			label += c.rowSuffix(conn)
			if icon != "" {
				fmt.Fprintf(&b, "%s%s %s\n", marker, icon, label)
			} else {
				fmt.Fprintf(&b, "%s%s\n", marker, label)
			}
			continue
		}
		fmt.Fprintf(&b, "%s%s%s\n", marker, conn.Name, c.rowSuffix(conn))
	}
	return b.String()
}

// rowSuffix returns the parsed "host/database" endpoint prefixed with two
// spaces so it reads as a separate column. Nil-safe: empty when the hook is
// unset or returns "".
func (c *ConnectionManagerContext) rowSuffix(conn *models.Connection) string {
	if c.deps.RowSuffix == nil {
		return ""
	}
	sfx := c.deps.RowSuffix(conn)
	if sfx == "" {
		return ""
	}
	return "  " + sfx
}

// GetKind overrides BaseContext.GetKind to publish MAIN_CONTEXT, mirroring
// ConnectingContext so a later refactor that drops the explicit kind in
// setup.go stays sound.
func (c *ConnectionManagerContext) GetKind() types.ContextKind { return types.MAIN_CONTEXT }
