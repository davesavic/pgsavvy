package popup

import (
	"strings"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/theme"
)

// Panel is a body provider plus key-handling unit owned by a TabbedPopup tab.
//
// TabbedPopup itself does NOT route keys to panels; the orchestrator /
// controller wiring is responsible for invoking Panel.HandleKey. The
// interface is declared here so callers can compose panels into tabs
// without depending on a concrete panel implementation.
type Panel interface {
	Body() string
	HandleKey(types.Key) bool
}

// Tab pairs a human title with a Panel that supplies the tab body.
type Tab struct {
	Title string
	Panel Panel
}

// TabbedPopup is a minimal tabbed-popup state object. It owns the
// active-tab index, supports wrap-around cycling, and renders a header
// row + active-panel body via Body(). Color gating for the active
// title is delegated to a monochrome predicate (theme.IsMonochrome by
// default) so tests can exercise both render modes.
type TabbedPopup struct {
	tabs       []Tab
	active     int
	monochrome func() bool
}

// NewTabbedPopup constructs a TabbedPopup with the supplied tabs. The
// active index starts at 0. A nil/empty tabs slice is permitted; in that
// case Body() returns "" and the cycle/clamp methods are no-ops.
func NewTabbedPopup(tabs []Tab) *TabbedPopup {
	return newTabbedPopupWithMono(tabs, theme.IsMonochrome)
}

// newTabbedPopupWithMono is the test-friendly constructor that lets a
// fake monochrome predicate be injected. Same package only.
func newTabbedPopupWithMono(tabs []Tab, monochrome func() bool) *TabbedPopup {
	if monochrome == nil {
		monochrome = theme.IsMonochrome
	}
	return &TabbedPopup{
		tabs:       tabs,
		active:     0,
		monochrome: monochrome,
	}
}

// Active returns the current active-tab index. Zero when there are no
// tabs.
func (t *TabbedPopup) Active() int { return t.active }

// NextTab advances to the next tab, wrapping past the end back to 0.
// No-op when there are fewer than 2 tabs.
func (t *TabbedPopup) NextTab() {
	if len(t.tabs) < 2 {
		return
	}
	t.active = (t.active + 1) % len(t.tabs)
}

// PrevTab moves to the previous tab, wrapping past 0 to the last tab.
// No-op when there are fewer than 2 tabs.
func (t *TabbedPopup) PrevTab() {
	if len(t.tabs) < 2 {
		return
	}
	t.active = (t.active - 1 + len(t.tabs)) % len(t.tabs)
}

// SetActive sets the active index. Out-of-range values (negative or
// >= len(tabs)) are silently ignored — no panic, no clamp-to-edge.
func (t *TabbedPopup) SetActive(i int) {
	if i < 0 || i >= len(t.tabs) {
		return
	}
	t.active = i
}

// Body renders the header row followed by a blank line and the active
// panel's Body() output. The active title is wrapped in the yellow
// ANSI escape (\x1b[33m..\x1b[0m) when color is available, or in
// square brackets ([Title]) under monochrome mode.
//
// Returns "" when there are no tabs.
func (t *TabbedPopup) Body() string {
	if len(t.tabs) == 0 {
		return ""
	}
	mono := t.monochrome()

	var header strings.Builder
	for i, tab := range t.tabs {
		if i > 0 {
			header.WriteString("  ")
		}
		if i == t.active {
			if mono {
				header.WriteString("[")
				header.WriteString(tab.Title)
				header.WriteString("]")
			} else {
				header.WriteString("\x1b[33m")
				header.WriteString(tab.Title)
				header.WriteString("\x1b[0m")
			}
		} else {
			header.WriteString(tab.Title)
		}
	}

	var body string
	if p := t.tabs[t.active].Panel; p != nil {
		body = p.Body()
	}

	return header.String() + "\n\n" + body
}
