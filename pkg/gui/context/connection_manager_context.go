package context

import (
	"fmt"
	"strings"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/status"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/i18n"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/theme"
)

// ConnectionManagerMode is the modal's two-state render mode.
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
	// ModeForm renders the add/edit form — all connection fields at once.
	// Editing of text fields routes through the PROMPT popup;
	// toggles + the driver selector flip in place.
	ModeForm
)

// ConnectionManagerContext is the centered modal connection-manager box
// (connection list + in-modal connect). MAIN_CONTEXT
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
	form       connForm

	// onShow populates the row slice + restores the last-used cursor when the
	// modal gains focus. The orchestrator owns the connection
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

// SetOnShow injects the populate-on-focus closure. The
// orchestrator wires it to refresh the row slice + restore the last-used
// cursor.
func (c *ConnectionManagerContext) SetOnShow(fn func()) { c.onShow = fn }

// HandleFocus fires when the modal gains focus — either a fresh push onto the
// stack or when a child popup (PROMPT, CONFIRM) pops and returns focus here.
// In ModeForm a child popup return must preserve the form; likewise ModeConnecting
// must survive the SSH-secret PROMPT popup popping mid-connect —
// otherwise a reset to ModeList makes body() draw the row list and swallows a
// subsequent dial error. In all other modes it resets to ModeList and refreshes
// the row data. Both connecting-mode exits (success pop, Esc cancel) already
// reset to ModeList, so the modal is never re-opened stranded in ModeConnecting.
// Nil-safe.
func (c *ConnectionManagerContext) HandleFocus(_ types.OnFocusOpts) error {
	if c.mode == ModeForm || c.mode == ModeConnecting {
		return nil
	}
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
		return c.connecting.BodyGlyph(c.activeStageGlyph())
	}
	if c.mode == ModeForm {
		return c.form.render()
	}
	if len(c.items) == 0 {
		return "[a] add"
	}
	return c.renderRows()
}

// activeStageGlyph resolves the spinner glyph drawn for the Active connect
// stage row (T3 AD5/AD6a). It reads the live wall-clock frame via the injected
// SpinnerFrame accessor so the glyph animates in lock-step with the status-bar
// spinner. The accessor is unset in test fixtures / partial bootstrap, so a nil
// accessor falls back to the frame-0 glyph — a static (non-animated) but valid
// spinner rune, never a panic.
func (c *ConnectionManagerContext) activeStageGlyph() rune {
	if c.deps.SpinnerFrame == nil {
		return status.SpinnerGlyph(0)
	}
	return status.SpinnerGlyph(c.deps.SpinnerFrame())
}

// --- Form drive surface -----------------------------------------------------
//
// The controller drives the in-place form through these exported methods
// rather than the unexported connForm type. All run on the UI thread.

// FormMoveFocus shifts the field cursor by delta (j/k/Tab nav).
func (c *ConnectionManagerContext) FormMoveFocus(delta int) { c.form.moveFocus(delta) }

// FormFocusedIsText reports whether the focused field is a text field (edited
// via the PROMPT popup).
func (c *ConnectionManagerContext) FormFocusedIsText() bool {
	return c.form.focusedSpec().kind == fieldText
}

// FormFocusedLabel returns the focused field's label (used as the prompt
// title).
func (c *ConnectionManagerContext) FormFocusedLabel() string {
	return c.form.focusedSpec().label
}

// FormFocusedValue returns the focused text field's current value (popup
// seed). Empty for non-text fields.
func (c *ConnectionManagerContext) FormFocusedValue() string {
	s := c.form.focusedSpec()
	if s.kind != fieldText {
		return ""
	}
	return c.form.textValue(s.id)
}

// FormFocusedValidator returns the focused text field's validator (nil for
// free-text fields or non-text rows).
func (c *ConnectionManagerContext) FormFocusedValidator(tr *i18n.TranslationSet) func(string) error {
	s := c.form.focusedSpec()
	if s.kind != fieldText {
		return nil
	}
	return c.form.validatorFor(s.id, tr)
}

// FormSetFocusedValue stores a (pre-validated) value into the focused text
// field and clears any inline error.
func (c *ConnectionManagerContext) FormSetFocusedValue(v string) {
	c.form.setTextValue(c.form.focusedSpec().id, v)
	c.form.err = ""
}

// FormSetError stamps an inline error string rendered under the focused
// field. Used when a PROMPT-popup submit fails validation.
func (c *ConnectionManagerContext) FormSetError(msg string) { c.form.err = msg }

// FormToggleFocused flips the focused toggle or cycles the driver selector
// (space / i on a non-text field).
func (c *ConnectionManagerContext) FormToggleFocused() { c.form.toggleFocused() }

// FormValidateAll runs save-time validation. On failure it stamps the inline
// error and moves focus onto the offending field, returning false. On success
// it returns the edited connection plus the add/edit metadata.
func (c *ConnectionManagerContext) FormValidateAll(tr *i18n.TranslationSet) (conn models.Connection, isEdit bool, originalName string, ok bool) {
	msg, idx, valid := c.form.validateAll(tr)
	if !valid {
		c.form.err = msg
		c.form.focus = idx
		return models.Connection{}, false, "", false
	}
	c.form.err = ""
	return c.form.conn, c.form.isEdit, c.form.originalName, true
}

// OpenAddForm seeds a blank add form and switches to ModeForm. existingNames
// is the snapshot used for the name-uniqueness check; driversFn supplies the
// driver selector list (nil → drivers.Names). The driver defaults to the
// first registered name so the selector starts on a valid value.
func (c *ConnectionManagerContext) OpenAddForm(existingNames []string, driversFn func() []string) {
	f := connForm{
		isEdit:        false,
		existingNames: existingNames,
		driversFn:     driversFn,
	}
	if names := f.names(); len(names) > 0 {
		f.conn.Driver = names[0]
	}
	c.form = f
	c.mode = ModeForm
}

// OpenEditForm seeds the form from an existing profile (rename support: the
// originalName is excluded from the uniqueness check) and switches to
// ModeForm.
func (c *ConnectionManagerContext) OpenEditForm(conn models.Connection, existingNames []string, driversFn func() []string) {
	c.form = connForm{
		conn:          conn,
		isEdit:        true,
		originalName:  conn.Name,
		existingNames: existingNames,
		driversFn:     driversFn,
	}
	c.mode = ModeForm
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
			icon, label, color := c.deps.PerRowDecorationHook(conn)
			if label == "" {
				label = conn.Name
			}
			label += c.rowSuffix(conn)
			if sgr := theme.AnsiFgSGR(color); sgr != "" {
				label = sgr + label + theme.AnsiReset
			}
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

// OptionsBarFilter returns a predicate that selects which ShowInBar action IDs
// are visible for the current internal mode. Each mode returns an explicit
// allowlist so list-only bindings (e.g. ConnectionManagerAdd/Delete) do not
// leak into the form bar. ConnectionManagerEdit is the single `i` edit action,
// advertised in both list and form mode (it routes by mode). The status bar
// renderer type-asserts to this method each frame.
func (c *ConnectionManagerContext) OptionsBarFilter() func(string) bool {
	switch c.mode {
	case ModeForm:
		return func(id string) bool {
			return id == commands.ConnectionManagerEdit ||
				id == commands.ConnectionManagerClose ||
				id == commands.ConnectionManagerConfirm
		}
	case ModeConnecting:
		return func(id string) bool {
			return id == commands.ConnectionManagerRetry ||
				id == commands.ConnectionManagerClose ||
				id == commands.ConnectionManagerConfirm
		}
	default: // ModeList
		return func(id string) bool {
			return id == commands.ConnectionManagerConfirm ||
				id == commands.ConnectionManagerClose ||
				id == commands.ConnectionManagerAdd ||
				id == commands.ConnectionManagerEdit ||
				id == commands.ConnectionManagerDelete
		}
	}
}

// GetKind overrides BaseContext.GetKind to publish MAIN_CONTEXT, mirroring
// ConnectingContext so a later refactor that drops the explicit kind in
// setup.go stays sound.
func (c *ConnectionManagerContext) GetKind() types.ContextKind { return types.MAIN_CONTEXT }
